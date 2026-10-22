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

	"github.com/jeon-jihyeon/nodloop/internal/classify"
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

// When the records of a bench were written on the Sunday before the testkit clock starts
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

// A bench whose ledger holds the person's lesson and the lessons given with the run of each and a person's reject on it
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
			want{[]string{"add k-other v1 hold scope_wider 2"}, []string{"k-other v1 candidate"}},
		},
		{
			"the next version of a lesson a person approved asks the person",
			args{lessons: []knowledge.Knowledge{next}},
			want{[]string{"update k-person v2 hold person_item 2"}, []string{"k-person v2 candidate"}},
		},
		{
			"a candidate with a veto asks the person",
			args{lessons: []knowledge.Knowledge{vetoed}},
			want{[]string{"add k-veto v1 hold veto 2"}, []string{"k-veto v1 candidate"}},
		},
		{
			"an add the judge finds against asks the person",
			args{lessons: []knowledge.Knowledge{lesson("k-against", 1, "Skip the tests", acme("acme"), candidate, "")}, answer: answer(0.9, 0.8)},
			want{[]string{"add k-against v1 hold judge_against 2"}, []string{"k-against v1 candidate"}},
		},
		{
			"an add the judge finds the anchor silent on asks the person",
			args{lessons: []knowledge.Knowledge{lesson("k-silent", 1, "Use tabs", acme("acme"), candidate, "")}, answer: answer(0.2, 0.1)},
			want{[]string{"add k-silent v1 hold judge_undecided 2"}, []string{"k-silent v1 candidate"}},
		},
		{
			"an add the judge finds conform in a bin not yet open asks the person",
			args{lessons: []knowledge.Knowledge{lesson("k-b2", 1, "Run the linter too", acme("acme"), candidate, "")}, answer: answer(0.9, 0.1)},
			want{[]string{"add k-b2 v1 hold kind_closed 2"}, []string{"k-b2 v1 candidate"}},
		},
		{
			"an approval that needs room asks the person with the compaction",
			args{lessons: []knowledge.Knowledge{
				lesson("k-big1", 1, "first "+big, acme("big"), approved, "ann"), lesson("k-big2", 1, "second "+big, acme("big"), approved, "ann"),
				lesson("k-more", 1, "third "+big, acme("big"), candidate, ""),
			}},
			want{[]string{"add k-more v1 hold room 2"}, []string{"k-big1 v1 approved ann", "k-big2 v1 approved ann", "k-more v1 candidate"}},
		},
		{
			"a place where lessons nodloop approved reach the person's asks to reanchor first",
			args{lessons: []knowledge.Knowledge{
				lesson("k-carried", 1, "Squash before a merge", acme("acme"), approved, knowledge.ApproverNodloop),
				lesson("k-drift", 1, "Rebase before a push", acme("acme"), candidate, ""),
			}},
			want{[]string{"add k-drift v1 hold drift 2"}, []string{"k-carried v1 approved nodloop", "k-drift v1 candidate"}},
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
	judged := slices.IndexFunc(first.Decided, func(d curate.Decided) bool { return d.Change.ID == "k-add" })
	require.GreaterOrEqual(t, judged, 0)
	require.NotNil(t, first.Decided[judged].Decision.Confidence)
	assert.InDelta(t, 0.98, *first.Decided[judged].Decision.Confidence, 1e-9, "the decide keeps the mean confidence its bin reads")
	assert.Equal(t, []string{}, decisionsOf(second.Decided))
	require.Equal(t, []string{"add k-add v1 apply  1"}, decisionsOf(third.Decided))
	assert.Equal(t, []string{"k-add v1 approved nodloop", "k-dup v1 retired nodloop duplicate"}, statuses(t, b))
	assert.Equal(t, third.Decided[0].ID, b.decision(t, "k-add", 1))
	assert.Equal(t, first.Decided[1].ID, b.decision(t, "k-dup", 1))
}

// A decide a crash left before its ledger write is carried out by the next pass under the same decide and only while the version waits
// 1. a join whose older candidate a person decided since still drops the newer one and joins nothing
// 2. each kind is carried out by its own ledger call so a decide whose action is not the one of its kind writes nothing
func TestCuratorPassRecovers(t *testing.T) {
	candidate := knowledge.StatusCandidate
	gone := lesson("k-gone", 1, "Run the tests before a commit", acme("acme"), knowledge.StatusRetired, "ann")
	gone.Reason, gone.Time = knowledge.ReasonPerson, written.Add(time.Hour)
	older := lesson("k-older", 1, "Name the failing test first", acme("acme"), candidate, "")
	kept := lesson("k-older", 1, "Name the failing test first", acme("acme"), knowledge.StatusApproved, "ann")
	kept.Time, kept.ApprovedAt = written.Add(time.Hour), written.Add(time.Hour)
	newer := lesson("k-newer", 1, "Name the failing test first", acme("acme"), candidate, "")
	drop := curate.Decision{
		RuleCode: curate.RuleSameAsItem, Action: curate.ActionDrop, Reason: knowledge.ReasonDuplicate, Failure: curate.FailureHard,
		Reopen: curate.ReopenPersonOnly,
	}
	join := curate.Decision{RuleCode: curate.RuleSameAsCandidate, Action: curate.ActionJoin, Reason: knowledge.ReasonDuplicate}
	type args struct {
		lessons  []knowledge.Knowledge
		change   curate.Change
		decision curate.Decision
	}
	type want struct {
		statuses []string
		decision string
		// The runs the evidence of the older candidate cites
		older []string
		err   error
	}
	dup := []knowledge.Knowledge{lesson("k-dup", 1, "Run the tests before a commit", acme("acme"), candidate, "")}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"a drop with no record that names it is carried out",
			args{dup, curate.Change{Kind: curate.ChangeDrop, ID: "k-dup", Version: 1, Anchor: 1}, drop},
			want{[]string{"k-dup v1 retired nodloop duplicate"}, "d-crash", nil, nil},
		},
		{
			"a drop of a version a person retired since is left as it is",
			args{
				[]knowledge.Knowledge{lesson("k-gone", 1, "Run the tests before a commit", acme("acme"), candidate, ""), gone},
				curate.Change{Kind: curate.ChangeDrop, ID: "k-gone", Version: 1, Anchor: 1}, drop,
			},
			want{[]string{"k-gone v1 retired ann person"}, "", nil, nil},
		},
		{
			"a join whose older candidate a person approved since drops the newer one and joins nothing",
			args{
				[]knowledge.Knowledge{older, kept, newer},
				curate.Change{Kind: curate.ChangeJoin, ID: "k-newer", Version: 1, Old: []knowledge.Ref{ref("k-older")}, Anchor: 1}, join,
			},
			want{[]string{"k-newer v1 retired nodloop duplicate", "k-older v1 approved ann"}, "d-crash", []string{"r-k-older"}, nil},
		},
		{
			"a compaction that would apply a waiting version writes nothing since only its coverage check approves it",
			args{
				dup, curate.Change{Kind: curate.ChangeCompaction, ID: "k-dup", Version: 1, Compaction: "c-1", Anchor: 1},
				curate.Decision{Tier: 1, Action: curate.ActionApply},
			},
			want{[]string{"k-dup v1 candidate"}, "", nil, curate.ErrDecideInvalid},
		},
		{
			"an add that would be dropped writes nothing since a drop is a change of its own",
			args{dup, curate.Change{Kind: curate.ChangeAdd, ID: "k-dup", Version: 1, Anchor: 1}, drop},
			want{[]string{"k-dup v1 candidate"}, "", nil, curate.ErrDecideInvalid},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := newBench(t, nil, tc.args.lessons, nil)
			id := tc.args.change.ID
			event := curate.Event{
				ID: "d-crash", Ref: "r-" + id, Source: trace.SourcePush,
				Draw: trace.NewDraw("2026-09-21", "0.10.0+1a2b3c4d", "5eed", "d-crash", "audit", 0.05),
			}
			tr, err := event.Decide(tc.args.change, tc.args.decision, nil, written.Add(2*time.Hour))
			require.NoError(t, err)
			require.NoError(t, b.stores.Traces.Append(ctx, tr.Record()))

			got, err := b.curator().Pass(ctx, b.setting(t))
			assert.ErrorIs(t, err, tc.want.err)
			again, err := b.curator().Pass(ctx, b.setting(t))
			assert.ErrorIs(t, err, tc.want.err)

			assert.Empty(t, got.Decided)
			assert.Empty(t, again.Decided)
			assert.Equal(t, tc.want.statuses, statuses(t, b))
			assert.Equal(t, tc.want.decision, b.decision(t, id, 1))
			all, err := b.stores.Ledger.All(ctx)
			require.NoError(t, err)
			joined, _ := all.Version("k-older", 1)
			assert.Equal(t, tc.want.older, joined.Evidence.FeedbackTraceIDs)
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
	assert.Equal(t, []string{"add k-add v1 hold judge_split 2"}, decisionsOf(second.Decided))
	assert.Empty(t, third.Decided)
	retried, err := b.stores.Traces.Get(ctx, first.Decided[0].ID)
	require.NoError(t, err)
	assert.Equal(t, "judge_member_failed", retried.Error)
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
		Judge: curate.JudgeConfig{}.Next(sonnetJudge, nil, "", written), Knobs: curate.StartKnobs(),
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
	assert.Equal(t, []string{"withdraw k-carried v1 hold kind_closed 2"}, decisionsOf(got.Decided))
	assert.Equal(t, []string{"k-carried v1 approved nodloop"}, statuses(t, b))
	assert.Empty(t, got.Due)
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

// A capture trace whose person's words asked to drop k-carried and the person's lesson beside one that does not decode
func (b bench) words(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	outputs := map[string]string{
		"c-words": `{"messages":1,"stops":[{"id":"k-carried","version":1,"run":"r-k-carried"},{"id":"k-person","version":1,"run":"r-k-carried"}]}`,
		"c-torn":  `["stops"]`,
	}
	for id, output := range outputs {
		capture := trace.Trace{ID: id, Name: trace.NameCapture, SessionID: "s-r-k-carried", Time: written.Add(time.Hour), Input: json.RawMessage(`{}`),
			Output: json.RawMessage(output)}
		require.NoError(t, b.stores.Traces.Append(ctx, capture.Record()))
	}
}

// The person's words asking to drop a version decide each version they name once
// 1. a version nodloop approved is withdrawn once the shadow ended
// 2. a version a person approved asks the person
func TestCuratorPassWords(t *testing.T) {
	ctx := context.Background()
	b := newBench(t, nil, []knowledge.Knowledge{
		lesson("k-carried", 1, "Squash before a merge", acme("acme"), knowledge.StatusApproved, knowledge.ApproverNodloop),
	}, nil)
	b.words(t)

	first, err := b.curator().Pass(ctx, b.setting(t))
	require.NoError(t, err)
	// No audit draws the withdrawal and the protected version stays held
	b.narrow(t, first.Event, func(k *curate.Knobs) { k.Activated, k.Audit = true, curate.Audit{} })
	second, err := b.curator().Pass(ctx, b.setting(t))
	require.NoError(t, err)
	third, err := b.curator().Pass(ctx, b.setting(t))
	require.NoError(t, err)

	assert.Equal(t, []string{"withdraw k-carried v1 shadow  1", "withdraw k-person v1 hold person_item 2"}, decisionsOf(first.Decided))
	require.Equal(t, []string{"withdraw k-carried v1 withdraw  1"}, decisionsOf(second.Decided))
	assert.Empty(t, third.Decided)
	assert.Equal(t, "c-words", second.Decided[0].Change.Signal)
	assert.Equal(t, "r-k-carried", second.Decided[0].Ref)
	assert.Equal(t, []string{"k-carried v1 retired nodloop words"}, statuses(t, b))
	assert.Empty(t, third.Due)
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
		{curate.QuestionAudit, "k-dup v1", curate.RuleSameAsItem, "r-k-dup", "runs of session. repo=acme"},
		{curate.QuestionShadow, "k-shadow v1", "", "r-k-shadow", "runs of session. repo=acme"},
	}, view(first.Due))
	assert.Equal(t, []due{
		{curate.QuestionAudit, "k-dup v1", curate.RuleSameAsItem, "r-k-dup", "runs of session. repo=acme"},
	}, view(second.Due))
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

func TestCuratorPassRefusesUnreadableEvidenceRun(t *testing.T) {
	t.Parallel()
	b := newBench(t, answer(0.9, 0.02), []knowledge.Knowledge{
		lesson("k-old", 1, "Name the failing test first", acme("acme"), knowledge.StatusCandidate, ""),
	}, nil)
	traces := &testkit.FlakyTraces{Store: b.stores.Traces, Gets: testkit.Reads{Err: assert.AnError}}
	setting := b.setting(t)
	setting.Window = written.Add(time.Hour)
	c := curate.New(b.stores.Ledger, traces, b.stores.Feedback, b.client, nil, seal, b.stores.Clock.Now)

	got, err := c.Pass(context.Background(), setting)

	assert.ErrorIs(t, err, assert.AnError)
	assert.Empty(t, got.Decided)
	assert.Equal(t, []string{"k-old v1 candidate"}, statuses(t, b))
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
	unscoped := lesson("k-unscoped", 1, "Name the failing test first", nil, candidate, "")
	unscoped.Evidence.FeedbackTraceIDs = []string{"r-k-person"}
	type args struct {
		lessons []knowledge.Knowledge
		// The window starts after every run of the bench
		late bool
		// Records of an earlier release appended as they are
		legacy []knowledge.Knowledge
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
			args{[]knowledge.Knowledge{lesson("k-old", 1, "Name the failing test first", acme("acme"), candidate, "")}, true, nil},
			want{[]string{"add k-old v1 shadow  1"}, []string{"r-k-old"}},
		},
		{
			"a run never recorded leaves the change with no place of its own", args{[]knowledge.Knowledge{missing}, false, nil},
			want{[]string{"add k-missing v1 shadow  1"}, []string{"r-unrecorded"}},
		},
		{
			"a candidate of an outcome alone cites the run of the outcome", args{[]knowledge.Knowledge{outcome}, false, nil},
			want{[]string{"add k-outcome v1 shadow  1"}, []string{"r-outcome"}},
		},
		{"a candidate that cites no run waits as before", args{[]knowledge.Knowledge{uncited}, false, nil}, want{[]string{}, []string{}}},
		{"a candidate of an earlier release scoped to no run waits as before", args{nil, false, []knowledge.Knowledge{unscoped}}, want{[]string{}, []string{}}},
		{
			"a version built on one a person retired asks the person", args{[]knowledge.Knowledge{retired, turned}, false, nil},
			want{[]string{"add k-turned v2 hold person_item 2"}, []string{"r-turned"}},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := newBench(t, answer(0.9, 0.02), tc.args.lessons, nil)
			for _, k := range tc.args.legacy {
				require.NoError(t, b.stores.Knowledge.Append(ctx, k.Record()))
			}
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
	carried := lesson("k-carried", 1, "Squash before a merge", acme("acme"), knowledge.StatusApproved, knowledge.ApproverNodloop)
	type args struct {
		lessons []knowledge.Knowledge
		// The start of the cycle names few-shot decisions the records lack
		unknownShots bool
		// Appends, lists of traces and reads of the verdicts that pass before every later one fails and none fails when negative
		appends, lists, reads int
		seed                  string
		// The knowledge cannot be read
		unreadable bool
		// The Monday of a knob event on record under the judge of the pass and the numbers the knobs move on
		monday  time.Time
		numbers curate.Numbers
		// The error the reader of the numbers fails with
		failed error
		// An answer after the knob event on record so a pass inside the cycle reads the numbers
		answered bool
		// An agent proposed a judgment of the first lesson that no decide asks about yet
		proposed bool
		// The stops of a capture of the window
		stops string
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
			"interventions that cannot be read fail the pass", args{lessons: []knowledge.Knowledge{dup}, appends: -1, lists: 3, reads: -1, seed: "5eed"},
			want{assert.AnError, []string{"k-dup v1 candidate"}, 0},
		},
		{
			"runs that cannot be read fail the pass", args{lessons: []knowledge.Knowledge{dup}, appends: -1, lists: 4, reads: -1, seed: "5eed"},
			want{assert.AnError, []string{"k-dup v1 candidate"}, 0},
		},
		{
			"numbers that cannot be read at a cycle start fail the pass before anything is decided",
			args{
				lessons: []knowledge.Knowledge{dup}, appends: -1, lists: -1, reads: -1, seed: "5eed", monday: time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC),
				failed: assert.AnError,
			},
			want{assert.AnError, []string{"k-dup v1 candidate"}, 0},
		},
		{
			"numbers that cannot be read after an answer inside the cycle fail the pass before anything is decided",
			args{
				lessons: []knowledge.Knowledge{dup}, appends: -1, lists: -1, reads: -1, seed: "5eed", monday: time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC),
				failed: assert.AnError, answered: true,
			},
			want{assert.AnError, []string{"k-dup v1 candidate"}, 0},
		},
		{
			"a judge whose trace fails to append fails the change",
			args{lessons: []knowledge.Knowledge{add}, appends: 1, lists: -1, reads: -1, seed: "5eed"}, want{assert.AnError, []string{"k-add v1 candidate"}, 0},
		},
		{
			"a proposal whose decide fails to append joins the error of the pass",
			args{lessons: []knowledge.Knowledge{add}, appends: 1, lists: -1, reads: -1, seed: "5eed", proposed: true},
			want{assert.AnError, []string{"k-add v1 candidate"}, 0},
		},
		{
			"a signal that cites no run fails its withdrawal",
			args{lessons: []knowledge.Knowledge{carried}, appends: -1, lists: -1, reads: -1, seed: "5eed", stops: `[{"id":"k-carried","version":1}]`},
			want{curate.ErrDecideInvalid, []string{"k-carried v1 approved nodloop"}, 0},
		},
		{
			"extractions that cannot be read fail the withdrawals and the candidates are still decided",
			args{lessons: []knowledge.Knowledge{dup}, appends: -1, lists: 5, reads: -1, seed: "5eed"},
			want{assert.AnError, []string{"k-dup v1 retired nodloop duplicate"}, 1},
		},
		{
			"a narrowing that fails to append fails the pass before anything is decided",
			args{
				lessons: []knowledge.Knowledge{dup}, appends: 0, lists: -1, reads: -1, seed: "5eed", monday: time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC),
				numbers: curate.Numbers{Alarmed: []curate.Opening{{Kind: curate.ChangeAdd, Bin: curate.Bin1}}}, answered: true,
			},
			want{assert.AnError, []string{"k-dup v1 candidate"}, 0},
		},
		{
			"an offer whose decide fails to append joins the error of the pass",
			args{
				lessons: []knowledge.Knowledge{dup}, appends: 1, lists: -1, reads: -1, seed: "5eed", monday: time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC),
				numbers: curate.Numbers{ShadowCycles: 2, ShadowMet: true},
			},
			want{assert.AnError, []string{"k-dup v1 candidate"}, 0},
		},
		{
			"captures that cannot be read fail the withdrawals and the candidates are still decided",
			args{lessons: []knowledge.Knowledge{dup}, appends: -1, lists: 6, reads: -1, seed: "5eed"},
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
			if !tc.args.monday.IsZero() {
				b.cycleKnobs(t, tc.args.monday, sonnetJudge, func(*curate.Knobs) {})
			}
			if tc.args.answered {
				b.answered(t, written.Add(time.Hour))
			}
			if tc.args.proposed {
				r := curate.Request{Action: trace.ActionJudge, Target: tc.args.lessons[0].ID + ":1", Choice: trace.ChoiceKeep}
				require.NoError(t, testkit.Err(b.intervener(nil).Intervene(ctx, r, agent)))
			}
			if tc.args.stops != "" {
				b.captured(t, tc.args.stops)
			}
			traces := &testkit.FlakyTraces{Store: b.stores.Traces, Appends: failing(tc.args.appends), Lists: failing(tc.args.lists)}
			verdicts := &testkit.FlakyFeedback{Store: b.stores.Feedback, Reads: failing(tc.args.reads)}
			setting := b.setting(t)
			setting.Seed, setting.Numbers = tc.args.seed, numbersOf(tc.args.numbers, tc.args.failed)
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

// A reader of the numbers of loop that gives the numbers or fails with the error
func numbersOf(n curate.Numbers, err error) func(context.Context) (curate.Numbers, error) {
	return func(context.Context) (curate.Numbers, error) { return n, err }
}

// An answer recorded at the time to a question no record names
func (b bench) answered(t *testing.T, at time.Time) {
	t.Helper()
	tr := trace.Trace{ID: trace.NewID(at), Name: trace.NameAnswer, Ref: "x-answered", Time: at, Input: json.RawMessage(`{"decide":"d-answered"}`)}
	require.NoError(t, b.stores.Traces.Append(context.Background(), tr.Record()))
}

// A capture of the window whose stops are the JSON given
func (b bench) captured(t *testing.T, stops string) {
	t.Helper()
	tr := trace.Trace{
		ID: "c-stops", Name: trace.NameCapture, Time: written.Add(time.Hour), Input: json.RawMessage(`{}`), Output: json.RawMessage(`{"stops":` + stops + `}`),
	}
	require.NoError(t, b.stores.Traces.Append(context.Background(), tr.Record()))
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

// A join that cannot be carried out drops nothing and fails the pass while the older candidate is still decided
func TestCuratorPassJoinFails(t *testing.T) {
	older := lesson("k-older", 1, "Name the failing test first", acme("acme"), knowledge.StatusCandidate, "")
	newer := lesson("k-newer", 1, "Name the failing test first", acme("acme"), knowledge.StatusCandidate, "")
	newer.Time = written.Add(time.Hour)
	type args struct {
		// A join of the newer candidate on record that names no older one
		joinless bool
		// Gets of traces that pass before every later one fails and none fails when negative
		gets int
		// A trace of another name recorded after the run the newer candidate cites under its id
		renamed bool
	}
	type want struct {
		err      error
		decided  []string
		statuses []string
	}
	waiting := []string{"k-newer v1 candidate", "k-older v1 candidate"}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a join on record that names no older candidate", args{true, -1, false}, want{curate.ErrDecideInvalid, []string{"add k-older v1 shadow  1"}, waiting}},
		{
			"a run of the evidence that cannot be read", args{false, 0, false},
			want{assert.AnError, []string{"add k-older v1 shadow  1", "join k-newer v1 join same_as_candidate 0"}, waiting},
		},
		{
			"a cited id whose newest trace is no run", args{false, -1, true},
			want{trace.ErrNotRun, []string{"add k-older v1 shadow  1", "join k-newer v1 join same_as_candidate 0"}, waiting},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := newBench(t, answer(0.9, 0.02), []knowledge.Knowledge{older, newer}, nil)
			if tc.args.joinless {
				joinless := trace.Trace{
					ID: "d-joinless", Name: trace.NameDecide, Ref: "r-k-newer", Time: written.Add(2 * time.Hour),
					Input:  json.RawMessage(`{"kind":"join","id":"k-newer","version":1,"anchor":1}`),
					Output: json.RawMessage(`{"tier":0,"rule_code":"same_as_candidate","action":"join","reason":"duplicate"}`),
				}
				require.NoError(t, b.stores.Traces.Append(ctx, joinless.Record()))
			}
			if tc.args.renamed {
				renamed := trace.Trace{ID: "r-k-newer", Name: trace.NameExtract, Ref: "r-k-newer", Time: written.Add(time.Hour), Input: json.RawMessage(`{}`)}
				require.NoError(t, b.stores.Traces.Append(ctx, renamed.Record()))
			}
			traces := &testkit.FlakyTraces{Store: b.stores.Traces, Gets: failing(tc.args.gets)}
			curator := curate.New(b.stores.Ledger, traces, b.stores.Feedback, b.client, nil, seal, b.stores.Clock.Now)

			got, err := curator.Pass(ctx, b.setting(t))

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.decided, decisionsOf(got.Decided))
			assert.Equal(t, tc.want.statuses, statuses(t, b))
		})
	}
}

// A ledger that cannot be read again after a write keeps the records the pass read last
// So the later changes of the pass are decided on them and the questions due stay due
func TestCuratorPassReloadFails(t *testing.T) {
	ctx := context.Background()
	b := newBench(t, answer(0.9, 0.02), []knowledge.Knowledge{
		lesson("k-dup", 1, "Run the tests before a commit", acme("acme"), knowledge.StatusCandidate, ""),
		lesson("k-add", 1, "Name the failing test first", acme("acme"), knowledge.StatusCandidate, ""),
	}, nil)
	curator := curate.New(flakyLedger(t, b, 1), b.stores.Traces, b.stores.Feedback, b.client, nil, seal, b.stores.Clock.Now)

	got, err := curator.Pass(ctx, b.setting(t))

	assert.ErrorIs(t, err, assert.AnError)
	assert.Equal(t, []string{"add k-add v1 shadow  1", "drop k-dup v1 drop same_as_item 0"}, decisionsOf(got.Decided))
	assert.Equal(t, []string{"k-add v1 candidate", "k-dup v1 retired nodloop duplicate"}, statuses(t, b))
	shadows := slices.DeleteFunc(slices.Clone(got.Due), func(d curate.Due) bool { return d.Kind != curate.QuestionShadow })
	require.Len(t, shadows, 1)
	assert.Equal(t, ref("k-add"), shadows[0].Ref)
}

// The knob event of the cycle that starts on the Monday with the knobs the change gives under a judge of the settings
// The judge starts at written so a pass under the same settings keeps it
func (b bench) cycleKnobs(t *testing.T, monday time.Time, settings curate.JudgeSettings, change func(*curate.Knobs)) string {
	t.Helper()
	ev := curate.KnobEvent{
		CycleID: monday.Format(trace.CycleDate), Start: monday, Seed: "5eed", Cause: curate.KnobStart,
		Judge: curate.JudgeConfig{}.Next(settings, nil, "", written), Knobs: curate.StartKnobs(),
	}
	change(&ev.Knobs)
	tr, err := ev.Trace("0.10.0", written)
	require.NoError(t, err)
	require.NoError(t, b.stores.Traces.Append(context.Background(), tr.Record()))
	return tr.ID
}

// The knob changes as "rule kind bin differ of answers" or "rule p" in their order
func changesOf(cs []curate.KnobChange) []string {
	out := []string{}
	for _, c := range cs {
		if c.Opening == nil {
			out = append(out, fmt.Sprintf("%s p %v", c.Rule, c.P))
			continue
		}
		out = append(out, strings.TrimSpace(fmt.Sprintf("%s %s %s %d of %d", c.Rule, c.Opening.Kind, c.Opening.Bin, c.Differ, c.Answers)))
	}
	return out
}

// The knobs move on the numbers of loop before anything is decided and every offer waits for the person as a question
// 1. an alarm inside the cycle narrows its kind and bin at once in a knob event of cause narrow and raises the audit p
// 2. a pass inside the cycle with no answer since the knob event reads no numbers since an alarm needs an answer
// 3. the first pass of a cycle narrows on the posterior and offers the next bin and the activation as decides of tier 2
// 4. a cycle that starts a new judge configuration reads no number
// 5. an offer the person granted since leaves no question
func TestCuratorPassKnobs(t *testing.T) {
	this, before := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	b1, b2 := curate.Opening{Kind: curate.ChangeAdd, Bin: curate.Bin1}, curate.Opening{Kind: curate.ChangeAdd, Bin: curate.Bin2}
	withdraw := curate.Opening{Kind: curate.ChangeWithdraw}
	offers := curate.Numbers{
		Gauges: []curate.Gauge{{Opening: b1, Answers: 10, Differ: 4, Above: 0.9}, {Opening: b2, Answers: 43, Met: true}}, ShadowCycles: 2, ShadowMet: true,
	}
	other := sonnetJudge
	other.Effort = "max"
	type args struct {
		// The Monday of the knob event on record before the pass and the judge it ran
		monday time.Time
		judge  curate.JudgeSettings
		knobs  func(*curate.Knobs)
		// Offers a pass of an earlier cycle prepared
		offered []curate.Change
		numbers curate.Numbers
		// An answer after the knob event on record
		answered bool
	}
	type want struct {
		// The knob events the pass appended as "cause changes"
		appended []string
		// Whether a narrowing follows the knob event on record
		follows bool
		open    []curate.Opening
		tier1   float64
		decided []string
		due     []string
		// How often the pass read the numbers
		reads int
	}
	alarmed := curate.Numbers{Gauges: []curate.Gauge{{Opening: b1, Answers: 3, Differ: 3}}, Alarmed: []curate.Opening{b1}}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"an alarm inside the cycle narrows its kind and bin at once and raises the audit p",
			args{this, sonnetJudge, func(*curate.Knobs) {}, nil, alarmed, true},
			want{[]string{"narrow [alarm add b1 3 of 3 raise p 0.3]"}, true, []curate.Opening{withdraw}, 0.3, []string{}, []string{}, 1},
		},
		{
			"an alarm on a kind and bin already closed moves nothing and appends no knob event",
			args{this, sonnetJudge, func(*curate.Knobs) {}, nil, curate.Numbers{Alarmed: []curate.Opening{b2}}, true},
			want{[]string{}, false, []curate.Opening{b1, withdraw}, 0.15, []string{}, []string{}, 1},
		},
		{
			"a pass inside the cycle with no answer since the knob event reads no numbers",
			args{this, sonnetJudge, func(*curate.Knobs) {}, nil, alarmed, false},
			want{[]string{}, false, []curate.Opening{b1, withdraw}, 0.15, []string{}, []string{}, 0},
		},
		{
			"a cycle start narrows on the posterior and offers the next bin and the activation as questions",
			args{before, sonnetJudge, func(*curate.Knobs) {}, nil, offers, false},
			want{
				[]string{"start [posterior add b1 4 of 10 raise p 0.3]"}, false, []curate.Opening{withdraw}, 0.3,
				[]string{"activate  v0 ask  2", "widen  v0 ask  2"}, []string{"direction activate", "direction widen add b2"}, 1,
			},
		},
		{
			"a cycle that starts a new judge configuration reads no number",
			args{before, other, func(*curate.Knobs) {}, nil, offers, true},
			want{[]string{"start []"}, false, []curate.Opening{b1, withdraw}, 0.15, []string{}, []string{}, 0},
		},
		{
			"an offer the person granted since leaves no question",
			args{this, sonnetJudge, func(k *curate.Knobs) { k.Open, k.Activated = append(k.Open, b2), true }, []curate.Change{
				{Kind: curate.ChangeWiden, Opens: &b2, Anchor: 1}, {Kind: curate.ChangeActivate, Anchor: 1},
			}, curate.Numbers{}, false},
			want{[]string{}, false, []curate.Opening{b1, withdraw, b2}, 0.15, []string{}, []string{}, 0},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := newBench(t, nil, nil, nil)
			onRecord := b.cycleKnobs(t, tc.args.monday, tc.args.judge, tc.args.knobs)
			for i, c := range tc.args.offered {
				id := fmt.Sprintf("d-offer%d", i)
				ev := curate.Event{ID: id, Source: trace.SourcePush, Draw: trace.NewDraw("2026-09-21", "0.10.0+1a2b3c4d", "5eed", id, "audit", 0)}
				tr, err := ev.Decide(c, curate.Decision{Tier: 2, Action: curate.ActionAsk}, nil, written)
				require.NoError(t, err)
				require.NoError(t, b.stores.Traces.Append(ctx, tr.Record()))
			}
			if tc.args.answered {
				b.answered(t, written.Add(time.Hour))
			}
			setting := b.setting(t)
			reads := 0
			setting.Numbers = func(context.Context) (curate.Numbers, error) {
				reads++
				return tc.args.numbers, nil
			}

			got, err := b.curator().Pass(ctx, setting)

			require.NoError(t, err)
			knobs, err := b.stores.Traces.List(ctx, record.TraceFilter{Name: string(trace.NameKnob)})
			require.NoError(t, err)
			appended := []string{}
			for _, tr := range knobs[:len(knobs)-1] {
				ev, ok := curate.RestoreKnob(trace.Restore(tr))
				require.True(t, ok)
				appended = append(appended, fmt.Sprintf("%s %v", ev.Cause, changesOf(ev.Changes)))
			}
			due := []string{}
			for _, d := range got.Due {
				due = append(due, strings.TrimSpace(fmt.Sprintf("%s %s %s", d.Kind, d.Change, opensOf(got.Decided, d.Decide))))
			}
			slices.Sort(due)
			assert.Equal(t, tc.want, want{
				appended, got.Event.Before == onRecord && got.Event.Cause == curate.KnobNarrow, got.Event.Knobs.Open, got.Event.Knobs.Audit.Tier1,
				decisionsOf(got.Decided), due, reads,
			})
		})
	}
}

// A cycle start reads the model a claude member of the judge answered with on the newest decide of the cycle that ends
// 1. a configuration that recorded none takes it and goes on so the numbers of loop still move the knobs
// 2. another model behind the same name starts a new configuration that records it and reads no number
// 3. a classify trace the store lacks and a member that failed name no model so the decide before them is read
// 4. a failed read of a classify trace fails the pass before its knob event
func TestCuratorPassJudgeModel(t *testing.T) {
	ctx := context.Background()
	// One judged decide of the cycle that ends
	type judged struct {
		member classify.Asked
		// Whether its classify trace is on record
		kept bool
	}
	type args struct {
		// The model the knob event on record names
		recorded string
		// Oldest first
		decides []judged
		gets    testkit.Reads
	}
	type want struct {
		answered string
		// Whether the configuration started at the pass
		started bool
		reads   int
		err     error
	}
	reads := testkit.Reads{Allowed: 100}
	sonnet := judged{classify.Asked{Name: "claude:1", Model: "claude-sonnet-5-5"}, true}
	moved := judged{classify.Asked{Name: "claude:1", Model: "claude-sonnet-5-4"}, true}
	failed := judged{classify.Asked{Name: "claude:1", Model: "sonnet", Error: "classify: member failed"}, true}
	gone := judged{sonnet.member, false}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a configuration that recorded no model takes the one answered", args{"", []judged{sonnet}, reads},
			want{"claude-sonnet-5-5", false, 1, nil}},
		{"the model recorded answered again keeps the configuration", args{"claude-sonnet-5-5", []judged{sonnet},
			reads}, want{"claude-sonnet-5-5", false, 1, nil}},
		{"another model starts a new configuration that records it", args{"claude-sonnet-5-5", []judged{moved},
			reads}, want{"claude-sonnet-5-4", true, 0, nil}},
		{"a newer answer of the model recorded wins over an older move", args{"claude-sonnet-5-5", []judged{moved,
			sonnet}, reads}, want{"claude-sonnet-5-5", false, 1, nil}},
		{"a member that failed lets the decide before it name the model", args{"claude-sonnet-5-5",
			[]judged{moved, failed}, reads}, want{"claude-sonnet-5-4", true, 0, nil}},
		{"a classify trace the store lacks lets the decide before it name the model", args{"claude-sonnet-5-5",
			[]judged{moved, gone}, reads},
			want{"claude-sonnet-5-4", true, 0, nil}},
		{"a failed read of a classify trace fails the pass", args{"claude-sonnet-5-5", []judged{sonnet},
			testkit.Reads{Err: assert.AnError}},
			want{"", false, 0, assert.AnError}},
		{"no decide that names a model keeps the one recorded", args{"claude-sonnet-5-5", []judged{failed, gone},
			reads}, want{"claude-sonnet-5-5", false, 1, nil}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := newBench(t, nil, nil, nil)
			monday := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
			ev := curate.KnobEvent{
				CycleID: monday.Format(trace.CycleDate), Start: monday, Seed: "5eed", Cause: curate.KnobStart,
				Judge: curate.JudgeConfig{}.Next(sonnetJudge, nil, "", written), Knobs: curate.StartKnobs(),
			}
			ev.Judge.Answered = tc.args.recorded
			knob, err := ev.Trace("0.10.0", written)
			require.NoError(t, err)
			require.NoError(t, b.stores.Traces.Append(ctx, knob.Record()))
			for i, j := range tc.args.decides {
				at, ballotID, decideID := written.Add(time.Duration(i+1)*time.Hour), fmt.Sprintf("c-judge%d",
					i), fmt.Sprintf("d-judged%d", i)
				if j.kept {
					out, err := json.Marshal(classify.TraceOutput{Members: []classify.Asked{j.member}})
					require.NoError(t, err)
					ballot := trace.Trace{ID: ballotID, Name: trace.NameClassify, Subject: string(classify.PointJudge),
						Ref: "r-x", Time: at, Output: out}
					require.NoError(t, b.stores.Traces.Append(ctx, ballot.Record()))
				}
				draw := trace.NewDraw("2026-09-07", "0.10.0+1a2b3c4d", "5eed", decideID, "audit", 0)
				decide, err := curate.Event{ID: decideID, Ref: "r-x", Source: trace.SourcePush, Draw: draw}.Decide(
					curate.Change{Kind: curate.ChangeAdd, ID: "k-x", Version: 1, Anchor: 1},
					curate.Decision{Tier: 2, Action: curate.ActionAsk, Judge: ballotID, Outcome: curate.OutcomeSplit}, nil, at)
				require.NoError(t, err)
				require.NoError(t, b.stores.Traces.Append(ctx, decide.Record()))
			}
			setting := b.setting(t)
			numbers := 0
			setting.Numbers = func(context.Context) (curate.Numbers, error) {
				numbers++
				return curate.Numbers{}, nil
			}
			traces := &testkit.FlakyTraces{Store: b.stores.Traces, Gets: tc.args.gets}
			curator := curate.New(b.stores.Ledger, traces, b.stores.Feedback, b.client, nil, seal, b.stores.Clock.Now)

			got, err := curator.Pass(ctx, setting)

			assert.Equal(t, tc.want, want{got.Event.Judge.Answered,
				!got.Event.Judge.Start.IsZero() && !got.Event.Judge.Start.Equal(written), numbers, err})
		})
	}
}

// A cycle start whose open kinds and bins all met their bound lowers the tier 1 audit p no further than the person set it
func TestCuratorPassLowersToPersonsSetting(t *testing.T) {
	ctx := context.Background()
	type want struct {
		tier1   float64
		changes []string
	}
	tcs := []struct {
		name string
		// The tier 1 audit p the person set before the cycle start and none when 0
		args float64
		want want
	}{
		{"the p the person set stays", 0.3, want{0.3, []string{}}},
		{"no setting of the person lowers to the floor", 0, want{0.05, []string{"lower p 0.05"}}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := newBench(t, nil, nil, nil)
			b.cycleKnobs(t, time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC), sonnetJudge, func(*curate.Knobs) {})
			if tc.args > 0 {
				r := curate.Request{Action: trace.ActionKnob, Target: string(curate.KnobAuditTier1), Value: &tc.args}
				require.NoError(t, testkit.Err(b.intervener(nil).Intervene(ctx, r, person)))
			}
			b1, withdraw := curate.Opening{Kind: curate.ChangeAdd, Bin: curate.Bin1}, curate.Opening{Kind: curate.ChangeWithdraw}
			setting := b.setting(t)
			setting.Numbers = numbersOf(curate.Numbers{Gauges: []curate.Gauge{{Opening: b1, Met: true}, {Opening: withdraw, Met: true}}}, nil)

			got, err := b.curator().Pass(ctx, setting)

			require.NoError(t, err)
			assert.Equal(t, tc.want, want{got.Event.Knobs.Audit.Tier1, changesOf(got.Event.Changes)})
		})
	}
}

// The kind and bin the decide of the id opens as "kind bin" and empty for any other decide
func opensOf(ds []curate.Decided, id string) string {
	i := slices.IndexFunc(ds, func(d curate.Decided) bool { return d.ID == id && d.Change.Opens != nil })
	if i < 0 {
		return ""
	}
	return fmt.Sprintf("%s %s", ds[i].Change.Opens.Kind, ds[i].Change.Opens.Bin)
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
	require.Equal(t, []string{"update k-carried v2 hold kind_closed 2"}, decisionsOf(got.Decided))
	require.Len(t, prompts, 3)
	change := "replace [meaning] Squash before a merge\nwith [meaning] Squash and rebase before a merge\nscope: runs of session. repo=acme"
	quoted, err := json.Marshal(change)
	require.NoError(t, err)
	assert.Contains(t, prompts[0], "\"approved [meaning] Squash before a merge\"\n\n## Change\n"+string(quoted)+"\n")
	assert.Contains(t, prompts[0], `"file plan.md\nLint before tests"`+"\n")
	assert.Contains(t, prompts[0], `"file CLAUDE.md\nNever push to main"`+"\n")
	assert.Contains(t, prompts[0], "\"[meaning] Keep commits small\"\n")
	assert.NotContains(t, prompts[0], "gone.md")
	assert.Contains(t, prompts[0], "\"rebase it too before you merge\"\n")
	assert.NotContains(t, prompts[0], "the conversation's own summary")
	assert.Equal(t, seal("approved [meaning] Squash before a merge\n"+change), got.Decided[0].Change.Diff)
	assert.Equal(t, curate.OutcomeConform, got.Decided[0].Decision.Outcome)
}

// A version of a few-shot set that the person retired after the configuration started keeps its example and the judge still runs
func TestCuratorPassShotsAsOfStart(t *testing.T) {
	ctx := context.Background()
	b := newBench(t, nil, []knowledge.Knowledge{
		lesson("k-shot", 1, "Keep commits small", acme("acme"), knowledge.StatusApproved, "ann"),
		lesson("k-first", 1, "Name the failing test first", acme("acme"), knowledge.StatusCandidate, ""),
	}, nil)
	var prompts []string
	b.client = llmmock.NewMockClient(gomock.NewController(t))
	b.client.EXPECT().Complete(gomock.Any(), gomock.Any()).AnyTimes().DoAndReturn(func(_ context.Context, req llm.Request) (llm.Response, error) {
		prompts = append(prompts, req.Prompt)
		return llm.Response{Output: answer(0.9, 0.02), Model: "claude-sonnet-5-5"}, nil
	})
	first, err := b.curator().Pass(ctx, b.setting(t))
	require.NoError(t, err)
	require.NoError(t, testkit.Err(b.stores.Ledger.Retire(ctx, "k-shot", 1, "ann")))
	b.correct(t, "r-k-second", trace.Labels{"repo": {"acme"}}, []feedback.Verdict{feedback.VerdictReject})
	second := lesson("k-second", 1, "Pin the base image", acme("acme"), knowledge.StatusCandidate, "")
	require.NoError(t, testkit.Err(b.stores.Ledger.Import(ctx, []knowledge.Knowledge{second})))
	prompts = nil

	again, err := b.curator().Pass(ctx, b.setting(t))

	require.NoError(t, err)
	assert.Equal(t, []string{"add k-first v1 shadow  1"}, decisionsOf(first.Decided))
	assert.Equal(t, []string{"add k-second v1 shadow  1"}, decisionsOf(again.Decided))
	require.Len(t, prompts, 3)
	assert.Contains(t, prompts[0], "\"approved as drafted: Keep commits small\"\n")
	assert.NotContains(t, prompts[0], "retired: Keep commits small")
}

// A trace reads back as a decide or a knob event only when it is one that decodes
func TestRestoreDecideAndKnob(t *testing.T) {
	decide := trace.Trace{
		ID: "d1", Name: trace.NameDecide, Ref: "r-1", Time: written,
		Input: json.RawMessage(`{"kind":"drop","id":"k-1","version":1,"anchor":1}`), Output: json.RawMessage(`{"tier":0,"action":"drop"}`),
		Draw: &trace.Draw{Drawn: true},
	}
	knob := trace.Trace{ID: "n1", Name: trace.NameKnob, Time: written, Input: json.RawMessage(`{"cycle_id":"2026-09-21","seed":"5eed"}`)}
	type want struct {
		decide, knob bool
	}
	tcs := []struct {
		name string
		args trace.Trace
		want want
	}{
		{"a decide", decide, want{true, false}},
		{"a knob event", knob, want{false, true}},
		{"a decide whose output does not decode", trace.Trace{Name: trace.NameDecide, Input: decide.Input, Output: json.RawMessage(`[`)}, want{false, false}},
		{"a knob event with no cycle", trace.Trace{Name: trace.NameKnob, Input: json.RawMessage(`{}`)}, want{false, false}},
		{"a trace of another name", trace.Trace{Name: trace.NameRun, Input: decide.Input, Output: decide.Output}, want{false, false}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, isDecide := curate.RestoreDecide(tc.args)
			_, isKnob := curate.RestoreKnob(tc.args)
			assert.Equal(t, tc.want, want{isDecide, isKnob})
		})
	}
	got, _ := curate.RestoreDecide(decide)
	assert.Equal(t, curate.Decided{
		ID: "d1", Time: written, Ref: "r-1", Change: curate.Change{Kind: curate.ChangeDrop, ID: "k-1", Version: 1, Anchor: 1},
		Decision: curate.Decision{Action: curate.ActionDrop}, Drawn: true,
	}, got)
}
