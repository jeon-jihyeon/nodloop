package curate_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	knowledgefile "github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	"github.com/jeon-jihyeon/nodloop/internal/llm"
	"github.com/jeon-jihyeon/nodloop/internal/llm/llmmock"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	vetofile "github.com/jeon-jihyeon/nodloop/internal/veto/file"
)

// When the records of a bench were written, the Sunday before the testkit clock starts
var written = time.Date(2026, 9, 20, 9, 0, 0, 0, time.UTC)

// The scope of the runs of one repo in sessions
func acme(repo string, more ...string) *knowledge.RunScope {
	labels := trace.Labels{"repo": {repo}}
	if len(more) == 2 {
		labels[more[0]] = []string{more[1]}
	}
	return &knowledge.RunScope{Producer: "session", Labels: labels}
}

// A lesson drafted from the correction of its run in the scope with the status and the approver
func lesson(id string, version int, content string, scope *knowledge.RunScope, status knowledge.Status, approver string) knowledge.Knowledge {
	k := knowledge.Knowledge{
		ID: id, Version: version, Kind: knowledge.KindMeaning, Content: content, Run: scope, Basis: knowledge.BasisStated,
		Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{"r-" + id}}, Status: status, Approver: approver, Author: "claude",
		Drafted: true, Time: written,
	}
	if status == knowledge.StatusApproved {
		k.ApprovedAt = written
	}
	return k
}

// The person's lesson of the place repo=acme every bench starts with
func personLesson() knowledge.Knowledge {
	return lesson("k-person", 1, "Run the tests before a commit", acme("acme"), knowledge.StatusApproved, "ann")
}

// The stores of one pass with a judge whose every member gives one answer
type bench struct {
	stores testkit.Stores
	client *llmmock.MockClient
}

// A bench whose ledger holds the person's lesson and the lessons given, each with its run and a person's reject on it
// 1. the verdicts name more verdicts on a run after the reject in their order
// 2. a run whose id starts with r-unrecorded gets its verdicts and no trace
func newBench(t *testing.T, answer json.RawMessage, lessons []knowledge.Knowledge, verdicts map[string][]feedback.Verdict) bench {
	t.Helper()
	ctx := context.Background()
	b := bench{stores: testkit.Open(t), client: llmmock.NewMockClient(gomock.NewController(t))}
	b.client.EXPECT().Complete(gomock.Any(), gomock.Any()).Return(llm.Response{Output: answer, Model: "claude-sonnet-5-5"}, nil).AnyTimes()
	all := slices.Concat([]knowledge.Knowledge{personLesson()}, lessons)
	for _, k := range all {
		for _, id := range k.Evidence.FeedbackTraceIDs {
			b.correct(t, id, k.Run.Labels, slices.Concat([]feedback.Verdict{feedback.VerdictReject}, verdicts[id]))
		}
	}
	require.NoError(t, testkit.Err(b.stores.Ledger.Import(ctx, all)))
	return b
}

// Records the run in a place of the labels with the verdicts of a person on it in their order
func (b bench) correct(t *testing.T, id string, labels trace.Labels, verdicts []feedback.Verdict) {
	t.Helper()
	ctx := context.Background()
	place := trace.Labels{}
	for key, values := range labels {
		place[key] = values[:1]
	}
	run, err := trace.NewRun("session", "", place, json.RawMessage(`{"applied":[]}`), []byte("an answer"), written)
	require.NoError(t, err)
	run.ID = id
	run.SessionID = "s-" + id
	if _, err := b.stores.Traces.Get(ctx, id); err == nil {
		return
	}
	if !strings.HasPrefix(id, "r-unrecorded") {
		require.NoError(t, b.stores.Traces.Append(ctx, run.Record()))
	}
	for i, v := range verdicts {
		f, err := feedback.New(id, v, "", "", nil, "ann", written.Add(time.Duration(i+1)*time.Minute))
		require.NoError(t, err)
		require.NoError(t, b.stores.Feedback.Append(ctx, f.Record()))
	}
}

func (b bench) curator() *curate.Curator {
	return curate.New(b.stores.Ledger, b.stores.Traces, b.stores.Feedback, b.client, nil, seal, b.stores.Clock.Now)
}

// What a pass reads besides the records with the anchor of the ledger as it stands
func (b bench) setting(t *testing.T) curate.Setting {
	t.Helper()
	all, err := b.stores.Ledger.All(context.Background())
	require.NoError(t, err)
	return curate.Setting{
		Build: "0.10.0", Judge: sonnetJudge, Seed: "5eed", Anchor: curate.NewAnchor(all, nil, nil),
		Version: curate.AnchorVersion{Version: 1, Cause: curate.CauseApproval}, Since: written, Temp: curate.TempDirs{"/tmp"},
		Read: func(string) (string, error) { return "", os.ErrNotExist },
	}
}

// The decides of the pass as "kind id vN action rule tier" in sorted order
func decisionsOf(ds []curate.Decided) []string {
	out := []string{}
	for _, d := range ds {
		out = append(out, fmt.Sprintf("%s %s v%d %s %s %d", d.Change.Kind, d.Change.ID, d.Change.Version, d.Decision.Action, d.Decision.RuleCode, d.Decision.Tier))
	}
	slices.Sort(out)
	return out
}

// The newest record of every version the bench added as "id vN status approver reason" in sorted order
func statuses(t *testing.T, b bench) []string {
	t.Helper()
	all, err := b.stores.Ledger.All(context.Background())
	require.NoError(t, err)
	out := []string{}
	for _, k := range all.Versions() {
		if k.ID != "k-person" || k.Version != 1 {
			out = append(out, strings.TrimSpace(fmt.Sprintf("%s v%d %s %s %s", k.ID, k.Version, k.Status, k.Approver, k.Reason)))
		}
	}
	slices.Sort(out)
	return out
}

// Every tier rule sorts a candidate and only tier 0 writes the ledger while tier 1 runs in shadow
func TestCuratorPassTiers(t *testing.T) {
	candidate, approved := knowledge.StatusCandidate, knowledge.StatusApproved
	conform := answer(0.9, 0.02)
	vetoed := lesson("k-veto", 1, "Never force push", acme("acme"), candidate, "")
	vetoed.Kind = knowledge.KindJudgment
	vetoed.Veto = &knowledge.Veto{
		Tool: "Bash", When: []knowledge.VetoCondition{{Field: "command", Match: `push\s+--force`}}, Example: map[string]any{"command": "git push --force"},
	}
	next := lesson("k-person", 2, "Run the tests and the linter before a commit", acme("acme"), candidate, "")
	next.Base, next.Evidence.FeedbackTraceIDs = 1, []string{"r-next"}
	newer := lesson("k-newer", 1, "Name the failing test first", acme("acme"), candidate, "")
	newer.Time = written.Add(time.Hour)
	big := strings.Repeat("a long lesson ", 320)
	type args struct {
		lessons  []knowledge.Knowledge
		verdicts map[string][]feedback.Verdict
		answer   json.RawMessage
	}
	type want struct {
		decided  []string
		statuses []string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"an exact duplicate of a lesson a person approved is dropped",
			args{lessons: []knowledge.Knowledge{lesson("k-dup", 1, "run  the TESTS before a commit.", acme("acme"), candidate, "")}},
			want{[]string{"drop k-dup v1 drop same_as_item 0"}, []string{"k-dup v1 retired nodloop duplicate"}},
		},
		{
			"a newer candidate that says what an older one says joins its evidence to it and the older one waits in shadow",
			args{lessons: []knowledge.Knowledge{lesson("k-older", 1, "Name the failing test first", acme("acme"), candidate, ""), newer}, answer: conform},
			want{[]string{"add k-older v1 shadow  1", "join k-newer v1 join same_as_candidate 0"}, []string{"k-newer v1 retired nodloop duplicate", "k-older v1 candidate"}},
		},
		{
			"a candidate whose run a person approved since is dropped for no evidence",
			args{
				lessons:  []knowledge.Knowledge{lesson("k-undone", 1, "Pin the base image", acme("acme"), candidate, "")},
				verdicts: map[string][]feedback.Verdict{"r-k-undone": {feedback.VerdictApprove}},
			},
			want{[]string{"drop k-undone v1 drop no_evidence 0"}, []string{"k-undone v1 retired nodloop no_evidence"}},
		},
		{
			"a candidate scoped to a scratchpad is dropped",
			args{lessons: []knowledge.Knowledge{lesson("k-scratch", 1, "Keep the notes short", acme("acme", "dir", "/work/scratchpad/notes"), candidate, "")}},
			want{[]string{"drop k-scratch v1 drop temp_scope 0"}, []string{"k-scratch v1 retired nodloop temp_scope"}},
		},
		{
			"a candidate scoped below the temporary directory is dropped",
			args{lessons: []knowledge.Knowledge{lesson("k-tmp", 1, "Keep the notes short", acme("acme", "dir", "/tmp/acme"), candidate, "")}},
			want{[]string{"drop k-tmp v1 drop temp_scope 0"}, []string{"k-tmp v1 retired nodloop temp_scope"}},
		},
		{
			"a tmp folder inside a repository is a place like any other",
			args{lessons: []knowledge.Knowledge{lesson("k-repo-tmp", 1, "Keep the notes short", acme("acme", "dir", "tmp/notes"), candidate, "")}, answer: conform},
			want{[]string{"add k-repo-tmp v1 shadow  1"}, []string{"k-repo-tmp v1 candidate"}},
		},
		{
			"a scope no lesson a person approved reaches asks the person",
			args{lessons: []knowledge.Knowledge{lesson("k-other", 1, "Use the staging cluster", acme("svc"), candidate, "")}},
			want{[]string{"add k-other v1 ask scope_wider 2"}, []string{"k-other v1 candidate"}},
		},
		{
			"the next version of a lesson a person approved asks the person",
			args{lessons: []knowledge.Knowledge{next}},
			want{[]string{"update k-person v2 ask person_item 2"}, []string{"k-person v2 candidate"}},
		},
		{
			"a candidate with a veto asks the person",
			args{lessons: []knowledge.Knowledge{vetoed}},
			want{[]string{"add k-veto v1 ask veto 2"}, []string{"k-veto v1 candidate"}},
		},
		{
			"an add the judge finds against asks the person",
			args{lessons: []knowledge.Knowledge{lesson("k-against", 1, "Skip the tests", acme("acme"), candidate, "")}, answer: answer(0.9, 0.8)},
			want{[]string{"add k-against v1 ask judge_against 2"}, []string{"k-against v1 candidate"}},
		},
		{
			"an add the judge finds the anchor silent on asks the person",
			args{lessons: []knowledge.Knowledge{lesson("k-silent", 1, "Use tabs", acme("acme"), candidate, "")}, answer: answer(0.2, 0.1)},
			want{[]string{"add k-silent v1 ask judge_undecided 2"}, []string{"k-silent v1 candidate"}},
		},
		{
			"an add the judge finds conform in a bin not yet open asks the person",
			args{lessons: []knowledge.Knowledge{lesson("k-b2", 1, "Run the linter too", acme("acme"), candidate, "")}, answer: answer(0.9, 0.1)},
			want{[]string{"add k-b2 v1 ask kind_closed 2"}, []string{"k-b2 v1 candidate"}},
		},
		{
			"an approval that needs room asks the person with the compaction",
			args{lessons: []knowledge.Knowledge{
				lesson("k-big1", 1, "first "+big, acme("big"), approved, "ann"), lesson("k-big2", 1, "second "+big, acme("big"), approved, "ann"),
				lesson("k-more", 1, "third "+big, acme("big"), candidate, ""),
			}},
			want{[]string{"add k-more v1 ask room 2"}, []string{"k-big1 v1 approved ann", "k-big2 v1 approved ann", "k-more v1 candidate"}},
		},
		{
			"a place where lessons nodloop approved reach the person's asks to reanchor first",
			args{lessons: []knowledge.Knowledge{
				lesson("k-carried", 1, "Squash before a merge", acme("acme"), approved, knowledge.ApproverNodloop),
				lesson("k-drift", 1, "Rebase before a push", acme("acme"), candidate, ""),
			}},
			want{[]string{"add k-drift v1 ask drift 2"}, []string{"k-carried v1 approved nodloop", "k-drift v1 candidate"}},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := newBench(t, tc.args.answer, tc.args.lessons, tc.args.verdicts)

			got, err := b.curator().Pass(context.Background(), b.setting(t))

			require.NoError(t, err)
			assert.Equal(t, tc.want.decided, decisionsOf(got.Decided))
			assert.Equal(t, tc.want.statuses, statuses(t, b))
		})
	}
}

// The knob event of the cycle the bench's first pass ran in with the knobs the change gives as a narrowing of it
func (b bench) narrow(t *testing.T, event curate.KnobEvent, change func(*curate.Knobs)) {
	t.Helper()
	event.Cause = curate.KnobNarrow
	change(&event.Knobs)
	tr, err := event.Trace("0.10.0", b.stores.Clock.Now())
	require.NoError(t, err)
	require.NoError(t, b.stores.Traces.Append(context.Background(), tr.Record()))
}

// The decision a knowledge record of the version names
func (b bench) decision(t *testing.T, id string, version int) string {
	t.Helper()
	all, err := b.stores.Ledger.All(context.Background())
	require.NoError(t, err)
	k, err := all.Version(id, version)
	require.NoError(t, err)
	return k.Decision
}

// A pass decides each version once and decides one it left in shadow again once the shadow ended
func TestCuratorPassAgain(t *testing.T) {
	candidate := knowledge.StatusCandidate
	b := newBench(t, answer(0.9, 0.02), []knowledge.Knowledge{
		lesson("k-add", 1, "Name the failing test first", acme("acme"), candidate, ""),
		lesson("k-dup", 1, "Run the tests before a commit", acme("acme"), candidate, ""),
	}, nil)
	ctx := context.Background()
	first, err := b.curator().Pass(ctx, b.setting(t))
	require.NoError(t, err)

	second, err := b.curator().Pass(ctx, b.setting(t))
	require.NoError(t, err)
	b.narrow(t, first.Event, func(k *curate.Knobs) { k.Activated = true })
	third, err := b.curator().Pass(ctx, b.setting(t))
	require.NoError(t, err)

	assert.Equal(t, []string{"add k-add v1 shadow  1", "drop k-dup v1 drop same_as_item 0"}, decisionsOf(first.Decided))
	assert.Equal(t, []string{}, decisionsOf(second.Decided))
	require.Equal(t, []string{"add k-add v1 apply  1"}, decisionsOf(third.Decided))
	assert.Equal(t, []string{"k-add v1 approved nodloop", "k-dup v1 retired nodloop duplicate"}, statuses(t, b))
	assert.Equal(t, third.Decided[0].ID, b.decision(t, "k-add", 1))
	assert.Equal(t, first.Decided[1].ID, b.decision(t, "k-dup", 1))
}

// A decide a crash left before its ledger write is carried out by the next pass under the same decide and only while the version waits
func TestCuratorPassRecovers(t *testing.T) {
	candidate := knowledge.StatusCandidate
	gone := lesson("k-gone", 1, "Run the tests before a commit", acme("acme"), knowledge.StatusRetired, "ann")
	gone.Reason, gone.Time = knowledge.ReasonPerson, written.Add(time.Hour)
	type args struct {
		lessons []knowledge.Knowledge
		id      string
	}
	type want struct {
		statuses []string
		decision string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"a drop with no record that names it is carried out",
			args{[]knowledge.Knowledge{lesson("k-dup", 1, "Run the tests before a commit", acme("acme"), candidate, "")}, "k-dup"},
			want{[]string{"k-dup v1 retired nodloop duplicate"}, "d-crash"},
		},
		{
			"a drop of a version a person retired since is left as it is",
			args{[]knowledge.Knowledge{lesson("k-gone", 1, "Run the tests before a commit", acme("acme"), candidate, ""), gone}, "k-gone"},
			want{[]string{"k-gone v1 retired ann person"}, ""},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := newBench(t, nil, tc.args.lessons, nil)
			event := curate.Event{
				ID: "d-crash", Ref: "r-" + tc.args.id, Source: trace.SourcePush,
				Draw: trace.NewDraw("2026-09-21", "0.10.0+1a2b3c4d", "5eed", "d-crash", "audit", 0.05),
			}
			change := curate.Change{Kind: curate.ChangeDrop, ID: tc.args.id, Version: 1, Anchor: 1}
			drop := curate.Decision{
				RuleCode: curate.RuleSameAsItem, Action: curate.ActionDrop, Reason: knowledge.ReasonDuplicate, Failure: curate.FailureHard,
				Reopen: curate.ReopenPersonOnly,
			}
			tr, err := event.Decide(change, drop, nil, written.Add(2*time.Hour))
			require.NoError(t, err)
			require.NoError(t, b.stores.Traces.Append(ctx, tr.Record()))

			got, err := b.curator().Pass(ctx, b.setting(t))
			require.NoError(t, err)
			again, err := b.curator().Pass(ctx, b.setting(t))
			require.NoError(t, err)

			assert.Empty(t, got.Decided)
			assert.Empty(t, again.Decided)
			assert.Equal(t, tc.want.statuses, statuses(t, b))
			assert.Equal(t, tc.want.decision, b.decision(t, tc.args.id, 1))
		})
	}
}

// A judge call that fails leaves the change for the next pass once and then it is decided as split
func TestCuratorPassRetry(t *testing.T) {
	ctx := context.Background()
	b := bench{stores: testkit.Open(t), client: llmmock.NewMockClient(gomock.NewController(t))}
	b.client.EXPECT().Complete(gomock.Any(), gomock.Any()).Return(llm.Response{}, assert.AnError).AnyTimes()
	k := lesson("k-add", 1, "Name the failing test first", acme("acme"), knowledge.StatusCandidate, "")
	for _, l := range []knowledge.Knowledge{personLesson(), k} {
		b.correct(t, l.Evidence.FeedbackTraceIDs[0], l.Run.Labels, []feedback.Verdict{feedback.VerdictReject})
	}
	require.NoError(t, testkit.Err(b.stores.Ledger.Import(ctx, []knowledge.Knowledge{personLesson(), k})))

	first, err := b.curator().Pass(ctx, b.setting(t))
	require.NoError(t, err)
	second, err := b.curator().Pass(ctx, b.setting(t))
	require.NoError(t, err)
	third, err := b.curator().Pass(ctx, b.setting(t))
	require.NoError(t, err)

	assert.Equal(t, []string{"add k-add v1 retry  1"}, decisionsOf(first.Decided))
	assert.Equal(t, []string{"add k-add v1 ask judge_split 2"}, decisionsOf(second.Decided))
	assert.Empty(t, third.Decided)
	retried, err := b.stores.Traces.Get(ctx, first.Decided[0].ID)
	require.NoError(t, err)
	assert.Contains(t, retried.Error, assert.AnError.Error())
	assert.Equal(t, curate.OutcomeSplit, second.Decided[0].Decision.Outcome)
	assert.Equal(t, curate.FailureRepairable, first.Decided[0].Decision.Failure)
}

// The first pass of a cycle appends its knob event with the seed and the few-shot sets and the cycle after it keeps the judge
func TestCuratorPassCycles(t *testing.T) {
	ctx := context.Background()
	b := newBench(t, nil, nil, nil)
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	curator := curate.New(b.stores.Ledger, b.stores.Traces, b.stores.Feedback, b.client, nil, seal, clock)
	setting := b.setting(t)
	type cycle struct {
		id, seed, before string
		judgeStart       time.Time
		sets             int
	}
	var got []cycle

	_, err := curator.Pass(ctx, setting)
	require.NoError(t, err)
	now = now.Add(24 * time.Hour)
	_, err = curator.Pass(ctx, setting)
	require.NoError(t, err)
	now = time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)
	setting.Seed = "5eed2"
	third, err := curator.Pass(ctx, setting)
	require.NoError(t, err)
	knobs, err := b.stores.Traces.List(ctx, record.TraceFilter{Name: string(trace.NameKnob)})
	require.NoError(t, err)
	for _, tr := range slices.Backward(knobs) {
		k, ok := curate.RestoreKnob(trace.Restore(tr))
		require.True(t, ok)
		before := ""
		if k.Before != "" {
			before = "first"
		}
		got = append(got, cycle{k.CycleID, k.Seed, before, k.Judge.Start, len(slices.Concat(k.Judge.Sets...))})
	}

	start := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	assert.Equal(t, []cycle{{"2026-09-21", "5eed", "", start, 3}, {"2026-10-05", "5eed2", "first", start, 3}}, got)
	assert.Equal(t, knobs[1].ID, third.Event.Before)
	assert.Equal(t, curate.StartKnobs(), third.Event.Knobs)
	assert.Equal(t, curate.Mark{
		At: now, CycleID: "2026-10-05", PolicyVersion: third.Event.Policy("0.10.0"), Seed: "5eed2", Knobs: curate.StartKnobs(), Due: []curate.Due{},
	}, third.Mark(now))
}

// Extract traces of a conflict with k-carried and with the person's lesson beside one that does not decode and one that names nothing
func (b bench) conflicts(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	outputs := map[string]string{
		"e-k-carried": `{"conclusion":"conflict","related":{"id":"k-carried","version":1,"status":"approved"}}`,
		"e-k-person":  `{"conclusion":"conflict","related":{"id":"k-person","version":1,"status":"approved"}}`,
		"e-torn":      `["conflict"]`,
		"e-nothing":   `{"conclusion":"conflict"}`,
	}
	for id, output := range outputs {
		extract := trace.Trace{ID: id, Name: trace.NameExtract, Ref: "r-k-carried", Time: written.Add(time.Hour), Input: json.RawMessage(`{}`), Output: json.RawMessage(output)}
		require.NoError(t, b.stores.Traces.Append(ctx, extract.Record()))
	}
}

// Appends the knob event of the cycle of the testkit clock with the start knobs the change gives
func (b bench) knobs(t *testing.T, change func(*curate.Knobs)) {
	t.Helper()
	ev := curate.KnobEvent{
		CycleID: "2026-09-21", Start: time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC), Seed: "5eed", Cause: curate.KnobStart,
		Judge: curate.JudgeConfig{}.Next(sonnetJudge, nil, written), Knobs: curate.StartKnobs(),
	}
	change(&ev.Knobs)
	tr, err := ev.Trace("0.10.0", written)
	require.NoError(t, err)
	require.NoError(t, b.stores.Traces.Append(context.Background(), tr.Record()))
}

// A withdrawal while the kind is closed asks the person since it asks no judge
func TestCuratorPassWithdrawalClosed(t *testing.T) {
	b := newBench(t, nil, []knowledge.Knowledge{
		lesson("k-carried", 1, "Squash before a merge", acme("acme"), knowledge.StatusApproved, knowledge.ApproverNodloop),
	}, nil)
	b.conflicts(t)
	b.knobs(t, func(k *curate.Knobs) { k.Open = []curate.Opening{{Kind: curate.ChangeAdd, Bin: curate.Bin1}} })

	got, err := b.curator().Pass(context.Background(), b.setting(t))

	require.NoError(t, err)
	assert.Equal(t, []string{"withdraw k-carried v1 ask kind_closed 2"}, decisionsOf(got.Decided))
	assert.Equal(t, []string{"k-carried v1 approved nodloop"}, statuses(t, b))
	require.Len(t, got.Due, 1)
	assert.Equal(t, curate.QuestionDirection, got.Due[0].Kind)
}

// A conflict an extraction found with a version nodloop approved is a withdrawal decided in shadow and carried out once the shadow ended
func TestCuratorPassWithdrawal(t *testing.T) {
	ctx := context.Background()
	b := newBench(t, nil, []knowledge.Knowledge{
		lesson("k-carried", 1, "Squash before a merge", acme("acme"), knowledge.StatusApproved, knowledge.ApproverNodloop),
	}, nil)
	b.conflicts(t)

	first, err := b.curator().Pass(ctx, b.setting(t))
	require.NoError(t, err)
	b.narrow(t, first.Event, func(k *curate.Knobs) { k.Activated = true })
	second, err := b.curator().Pass(ctx, b.setting(t))
	require.NoError(t, err)

	assert.Equal(t, []string{"withdraw k-carried v1 shadow  1"}, decisionsOf(first.Decided))
	require.Equal(t, []string{"withdraw k-carried v1 withdraw  1"}, decisionsOf(second.Decided))
	assert.Equal(t, "e-k-carried", second.Decided[0].Change.Signal)
	assert.Equal(t, []string{"k-carried v1 retired nodloop conflict"}, statuses(t, b))
	assert.Equal(t, second.Decided[0].ID, b.decision(t, "k-carried", 1))
}

// Each decide that leaves a question lists it due once in the order it was decided until an answer or a person's decision settles it
func TestCuratorPassDue(t *testing.T) {
	ctx := context.Background()
	candidate := knowledge.StatusCandidate
	b := newBench(t, answer(0.9, 0.02), []knowledge.Knowledge{
		lesson("k-ask", 1, "Use the staging cluster", acme("svc"), candidate, ""),
		lesson("k-shadow", 1, "Name the failing test first", acme("acme"), candidate, ""),
		lesson("k-dup", 1, "Run the tests before a commit", acme("acme"), candidate, ""),
	}, nil)
	b.knobs(t, func(k *curate.Knobs) { k.Audit = curate.Audit{Tier0: 1, Tier1: 1} })
	type due struct {
		kind  curate.QuestionKind
		ref   string
		rule  curate.RuleCode
		run   string
		place string
	}
	view := func(ds []curate.Due) []due {
		out := []due{}
		for _, d := range ds {
			out = append(out, due{d.Kind, fmt.Sprintf("%s v%d", d.Ref.ID, d.Ref.Version), d.Rule, d.Run, d.Place.String()})
		}
		slices.SortFunc(out, func(a, b due) int { return strings.Compare(a.ref, b.ref) })
		return out
	}

	first, err := b.curator().Pass(ctx, b.setting(t))
	require.NoError(t, err)
	shadowed := slices.IndexFunc(first.Decided, func(d curate.Decided) bool { return d.Change.ID == "k-shadow" })
	require.GreaterOrEqual(t, shadowed, 0)
	answered := trace.Trace{ID: "a-1", Name: trace.NameAnswer, Ref: "x-1", Time: written.Add(3 * time.Hour), Input: json.RawMessage(`{"decide":"` + first.Decided[shadowed].ID + `"}`)}
	require.NoError(t, b.stores.Traces.Append(ctx, answered.Record()))
	require.NoError(t, testkit.Err(b.stores.Ledger.Retire(ctx, "k-ask", 1, "ann")))
	second, err := b.curator().Pass(ctx, b.setting(t))
	require.NoError(t, err)

	assert.Equal(t, []due{
		{curate.QuestionDirection, "k-ask v1", curate.RuleScopeWider, "r-k-ask", "runs of session. repo=svc"},
		{curate.QuestionAudit, "k-dup v1", curate.RuleSameAsItem, "r-k-dup", "runs of session. repo=acme"},
		{curate.QuestionShadow, "k-shadow v1", "", "r-k-shadow", "runs of session. repo=acme"},
	}, view(first.Due))
	assert.Equal(t, []due{
		{curate.QuestionAudit, "k-dup v1", curate.RuleSameAsItem, "r-k-dup", "runs of session. repo=acme"},
	}, view(second.Due))
}

// The corrected runs of the window no extraction read and those whose only extraction failed
func TestCuratorGaps(t *testing.T) {
	ctx := context.Background()
	b := newBench(t, nil, []knowledge.Knowledge{lesson("k-cited", 1, "Pin the base image", acme("acme"), knowledge.StatusCandidate, "")}, nil)
	now := b.stores.Clock.Now()
	labels := trace.Labels{"repo": {"acme"}}
	for _, id := range []string{"r-gap", "r-failed-once", "r-failed-twice", "r-refused", "r-young"} {
		b.correct(t, id, labels, []feedback.Verdict{feedback.VerdictReject})
	}
	b.correct(t, "r-approved", labels, []feedback.Verdict{feedback.VerdictReject, feedback.VerdictApprove})
	young, err := feedback.New("r-young", feedback.VerdictReject, "", "", nil, "ann", now.Add(-10*time.Minute))
	require.NoError(t, err)
	require.NoError(t, b.stores.Feedback.Append(ctx, young.Record()))
	inferred, err := trace.NewRun("tep", "", trace.Labels{"unit": {"a"}}, json.RawMessage(`{"applied":[]}`), []byte("an answer"), written)
	require.NoError(t, err)
	inferred.ID = "r-inferred"
	require.NoError(t, b.stores.Traces.Append(ctx, inferred.Record()))
	said, err := feedback.New("r-inferred", feedback.VerdictReject, "", "", nil, feedback.ReviewerSession, written.Add(time.Minute))
	require.NoError(t, err)
	require.NoError(t, b.stores.Feedback.Append(ctx, said.Record()))
	failed, err := trace.NewRun("session", "", labels, json.RawMessage(`{"applied":[]}`), []byte("an answer"), written)
	require.NoError(t, err)
	failed.ID, failed.Error = "r-error", "the run failed"
	require.NoError(t, b.stores.Traces.Append(ctx, failed.Record()))
	onFailed, err := feedback.New("r-error", feedback.VerdictReject, "", "", nil, "ann", written.Add(time.Minute))
	require.NoError(t, err)
	require.NoError(t, b.stores.Feedback.Append(ctx, onFailed.Record()))
	for i, e := range []struct{ run, conclusion string }{
		{"r-failed-once", "failed"}, {"r-failed-twice", "failed"}, {"r-failed-twice", "failed"}, {"r-refused", "refused"},
	} {
		extract := trace.Trace{
			ID: fmt.Sprintf("e-%d", i), Name: trace.NameExtract, Ref: e.run, Time: written.Add(time.Hour), Input: json.RawMessage(`{}`),
			Output: json.RawMessage(`{"conclusion":"` + e.conclusion + `"}`),
		}
		require.NoError(t, b.stores.Traces.Append(ctx, extract.Record()))
	}

	got, err := b.curator().Gaps(ctx, time.Time{})

	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"r-gap", "r-failed-once", "r-inferred"}, got)
}

// A store that cannot be read fails the pass and the gaps before anything is decided
func TestCuratorPassUnreadable(t *testing.T) {
	ctx := context.Background()
	b := newBench(t, nil, nil, nil)
	broken := testkit.Open(t)
	require.NoError(t, os.RemoveAll(broken.Dir+"/traces.jsonl"))
	require.NoError(t, os.Mkdir(broken.Dir+"/traces.jsonl", 0o700))
	curator := curate.New(b.stores.Ledger, broken.Traces, b.stores.Feedback, b.client, nil, seal, b.stores.Clock.Now)

	_, passErr := curator.Pass(ctx, b.setting(t))
	_, gapsErr := curator.Gaps(ctx, time.Time{})

	assert.Error(t, passErr)
	assert.Error(t, gapsErr)
}

// A candidate is decided from what its evidence cites wherever the run lies and waits as before when it cites no run
func TestCuratorPassEvidence(t *testing.T) {
	candidate := knowledge.StatusCandidate
	missing := lesson("k-missing", 1, "Name the failing test first", acme("acme"), candidate, "")
	missing.Evidence.FeedbackTraceIDs = []string{"r-unrecorded"}
	outcome := lesson("k-outcome", 1, "Name the failing test first", acme("acme"), candidate, "")
	outcome.Evidence = knowledge.Evidence{OutcomeTraceIDs: []string{"r-outcome"}}
	uncited := lesson("k-uncited", 1, "Name the failing test first", acme("acme"), candidate, "")
	uncited.Evidence = knowledge.Evidence{Knowledge: []knowledge.Ref{{ID: "k-person", Version: 1}}}
	retired := lesson("k-turned", 1, "Run the linter", acme("acme"), knowledge.StatusRetired, "ann")
	retired.Reason = knowledge.ReasonPerson
	turned := lesson("k-turned", 2, "Run the linter first", acme("acme"), candidate, "")
	turned.Base, turned.Evidence.FeedbackTraceIDs = 1, []string{"r-turned"}
	type args struct {
		lessons []knowledge.Knowledge
		// The window starts after every run of the bench
		late bool
	}
	type want struct {
		decided []string
		refs    []string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"a run outside the window is read from the store",
			args{[]knowledge.Knowledge{lesson("k-old", 1, "Name the failing test first", acme("acme"), candidate, "")}, true},
			want{[]string{"add k-old v1 shadow  1"}, []string{"r-k-old"}},
		},
		{"a run never recorded leaves the change with no place of its own", args{[]knowledge.Knowledge{missing}, false}, want{[]string{"add k-missing v1 shadow  1"}, []string{"r-unrecorded"}}},
		{"a candidate of an outcome alone cites the run of the outcome", args{[]knowledge.Knowledge{outcome}, false}, want{[]string{"add k-outcome v1 shadow  1"}, []string{"r-outcome"}}},
		{"a candidate that cites no run waits as before", args{[]knowledge.Knowledge{uncited}, false}, want{[]string{}, []string{}}},
		{"a version built on one a person retired asks the person", args{[]knowledge.Knowledge{retired, turned}, false}, want{[]string{"add k-turned v2 ask person_item 2"}, []string{"r-turned"}}},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := newBench(t, answer(0.9, 0.02), tc.args.lessons, nil)
			setting := b.setting(t)
			if tc.args.late {
				setting.Window = written.Add(time.Hour)
			}

			got, err := b.curator().Pass(ctx, setting)

			require.NoError(t, err)
			refs := []string{}
			for _, d := range got.Decided {
				refs = append(refs, d.Ref)
			}
			assert.Equal(t, tc.want.decided, decisionsOf(got.Decided))
			assert.Equal(t, tc.want.refs, refs)
		})
	}
}

// A failure on a change records nothing it cannot stand behind and joins the error of the pass
func TestCuratorPassFails(t *testing.T) {
	dup := lesson("k-dup", 1, "Run the tests before a commit", acme("acme"), knowledge.StatusCandidate, "")
	add := lesson("k-add", 1, "Name the failing test first", acme("acme"), knowledge.StatusCandidate, "")
	older := lesson("k-older", 1, "Keep commits small", acme("acme"), knowledge.StatusCandidate, "")
	newer := lesson("k-newer", 1, "Keep commits small", acme("acme"), knowledge.StatusCandidate, "")
	newer.Time, newer.Evidence.FeedbackTraceIDs = written.Add(time.Hour), []string{"r-k-newer", "r-k-newer2"}
	type args struct {
		lessons []knowledge.Knowledge
		// The start of the cycle names few-shot decisions the records lack
		unknownShots bool
		// Appends, lists of traces and reads of the verdicts that pass before every later one fails and none fails when negative
		appends, lists, reads int
		seed                  string
		// The knowledge cannot be read
		unreadable bool
	}
	type want struct {
		err      error
		statuses []string
		decides  int
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"a first pass with no seed starts no cycle",
			args{lessons: []knowledge.Knowledge{dup}, appends: -1, lists: -1, reads: -1}, want{curate.ErrKnobInvalid, []string{"k-dup v1 candidate"}, 0},
		},
		{
			"few-shot sets that name a decision the records lack fail the judged change",
			args{lessons: []knowledge.Knowledge{add}, unknownShots: true, appends: -1, lists: -1, reads: -1, seed: "5eed"},
			want{curate.ErrShotUnknown, []string{"k-add v1 candidate"}, 0},
		},
		{
			"a decide that fails to append writes nothing to the ledger",
			args{lessons: []knowledge.Knowledge{dup}, appends: 1, lists: -1, reads: -1, seed: "5eed"}, want{assert.AnError, []string{"k-dup v1 candidate"}, 0},
		},
		{
			"verdicts that cannot be read fail the change",
			args{lessons: []knowledge.Knowledge{add}, appends: -1, lists: -1, reads: 0, seed: "5eed"}, want{assert.AnError, []string{"k-add v1 candidate"}, 0},
		},
		{
			"verdicts the judge cannot read fail the change",
			args{lessons: []knowledge.Knowledge{add}, appends: -1, lists: -1, reads: 1, seed: "5eed"}, want{assert.AnError, []string{"k-add v1 candidate"}, 0},
		},
		{
			"verdicts that cannot be read leave a join to the next pass",
			args{lessons: []knowledge.Knowledge{older, newer}, appends: -1, lists: -1, reads: 0, seed: "5eed"},
			want{assert.AnError, []string{"k-newer v1 candidate", "k-older v1 candidate"}, 1},
		},
		{
			"knowledge that cannot be read fails the pass before it starts a cycle",
			args{lessons: []knowledge.Knowledge{add}, appends: -1, lists: -1, reads: -1, seed: "5eed", unreadable: true},
			want{assert.AnError, []string{"k-add v1 candidate"}, 0},
		},
		{
			"decides that cannot be read fail the pass", args{lessons: []knowledge.Knowledge{dup}, appends: -1, lists: 1, reads: -1, seed: "5eed"},
			want{assert.AnError, []string{"k-dup v1 candidate"}, 0},
		},
		{
			"answers that cannot be read fail the pass", args{lessons: []knowledge.Knowledge{dup}, appends: -1, lists: 2, reads: -1, seed: "5eed"},
			want{assert.AnError, []string{"k-dup v1 candidate"}, 0},
		},
		{
			"runs that cannot be read fail the pass", args{lessons: []knowledge.Knowledge{dup}, appends: -1, lists: 3, reads: -1, seed: "5eed"},
			want{assert.AnError, []string{"k-dup v1 candidate"}, 0},
		},
		{
			"extractions that cannot be read fail the withdrawals and the candidates are still decided",
			args{lessons: []knowledge.Knowledge{dup}, appends: -1, lists: 4, reads: -1, seed: "5eed"},
			want{assert.AnError, []string{"k-dup v1 retired nodloop duplicate"}, 1},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := newBench(t, answer(0.9, 0.02), tc.args.lessons, nil)
			if tc.args.unknownShots {
				b.knobs(t, func(*curate.Knobs) {})
				knobs, err := b.stores.Traces.List(ctx, record.TraceFilter{Name: string(trace.NameKnob)})
				require.NoError(t, err)
				require.NoError(t, b.stores.Traces.Append(ctx, unknownShots(t, knobs[0]).Record()))
			}
			traces := &testkit.FlakyTraces{Store: b.stores.Traces, Appends: failing(tc.args.appends), Lists: failing(tc.args.lists)}
			verdicts := &testkit.FlakyFeedback{Store: b.stores.Feedback, Reads: failing(tc.args.reads)}
			setting := b.setting(t)
			setting.Seed = tc.args.seed
			ledger := b.stores.Ledger
			if tc.args.unreadable {
				ledger = flakyLedger(t, b, 0)
			}
			curator := curate.New(ledger, traces, verdicts, b.client, nil, seal, b.stores.Clock.Now)

			_, err := curator.Pass(ctx, setting)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.statuses, statuses(t, b))
			decides, listErr := b.stores.Traces.List(ctx, record.TraceFilter{Name: string(trace.NameDecide)})
			require.NoError(t, listErr)
			assert.Len(t, decides, tc.want.decides)
		})
	}
}

// A candidate corroborate joined new evidence to after its decide is decided again and the judge serves every change of one pass
func TestCuratorPassCorroborated(t *testing.T) {
	ctx := context.Background()
	candidate := knowledge.StatusCandidate
	b := newBench(t, answer(0.9, 0.02), []knowledge.Knowledge{
		lesson("k-add", 1, "Name the failing test first", acme("acme"), candidate, ""),
		lesson("k-other", 1, "Keep commits small", acme("acme"), candidate, ""),
	}, nil)
	first, err := b.curator().Pass(ctx, b.setting(t))
	require.NoError(t, err)
	b.correct(t, "r-more", trace.Labels{"repo": {"acme"}}, []feedback.Verdict{feedback.VerdictReject})
	more, err := b.stores.Traces.Get(ctx, "r-more")
	require.NoError(t, err)
	list, err := b.stores.Feedback.List(ctx, record.FeedbackFilter{TraceID: "r-more"})
	require.NoError(t, err)
	require.NoError(t, testkit.Err(b.stores.Ledger.Corroborate(ctx, "k-add", 1, trace.Restore(more), feedback.RestoreAll(list))))

	second, err := b.curator().Pass(ctx, b.setting(t))

	require.NoError(t, err)
	assert.Equal(t, []string{"add k-add v1 shadow  1", "add k-other v1 shadow  1"}, decisionsOf(first.Decided))
	assert.Equal(t, []string{"add k-add v1 shadow  1"}, decisionsOf(second.Decided))
}

// The gaps fail on verdicts or knowledge that cannot be read
func TestCuratorGapsFails(t *testing.T) {
	type args struct {
		// Reads that pass before every later one fails
		lists, verdicts, knowledge int
	}
	tcs := []struct {
		name string
		args args
	}{
		{"extractions that cannot be read", args{1, -1, 1 << 20}},
		{"verdicts that cannot be read", args{-1, 0, 1 << 20}},
		{"knowledge that cannot be read", args{-1, -1, 0}},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := newBench(t, nil, nil, nil)
			traces := &testkit.FlakyTraces{Store: b.stores.Traces, Lists: failing(tc.args.lists)}
			verdicts := &testkit.FlakyFeedback{Store: b.stores.Feedback, Reads: failing(tc.args.verdicts)}
			curator := curate.New(flakyLedger(t, b, tc.args.knowledge), traces, verdicts, b.client, nil, seal, b.stores.Clock.Now)

			got, err := curator.Gaps(ctx, time.Time{})

			assert.ErrorIs(t, err, assert.AnError)
			assert.Nil(t, got)
		})
	}
}

// A ledger over the records of the bench whose reads fail after the allowed ones
func flakyLedger(t *testing.T, b bench, allowed int) *knowledge.Ledger {
	t.Helper()
	items, err := knowledgefile.New(b.stores.Dir)
	require.NoError(t, err)
	flaky := &testkit.FlakyKnowledge{Store: items, Reads: testkit.Reads{Allowed: allowed, Err: assert.AnError}}
	return knowledge.NewLedger(flaky, vetofile.NewApprovedFile(t.TempDir(), b.stores.Dir), b.stores.Clock.Now, func(prefix string) string { return prefix + "new" })
}

// A store call that fails after the allowed ones and never when allowed is negative
func failing(allowed int) testkit.Reads {
	if allowed < 0 {
		return testkit.Reads{}
	}
	return testkit.Reads{Allowed: allowed, Err: assert.AnError}
}

// The knob trace of the same cycle whose judge names a decision no record holds
func unknownShots(t *testing.T, knob record.Trace) trace.Trace {
	t.Helper()
	ev, ok := curate.RestoreKnob(trace.Restore(knob))
	require.True(t, ok)
	ev.Cause, ev.Judge.Sets = curate.KnobNarrow, [][]knowledge.Ref{{ref("k-gone")}}
	tr, err := ev.Trace("0.10.0", written.Add(time.Minute))
	require.NoError(t, err)
	return tr
}

// A join adds only the runs that still carry a correction and that a store holds and then drops the newer candidate
func TestCuratorPassJoin(t *testing.T) {
	ctx := context.Background()
	older := lesson("k-older", 1, "Name the failing test first", acme("acme"), knowledge.StatusCandidate, "")
	newer := lesson("k-newer", 1, "Name the failing test first", acme("acme"), knowledge.StatusCandidate, "")
	newer.Time = written.Add(time.Hour)
	newer.Evidence.FeedbackTraceIDs = []string{"r-k-newer", "r-undone", "r-unrecorded"}
	b := newBench(t, answer(0.9, 0.02), []knowledge.Knowledge{older, newer}, map[string][]feedback.Verdict{"r-undone": {feedback.VerdictApprove}})

	got, err := b.curator().Pass(ctx, b.setting(t))

	require.NoError(t, err)
	all, err := b.stores.Ledger.All(ctx)
	require.NoError(t, err)
	joined, err := all.Version("k-older", 1)
	require.NoError(t, err)
	assert.Equal(t, []string{"add k-older v1 shadow  1", "join k-newer v1 join same_as_candidate 0"}, decisionsOf(got.Decided))
	assert.Equal(t, []string{"r-k-older", "r-k-newer"}, joined.Evidence.FeedbackTraceIDs)
	assert.Equal(t, []string{"k-newer v1 retired nodloop duplicate", "k-older v1 candidate"}, statuses(t, b))
}

// The judge reads the change as a replacement, the lessons of the place, the files it reads now and what nodloop carried out there
func TestCuratorPassJudgeReads(t *testing.T) {
	ctx := context.Background()
	approved, candidate := knowledge.StatusApproved, knowledge.StatusCandidate
	next := lesson("k-carried", 2, "Squash and rebase before a merge", acme("acme"), candidate, "")
	next.Base, next.Evidence.FeedbackTraceIDs = 1, []string{"r-next"}
	b := newBench(t, nil, []knowledge.Knowledge{
		lesson("k-person2", 1, "Keep commits small", acme("acme"), approved, "ann"),
		lesson("k-person3", 1, "Write the why in the message", acme("acme"), approved, "ann"),
		lesson("k-carried", 1, "Squash before a merge", acme("acme"), approved, knowledge.ApproverNodloop),
		next,
	}, nil)
	var prompts []string
	b.client = llmmock.NewMockClient(gomock.NewController(t))
	b.client.EXPECT().Complete(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, req llm.Request) (llm.Response, error) {
		prompts = append(prompts, req.Prompt)
		return llm.Response{Output: answer(0.9, 0.02), Model: "claude-sonnet-5-5"}, nil
	}).Times(3)
	heard, err := feedback.NewCaptured("r-next", feedback.VerdictReject, "", "rebase it too before you merge", written.Add(time.Hour))
	require.NoError(t, err)
	said, err := feedback.New("r-next", feedback.VerdictReject, "", "the conversation's own summary", nil, feedback.ReviewerSession, written.Add(time.Hour))
	require.NoError(t, err)
	for _, v := range []feedback.Feedback{heard, said} {
		require.NoError(t, b.stores.Feedback.Append(ctx, v.Record()))
	}
	setting := b.setting(t)
	all, err := b.stores.Ledger.All(ctx)
	require.NoError(t, err)
	setting.Anchor = curate.NewAnchor(all, []curate.AnchorFile{{Path: "plan.md", HMAC: hmac}, {Path: "gone.md", HMAC: hmac}},
		[]curate.Loaded{{Session: "s-r-next", File: curate.AnchorFile{Path: "CLAUDE.md", HMAC: hmac}}})
	setting.Read = func(path string) (string, error) {
		texts := map[string]string{"plan.md": "Lint before tests", "CLAUDE.md": "Never push to main"}
		if text, ok := texts[path]; ok {
			return text, nil
		}
		return "", os.ErrNotExist
	}

	got, err := b.curator().Pass(ctx, setting)

	require.NoError(t, err)
	require.Equal(t, []string{"update k-carried v2 ask kind_closed 2"}, decisionsOf(got.Decided))
	require.Len(t, prompts, 3)
	change := "replace [meaning] Squash before a merge\nwith [meaning] Squash and rebase before a merge\nscope: runs of session. repo=acme"
	assert.Contains(t, prompts[0], "- approved [meaning] Squash before a merge\n\n## Change\n"+change+"\n")
	assert.Contains(t, prompts[0], "- file plan.md\nLint before tests\n")
	assert.Contains(t, prompts[0], "- file CLAUDE.md\nNever push to main\n")
	assert.Contains(t, prompts[0], "- [meaning] Keep commits small\n")
	assert.NotContains(t, prompts[0], "gone.md")
	assert.Contains(t, prompts[0], "- rebase it too before you merge\n")
	assert.NotContains(t, prompts[0], "the conversation's own summary")
	assert.Equal(t, seal("approved [meaning] Squash before a merge\n"+change), got.Decided[0].Change.Diff)
	assert.Equal(t, curate.OutcomeConform, got.Decided[0].Decision.Outcome)
}
