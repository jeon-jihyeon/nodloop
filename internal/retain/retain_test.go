package retain_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	feedbackfile "github.com/jeon-jihyeon/nodloop/internal/feedback/file"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	knowledgefile "github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/retain"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
	vetofile "github.com/jeon-jihyeon/nodloop/internal/veto/file"
)

var now = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

var errStore = errors.New("store failed")

func clock() time.Time { return now }

// The count of a cycle as the rollup input names it
func count(_ context.Context, c trace.Cycle) (json.RawMessage, error) {
	return json.Marshal(map[string]any{"cycle_id": c.ID, "start": c.Start, "end": c.End})
}

// A trace of the name recorded days before now with the ref and the input
func add(t *testing.T, st testkit.Stores, name trace.Name, days int, ref, input string) string {
	t.Helper()
	at := now.Add(-time.Duration(days) * 24 * time.Hour)
	tr := trace.Trace{ID: trace.NewID(at), Name: name, Producer: "bot", Time: at, Ref: ref, Input: []byte(input)}
	require.NoError(t, st.Traces.Append(context.Background(), tr.Record()))
	return tr.ID
}

func judge(t *testing.T, st testkit.Stores, id string, verdict feedback.Verdict, reviewer string) {
	t.Helper()
	fb, err := feedback.New(id, verdict, "", "", nil, reviewer, now)
	require.NoError(t, err)
	require.NoError(t, st.Feedback.Append(context.Background(), fb.Record()))
}

// A candidate of the id that cites the evidence
func propose(t *testing.T, st testkit.Stores, id string, evidence knowledge.Evidence) {
	t.Helper()
	_, _, err := st.Ledger.Propose(context.Background(), knowledge.Knowledge{
		ID: id, Kind: knowledge.KindJudgment, Content: "content of " + id, Author: "author",
		Run: &knowledge.RunScope{Producer: "bot"}, Evidence: evidence,
	})
	require.NoError(t, err)
}

// Records with one trace of every kind a prune judges by its key
func fixture(t *testing.T) (testkit.Stores, map[string]string) {
	t.Helper()
	ctx := context.Background()
	st := testkit.Open(t)
	none := `{"applied":[]}`
	usesA := `{"applied":[{"id":"a","version":1}]}`
	ids := map[string]string{
		"first receipt": add(t, st, trace.NameRun, 50, "", `{"applied":[],"withheld":[{"id":"a","version":1}],"drawn":true,"share":0.5,"scheme":1}`),
		"unjudged":      add(t, st, trace.NameRun, 40, "", none),
		"judged":        add(t, st, trace.NameRun, 40, "", none),
		"inferred":      add(t, st, trace.NameRun, 40, "", none),
		"withdrawn":     add(t, st, trace.NameRun, 40, "", none),
		"checked":       add(t, st, trace.NameRun, 40, "", none),
		"taught":        add(t, st, trace.NameRun, 40, "", none),
		"confirmed":     add(t, st, trace.NameRun, 40, "", none),
		"earlier use":   add(t, st, trace.NameRun, 45, "", usesA),
		"last use":      add(t, st, trace.NameRun, 40, "", usesA),
		"young":         add(t, st, trace.NameRun, 3, "", none),
		"check":         add(t, st, trace.NameCheck, 40, "", none),
		"replay":        add(t, st, trace.NameReplay, 40, "", none),
		"consolidate":   add(t, st, trace.NameConsolidate, 40, "", none),
		"compact":       add(t, st, trace.NameCompact, 40, "", none),
		"old turn":      add(t, st, trace.NameTurn, 40, "", "{}"),
		"young turn":    add(t, st, trace.NameTurn, 3, "", "{}"),
		"old calls":     add(t, st, trace.NameCalls, 40, "", "{}"),
		"young calls":   add(t, st, trace.NameCalls, 3, "", "{}"),
		"old rules":     add(t, st, trace.NameInstructions, 40, "", "{}"),
		"young rules":   add(t, st, trace.NameInstructions, 3, "", "{}"),
		"old capture":   add(t, st, trace.NameCapture, 40, "", "{}"),
		"young capture": add(t, st, trace.NameCapture, 3, "", "{}"),
		"decide":        add(t, st, trace.NameDecide, 40, "", "{}"),
		"anchor":        add(t, st, trace.NameAnchor, 40, "", "{}"),
		"expose":        add(t, st, trace.NameExpose, 40, "", "{}"),
		"answer":        add(t, st, trace.NameAnswer, 40, "", "{}"),
		"intervention":  add(t, st, trace.NameIntervention, 40, "", "{}"),
		"rollup":        add(t, st, trace.NameRollup, 40, "", "{}"),
	}
	failed := trace.Trace{
		ID: trace.NewID(now.Add(-35 * 24 * time.Hour)), Name: trace.NameRun, Producer: "bot", Time: now.Add(-35 * 24 * time.Hour),
		Input: []byte(usesA), Error: "timeout",
	}
	require.NoError(t, st.Traces.Append(ctx, failed.Record()))
	ids["failed use"] = failed.ID
	ids["use appended late"] = add(t, st, trace.NameRun, 44, "", usesA)
	ids["classify of a removed run"] = add(t, st, trace.NameClassify, 40, ids["unjudged"], none)
	ids["young classify of a removed run"] = add(t, st, trace.NameClassify, 1, ids["unjudged"], none)
	ids["classify of a kept run"] = add(t, st, trace.NameClassify, 40, ids["judged"], none)
	ids["extract"] = add(t, st, trace.NameExtract, 40, ids["unjudged"], none)
	judge(t, st, ids["judged"], feedback.VerdictApprove, "ann")
	judge(t, st, ids["inferred"], feedback.VerdictReject, feedback.ReviewerSession)
	judge(t, st, ids["withdrawn"], feedback.VerdictWithdraw, "ann")
	o, err := feedback.NewOutcome(ids["checked"], feedback.ResultRefuted, "", "", "ann", "", now)
	require.NoError(t, err)
	require.NoError(t, st.Outcomes.Append(ctx, o.Record()))
	propose(t, st, "a", knowledge.Evidence{FeedbackTraceIDs: []string{ids["taught"]}})
	require.NoError(t, testkit.Err(st.Ledger.Approve(ctx, "a", 1, "ann")))
	propose(t, st, "b", knowledge.Evidence{OutcomeTraceIDs: []string{ids["confirmed"]}})
	return st, ids
}

func TestRetainerPrune(t *testing.T) {
	st, ids := fixture(t)
	ctx := context.Background()
	retainer := retain.New(st.Traces, st.Feedback, st.Outcomes, st.Ledger, count, clock)
	plan, err := retainer.Plan(ctx, retain.Days(30).Cutoff(now))
	require.NoError(t, err)
	removed, err := retainer.Apply(ctx, plan)
	require.NoError(t, err)
	left, err := st.Traces.List(ctx, record.TraceFilter{})
	require.NoError(t, err)
	kept := map[string]bool{}
	for _, tr := range left {
		kept[tr.ID] = true
	}
	tcs := []struct {
		name string
		args string
		want bool
	}{
		{"an old run nothing cites goes", "unjudged", false},
		{"a run a person judged stays", "judged", true},
		{"a run only a session judged stays", "inferred", true},
		{"a withdrawn run stays", "withdrawn", true},
		{"a run with an outcome stays", "checked", true},
		{"a run knowledge cites as feedback stays", "taught", true},
		{"a run a candidate cites as an outcome stays", "confirmed", true},
		{"the newest run that applied an approved version stays", "last use", true},
		{"the first run that received an approved version stays even when withheld", "first receipt", true},
		{"an older run that applied the same version goes", "earlier use", false},
		{"a run appended last that is not the newest use goes", "use appended late", false},
		{"a failed run that applied it never counts as its last use", "failed use", false},
		{"a young run stays", "young", true},
		{"a classify trace of a removed run goes", "classify of a removed run", false},
		{"a young classify trace of a removed run goes with it", "young classify of a removed run", false},
		{"a classify trace of a kept run stays", "classify of a kept run", true},
		{"an extract trace stays", "extract", true},
		{"a check trace stays", "check", true},
		{"a replay trace stays", "replay", true},
		{"a consolidate trace stays", "consolidate", true},
		{"a compact trace stays", "compact", true},
		{"an old turn trace goes since its run holds the duration", "old turn", false},
		{"a young turn trace stays for the stop of its turn", "young turn", true},
		{"an old calls trace no run followed goes", "old calls", false},
		{"a young calls trace stays for an extraction", "young calls", true},
		{"an old instructions trace goes since an anchor version holds the files it read", "old rules", false},
		{"a young instructions trace stays", "young rules", true},
		{"an old capture trace goes since only the newest capture holds an offset still read", "old capture", false},
		{"a young capture trace stays", "young capture", true},
		{"a decide trace stays", "decide", true},
		{"an anchor trace stays", "anchor", true},
		{"an expose trace stays", "expose", true},
		{"an answer trace stays", "answer", true},
		{"an intervention trace stays", "intervention", true},
		{"a rollup trace stays", "rollup", true},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, kept[ids[tc.args]])
		})
	}
	assert.ElementsMatch(t, []string{ids["unjudged"], ids["earlier use"], ids["failed use"], ids["use appended late"]}, plan.Runs)
	assert.ElementsMatch(t, []string{ids["classify of a removed run"], ids["young classify of a removed run"]}, plan.Classify)
	assert.ElementsMatch(t, []string{ids["old turn"], ids["old rules"], ids["old capture"]}, plan.Session)
	assert.Equal(t, []string{ids["old calls"]}, plan.Calls, "a calls trace no run followed goes alone")
	assert.Equal(t, 8, plan.Kept)
	assert.Equal(t, 10, removed)
	assert.Equal(t, retain.Days(30).Cutoff(now), plan.Cutoff, "no knob trace leaves the cutoff as asked")
	assert.Empty(t, plan.Rollups)
}

// A verdict recorded between the plan and its removal keeps the run and its classify trace
func TestRetainerApplyReadsTheCitationsAgain(t *testing.T) {
	st := testkit.Open(t)
	ctx := context.Background()
	run := add(t, st, trace.NameRun, 40, "", `{"applied":[]}`)
	classify := add(t, st, trace.NameClassify, 40, run, `{}`)
	retainer := retain.New(st.Traces, st.Feedback, st.Outcomes, st.Ledger, count, clock)
	plan, err := retainer.Plan(ctx, retain.Days(30).Cutoff(now))
	require.NoError(t, err)
	require.Equal(t, []string{run}, plan.Runs)
	judge(t, st, run, feedback.VerdictApprove, "ann")

	removed, err := retainer.Apply(ctx, plan)

	require.NoError(t, err)
	assert.Zero(t, removed)
	left, err := st.Traces.List(ctx, record.TraceFilter{})
	require.NoError(t, err)
	assert.Equal(t, []string{classify, run}, []string{left[0].ID, left[1].ID})
}

// Records a verdict as the prune reaches the store and before the store takes its lock
type judgingTraces struct {
	*tracefile.Store
	judge func()
}

func (j judgingTraces) Prune(ctx context.Context, decide func(context.Context) ([]string, error)) (int, error) {
	j.judge()
	return j.Store.Prune(ctx, decide)
}

// A verdict recorded after Apply starts and before the store prunes keeps the run
// The store asks for the ids when its prune runs so the citations are read after the verdict
func TestRetainerApplyReadsTheCitationsWhenTheStorePrunes(t *testing.T) {
	st := testkit.Open(t)
	ctx := context.Background()
	run := add(t, st, trace.NameRun, 40, "", `{"applied":[]}`)
	traces := judgingTraces{Store: st.Traces, judge: func() { judge(t, st, run, feedback.VerdictReject, "ann") }}
	retainer := retain.New(traces, st.Feedback, st.Outcomes, st.Ledger, count, clock)
	plan, err := retainer.Plan(ctx, retain.Days(30).Cutoff(now))
	require.NoError(t, err)
	require.Equal(t, []string{run}, plan.Runs)

	removed, err := retainer.Apply(ctx, plan)

	require.NoError(t, err)
	assert.Zero(t, removed)
	_, err = st.Traces.Get(ctx, run)
	assert.NoError(t, err, "the judged run stays")
}

// Any read or removal that fails fails the prune and removes nothing
func TestRetainerPruneFailures(t *testing.T) {
	type args struct {
		// Calls of each store that pass before the next one fails
		feedbackReads, knowledgeReads, traceCalls int
		days                                      retain.Days
	}
	type want struct {
		removed int
		err     error
		left    int
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a prune removes the old run", args{9, 9, 9, 30}, want{1, nil, 1}},
		{"no cutoff reads nothing and removes nothing", args{0, 0, 0, 0}, want{0, nil, 2}},
		{"a failed read of the verdicts fails the plan", args{0, 9, 9, 30}, want{0, errStore, 2}},
		{"a failed read of the knowledge fails the plan", args{9, 0, 9, 30}, want{0, errStore, 2}},
		{"a failed read of the traces fails the plan", args{9, 9, 0, 30}, want{0, errStore, 2}},
		{"a failed second read of the citations fails the removal", args{1, 9, 9, 30}, want{0, errStore, 2}},
		{"a failed second read of the knowledge fails the removal", args{9, 1, 9, 30}, want{0, errStore, 2}},
		{"a failed removal fails the prune", args{9, 9, 1, 30}, want{0, errStore, 2}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := testkit.Open(t)
			ctx := context.Background()
			add(t, st, trace.NameRun, 40, "", `{"applied":[]}`)
			add(t, st, trace.NameRun, 3, "", `{"applied":[]}`)
			verdicts := &testkit.FlakyFeedback{Store: st.Feedback, Reads: testkit.Reads{Allowed: tc.args.feedbackReads, Err: errStore}}
			items, err := knowledgefile.New(st.Dir)
			require.NoError(t, err)
			flaky := &testkit.FlakyKnowledge{Store: items, Reads: testkit.Reads{Allowed: tc.args.knowledgeReads, Err: errStore}}
			ledger := knowledge.NewLedger(flaky, vetofile.NewApprovedFile(t.TempDir(), st.Dir), st.Clock.Now, knowledge.NewIDs(st.Clock.Now))
			traces := &testkit.FlakyTraces{Store: st.Traces, Reads: testkit.Reads{Allowed: tc.args.traceCalls, Err: errStore}}

			removed, err := retain.New(traces, verdicts, st.Outcomes, ledger, count, clock).Prune(ctx, tc.args.days.Cutoff(now))

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.removed, removed)
			left, err := st.Traces.List(ctx, record.TraceFilter{})
			require.NoError(t, err)
			assert.Len(t, left, tc.want.left)
		})
	}
}

// A knob trace of the cycle that starts on the date and lasts the weeks
func knob(t *testing.T, st testkit.Stores, cycle string, weeks int) {
	t.Helper()
	start, err := time.Parse(trace.CycleDate, cycle)
	require.NoError(t, err)
	input := fmt.Sprintf(`{"cycle_id":%q,"start":%q,"knobs":{"cycle_weeks":%d}}`, cycle, start.Format(time.RFC3339), weeks)
	tr := trace.Trace{ID: trace.NewID(start), Name: trace.NameKnob, Time: start, Input: json.RawMessage(input)}
	require.NoError(t, st.Traces.Append(context.Background(), tr.Record()))
}

// The cycle id of every rollup trace in the store newest first
func rollups(t *testing.T, st testkit.Stores) []string {
	t.Helper()
	found, err := st.Traces.List(context.Background(), record.TraceFilter{Name: string(trace.NameRollup)})
	require.NoError(t, err)
	var out []string
	for _, tr := range found {
		var r struct {
			CycleID string `json:"cycle_id"`
		}
		require.NoError(t, json.Unmarshal(tr.Input, &r))
		out = append(out, r.CycleID)
	}
	return out
}

// The prune keeps to the start of the open cycle and rolls up each closed cycle it reaches before any of its runs goes
// Now is the Monday 2026-10-12 at noon
func TestRetainerPruneCycles(t *testing.T) {
	type knobbed struct {
		cycle string
		weeks int
	}
	type args struct {
		knobs []knobbed
		// The cycle of a rollup trace already recorded and none when empty
		rolled string
		days   retain.Days
		// Days before now of an unjudged run each
		runs []int
	}
	type want struct {
		cutoff  time.Time
		rollups []string
		// Days before now of the runs left
		left []int
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"no knob trace prunes by the retention alone",
			args{days: 30, runs: []int{40, 3}},
			want{now.Add(-30 * 24 * time.Hour), nil, []int{3}},
		},
		{
			"a cycle closed by its length is rolled up first and its old runs go",
			args{knobs: []knobbed{{"2026-08-31", 2}}, days: 30, runs: []int{40, 25}},
			want{now.Add(-30 * 24 * time.Hour), []string{"2026-08-31"}, []int{25}},
		},
		{
			"every closed cycle the cutoff reaches is rolled up in time order",
			args{knobs: []knobbed{{"2026-08-31", 2}}, days: 7, runs: []int{40, 25, 10, 2}},
			want{now.Add(-7 * 24 * time.Hour), []string{"2026-09-28", "2026-09-14", "2026-08-31"}, []int{2}},
		},
		{
			"the cutoff moves back to the start of the open cycle so its runs stay",
			args{knobs: []knobbed{{"2026-09-28", 4}}, days: 7, runs: []int{20, 11, 2}},
			want{time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC), nil, []int{11, 2}},
		},
		{
			"a cycle with a rollup gets no second one",
			args{knobs: []knobbed{{"2026-08-31", 2}}, rolled: "2026-08-31", days: 30, runs: []int{40}},
			want{now.Add(-30 * 24 * time.Hour), []string{"2026-08-31"}, nil},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := testkit.Open(t)
			ctx := context.Background()
			for _, k := range tc.args.knobs {
				knob(t, st, k.cycle, k.weeks)
			}
			if tc.args.rolled != "" {
				add(t, st, trace.NameRollup, 30, "", fmt.Sprintf(`{"cycle_id":%q}`, tc.args.rolled))
			}
			days := map[string]int{}
			for _, d := range tc.args.runs {
				days[add(t, st, trace.NameRun, d, "", `{"applied":[]}`)] = d
			}
			retainer := retain.New(st.Traces, st.Feedback, st.Outcomes, st.Ledger, count, clock)

			plan, err := retainer.Plan(ctx, tc.args.days.Cutoff(now))
			require.NoError(t, err)
			_, err = retainer.Apply(ctx, plan)
			require.NoError(t, err)

			found, err := st.Traces.List(ctx, record.TraceFilter{Name: string(trace.NameRun)})
			require.NoError(t, err)
			var left []int
			for _, tr := range found {
				left = append([]int{days[tr.ID]}, left...)
			}
			assert.Equal(t, tc.want.cutoff, plan.Cutoff)
			assert.Equal(t, tc.want.rollups, rollups(t, st))
			assert.Equal(t, tc.want.left, left)
		})
	}
}

// A rollup another prune appended between the plan and its removal is the one of its cycle
func TestRetainerApplyReadsTheRollupsAgain(t *testing.T) {
	st := testkit.Open(t)
	ctx := context.Background()
	knob(t, st, "2026-08-31", 2)
	run := add(t, st, trace.NameRun, 40, "", `{"applied":[]}`)
	retainer := retain.New(st.Traces, st.Feedback, st.Outcomes, st.Ledger, count, clock)
	plan, err := retainer.Plan(ctx, retain.Days(30).Cutoff(now))
	require.NoError(t, err)
	require.Len(t, plan.Rollups, 1)
	add(t, st, trace.NameRollup, 0, "", `{"cycle_id":"2026-08-31"}`)

	removed, err := retainer.Apply(ctx, plan)

	require.NoError(t, err)
	assert.Equal(t, 1, removed)
	assert.Equal(t, []string{"2026-08-31"}, rollups(t, st))
	_, err = st.Traces.Get(ctx, run)
	assert.ErrorIs(t, err, record.ErrTraceNotFound)
}

// A count, an append or a read of the rollups that fails fails the prune before any run goes
func TestRetainerRollupFailures(t *testing.T) {
	failing := func(context.Context, trace.Cycle) (json.RawMessage, error) { return nil, errStore }
	type args struct {
		count retain.Count
		// Heads and appends of the trace store that pass before the next fails
		heads, appends int
	}
	tcs := []struct {
		name string
		args args
	}{
		{"a count that fails", args{failing, 9, 9}},
		{"an append that fails", args{count, 9, 0}},
		{"a read of the rollups that fails", args{count, 1, 9}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := testkit.Open(t)
			ctx := context.Background()
			knob(t, st, "2026-08-31", 2)
			add(t, st, trace.NameRun, 40, "", `{"applied":[]}`)
			traces := &testkit.FlakyTraces{
				Store: st.Traces, Reads: testkit.Reads{Allowed: tc.args.heads, Err: errStore}, Appends: testkit.Reads{Allowed: tc.args.appends, Err: errStore},
			}

			removed, err := retain.New(traces, st.Feedback, st.Outcomes, st.Ledger, tc.args.count, clock).Prune(ctx, retain.Days(30).Cutoff(now))

			assert.ErrorIs(t, err, errStore)
			assert.Zero(t, removed)
			left, err := st.Traces.List(ctx, record.TraceFilter{Name: string(trace.NameRun)})
			require.NoError(t, err)
			assert.Len(t, left, 1)
			assert.Empty(t, rollups(t, st))
		})
	}
}

// A corrupt verdict line fails the plan since the verdict it held would leave its run unprotected
func TestRetainerPlanFailsOnACorruptVerdict(t *testing.T) {
	st := testkit.Open(t)
	add(t, st, trace.NameRun, 40, "", `{"applied":[]}`)
	require.NoError(t, os.WriteFile(filepath.Join(st.Dir, "feedback.jsonl"), []byte("not json\n"), 0o600))

	_, err := retain.New(st.Traces, st.Feedback, st.Outcomes, st.Ledger, count, clock).Plan(context.Background(), retain.Days(30).Cutoff(now))

	assert.ErrorIs(t, err, feedbackfile.ErrCorrupt)
}

func TestDaysCheck(t *testing.T) {
	tcs := []struct {
		name string
		args retain.Days
		want error
	}{
		{"0 keeps every trace", 0, nil},
		{"a week is the shortest", 7, nil},
		{"a month passes", 30, nil},
		{"the largest whole day duration passes", 106751, nil},
		{"an overflowing duration is refused", 106752, retain.ErrDaysTooLong},
		{"a much larger duration is refused", 200000, retain.ErrDaysTooLong},
		{"a day is refused", 1, retain.ErrDaysTooShort},
		{"6 days is refused", 6, retain.ErrDaysTooShort},
		{"a negative count is refused", -1, retain.ErrDaysTooShort},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.ErrorIs(t, tc.args.Check(), tc.want)
		})
	}
}

func TestDaysCutoff(t *testing.T) {
	tcs := []struct {
		name string
		args retain.Days
		want time.Time
	}{
		{"0 keeps every run", 0, time.Time{}},
		{"days Check refuses keep every run", 3, time.Time{}},
		{"a negative count keeps every run", -30, time.Time{}},
		{"an overflowing duration keeps every run", 106752, time.Time{}},
		{"the largest duration stays before now", 106751, now.Add(-106751 * 24 * time.Hour)},
		{"whole days of 24 hours before now", 30, now.Add(-30 * 24 * time.Hour)},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Cutoff(now))
		})
	}
}

func TestDaily(t *testing.T) {
	type args struct {
		days retain.Days
		// The marker before the first trigger and an empty file when empty
		marker string
		// A directory under the record directory the trigger works in and the record directory when empty
		sub string
		// How the retainer opens
		openErr error
		after   time.Duration
	}
	type want struct {
		first, second       bool
		firstErr, secondErr error
		left                int
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"no retention never prunes", args{0, "", "", nil, 0}, want{false, false, nil, nil, 2}},
		{"days Check refuses fail before the marker", args{3, "", "", nil, 0}, want{false, false, retain.ErrDaysTooShort, retain.ErrDaysTooShort, 2}},
		{"a second trigger within a day removes nothing", args{30, "", "", nil, 23 * time.Hour}, want{true, false, nil, nil, 1}},
		{"a trigger a day later prunes again", args{30, "", "", nil, 24 * time.Hour}, want{true, true, nil, nil, 0}},
		{"a marker that does not decode reads as no prune", args{30, "not json", "", nil, 23 * time.Hour}, want{true, false, nil, nil, 1}},
		{"a fresh marker skips the prune", args{30, `{"at":"2026-10-12T06:00:00Z"}`, "", nil, 0}, want{false, false, nil, nil, 2}},
		{"a marker from the future reads as elapsed", args{30, `{"at":"2026-10-14T12:00:00Z"}`, "", nil, 23 * time.Hour}, want{true, false, nil, nil, 1}},
		{"a marker that cannot open fails the trigger", args{30, "", "missing", nil, 0}, want{false, false, retain.ErrMarker, retain.ErrMarker, 2}},
		{
			"a prune that fails to open is not tried again within a day", args{30, "", "", errStore, 23 * time.Hour},
			want{true, false, errStore, nil, 2},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := testkit.Open(t)
			ctx := context.Background()
			add(t, st, trace.NameRun, 40, "", `{"applied":[]}`)
			require.NoError(t, os.WriteFile(filepath.Join(st.Dir, retain.MarkerFile), []byte(tc.args.marker), 0o600))
			at := now
			tick := func() time.Time { return at }
			retainer := retain.New(st.Traces, st.Feedback, st.Outcomes, st.Ledger, count, tick)
			open := func() (*retain.Retainer, error) { return retainer, tc.args.openErr }
			daily := retain.NewDaily(retain.NewMarker(filepath.Join(st.Dir, tc.args.sub)), tc.args.days, open, tick)

			first, err := daily.Run(ctx)
			// One more old run arrives as from a slow import so a second prune would remove it
			add(t, st, trace.NameRun, 40, "", `{"applied":[]}`)
			at = at.Add(tc.args.after)
			second, secondErr := daily.Run(ctx)

			assert.ErrorIs(t, err, tc.want.firstErr)
			assert.ErrorIs(t, secondErr, tc.want.secondErr)
			assert.Equal(t, tc.want.first, first)
			assert.Equal(t, tc.want.second, second)
			left, err := st.Traces.List(ctx, record.TraceFilter{})
			require.NoError(t, err)
			assert.Len(t, left, tc.want.left)
		})
	}
}

// The marker keeps the cutoff of the prune it claimed so a report over the directory reads from it on
func TestDailyMarksTheCutoff(t *testing.T) {
	st := testkit.Open(t)
	ctx := context.Background()
	retainer := retain.New(st.Traces, st.Feedback, st.Outcomes, st.Ledger, count, clock)
	marks := retain.NewMarker(st.Dir)
	daily := retain.NewDaily(marks, 30, func() (*retain.Retainer, error) { return retainer, nil }, func() time.Time { return now })

	ran, err := daily.Run(ctx)

	require.NoError(t, err)
	assert.True(t, ran)
	last, err := marks.Last(ctx)
	require.NoError(t, err)
	assert.Equal(t, record.Mark{At: now, Cutoff: retain.Days(30).Cutoff(now)}, last)
}

func TestDailyDue(t *testing.T) {
	type args struct {
		days retain.Days
		// The content of each marker written by name
		markers map[string]string
		// The names made directories in place of a marker
		dirs []string
	}
	type want struct {
		due bool
		err error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"no retention is never due", args{0, nil, nil}, want{false, nil}},
		{"days Check refuses fail", args{3, nil, nil}, want{false, retain.ErrDaysTooShort}},
		{"no marker is due", args{30, nil, nil}, want{true, nil}},
		{"a fresh marker is not due", args{30, map[string]string{retain.MarkerFile: `{"at":"2026-10-12T06:00:00Z"}`}, nil}, want{false, nil}},
		{"a marker a day old is due", args{30, map[string]string{retain.MarkerFile: `{"at":"2026-10-11T12:00:00Z"}`}, nil}, want{true, nil}},
		{"a marker that does not decode is due", args{30, map[string]string{retain.MarkerFile: "not json"}, nil}, want{true, nil}},
		{"a marker that cannot be read fails", args{30, nil, []string{retain.MarkerFile}}, want{false, retain.ErrMarker}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			for name, content := range tc.args.markers {
				require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600))
			}
			for _, name := range tc.args.dirs {
				require.NoError(t, os.Mkdir(filepath.Join(dir, name), 0o755))
			}
			daily := retain.NewDaily(retain.NewMarker(dir), tc.args.days, nil, func() time.Time { return now })

			due, err := daily.Due(context.Background())

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.due, due)
		})
	}
}

func TestDaysWindow(t *testing.T) {
	week := now.Add(-7 * 24 * time.Hour)
	type args struct {
		days retain.Days
		last time.Time
	}
	tcs := []struct {
		name string
		args args
		want time.Time
	}{
		{"no retention and no prune read every run", args{0, time.Time{}}, time.Time{}},
		{"a prune another surface ran sets the window without retention", args{0, week}, week},
		{"a later cutoff of the retention wins", args{7, now.Add(-30 * 24 * time.Hour)}, week},
		{"a later cutoff of the last prune wins", args{30, week}, week},
		{"a last cutoff after now is left out", args{30, now.Add(time.Hour)}, now.Add(-30 * 24 * time.Hour)},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.days.Window(now, tc.args.last))
		})
	}
}

// A calls trace stays while an answered run of its session follows it
// so an extraction or a replay of a kept run still reads its calls
// and goes at the prune after the one that removed its run
// So a verdict recorded at the last moment keeps both
func TestRetainerPruneCalls(t *testing.T) {
	ctx := context.Background()
	st := testkit.Open(t)
	ago := func(hours int) time.Time { return now.Add(-time.Duration(hours) * time.Hour) }
	// A trace of session s1 at hours ago
	// A run is dated from its turn as the hook dates it
	session := func(name trace.Name, hours, turn int, input string) string {
		tr := trace.Trace{
			ID: trace.NewID(ago(hours)), Name: name, Producer: "bot", SessionID: "s1", Time: ago(hours), Input: []byte(input),
		}
		if name == trace.NameRun {
			tr.DurationMS = ago(hours).Sub(ago(turn)).Milliseconds()
		}
		require.NoError(t, st.Traces.Append(ctx, tr.Record()))
		return tr.ID
	}
	callsAt := func(hours, turn int) string {
		return session(trace.NameCalls, hours, turn, fmt.Sprintf(`{"at":%q,"turn":%q,"calls":[{"tool":"Bash"}]}`,
			ago(hours).Format(time.RFC3339), ago(turn).Format(time.RFC3339)))
	}
	ids := map[string]string{
		"of a kept run":                  callsAt(41*24, 42*24),
		"kept run":                       session(trace.NameRun, 40*24, 42*24, `{"applied":[]}`),
		"of a removed run":               callsAt(36*24, 37*24),
		"removed run":                    session(trace.NameRun, 35*24, 37*24, `{"applied":[]}`),
		"before a continued run":         callsAt(34*24, 34*24+1),
		"continued run":                  session(trace.NameRun, 33*24, 34*24, `{"applied":[],"continued":true}`),
		"answer after the continued run": session(trace.NameRun, 32*24, 34*24+1, `{"applied":[]}`),
		"no run followed":                callsAt(31*24, 31*24+1),
		"young":                          callsAt(2*24, 2*24+1),
	}
	judge(t, st, ids["kept run"], feedback.VerdictApprove, "ann")
	judge(t, st, ids["answer after the continued run"], feedback.VerdictApprove, "ann")
	retainer := retain.New(st.Traces, st.Feedback, st.Outcomes, st.Ledger, count, clock)
	left := func() []string {
		list, err := st.Traces.List(ctx, record.TraceFilter{Name: string(trace.NameCalls)})
		require.NoError(t, err)
		out := []string{}
		for _, tr := range list {
			out = append(out, tr.ID)
		}
		return out
	}
	plan, err := retainer.Plan(ctx, retain.Days(30).Cutoff(now))
	require.NoError(t, err)
	assert.Equal(t, []string{ids["no run followed"]}, plan.Calls,
		"the calls of a run the plan removes wait for the next prune")
	assert.Contains(t, plan.Runs, ids["removed run"])
	_, err = retainer.Prune(ctx, retain.Days(30).Cutoff(now))
	require.NoError(t, err)
	_, err = retainer.Prune(ctx, retain.Days(30).Cutoff(now))
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{ids["of a kept run"], ids["before a continued run"], ids["young"]}, left(),
		"a span ends at the answer a continued run led to, and the calls of a removed run go at the next prune")
}

// Calls whose turn no answer carries go at the first prune past the cutoff even while the answer after them stays
// The stored records cannot tell a removed run from a turn whose prompt left no trace
func TestRetainerPruneCallsOfAnUntracedTurn(t *testing.T) {
	ctx := context.Background()
	st := testkit.Open(t)
	ago := func(hours int) time.Time { return now.Add(-time.Duration(hours) * time.Hour) }
	input, err := json.Marshal(map[string]any{
		"at":    ago(40 * 24).Format(time.RFC3339),
		"turn":  ago(40*24 + 1).Format(time.RFC3339),
		"calls": []map[string]string{{"tool": "Bash"}},
	})
	require.NoError(t, err)
	calls := trace.Trace{
		ID: trace.NewID(ago(40 * 24)), Name: trace.NameCalls, Producer: "bot",
		SessionID: "s1", Time: ago(40 * 24), Input: input,
	}
	require.NoError(t, st.Traces.Append(ctx, calls.Record()))
	answer := trace.Trace{
		ID: trace.NewID(ago(39 * 24)), Name: trace.NameRun, Producer: "bot",
		SessionID: "s1", Time: ago(39 * 24), Input: []byte(`{"applied":[]}`),
	}
	require.NoError(t, st.Traces.Append(ctx, answer.Record()))
	judge(t, st, answer.ID, feedback.VerdictApprove, "ann")
	retainer := retain.New(st.Traces, st.Feedback, st.Outcomes, st.Ledger, count, clock)
	for range 2 {
		_, err := retainer.Prune(ctx, retain.Days(30).Cutoff(now))
		require.NoError(t, err)
	}
	left, err := st.Traces.List(ctx, record.TraceFilter{Name: string(trace.NameCalls)})
	require.NoError(t, err)
	assert.Empty(t, left)
}

// Calls named just past an answer of a short turn are never that answer's even while it stays
func TestRetainerPruneCallsNamedPastAnAnswer(t *testing.T) {
	ctx := context.Background()
	st := testkit.Open(t)
	at := now.Add(-40 * 24 * time.Hour)
	answer := trace.Trace{
		ID: trace.NewID(at), Name: trace.NameRun, Producer: "bot",
		SessionID: "s1", Time: at, DurationMS: 1, Input: []byte(`{"applied":[]}`),
	}
	require.NoError(t, st.Traces.Append(ctx, answer.Record()))
	judge(t, st, answer.ID, feedback.VerdictApprove, "ann")
	input, err := json.Marshal(map[string]any{
		"at":    at.Add(time.Millisecond).Format(time.RFC3339Nano),
		"turn":  at.Add(time.Nanosecond).Format(time.RFC3339Nano),
		"calls": []map[string]string{{"tool": "Bash"}},
	})
	require.NoError(t, err)
	calls := trace.Trace{
		ID: trace.NewID(at.Add(time.Millisecond)), Name: trace.NameCalls, Producer: "bot",
		SessionID: "s1", Time: at.Add(time.Millisecond), Input: input,
	}
	require.NoError(t, st.Traces.Append(ctx, calls.Record()))
	retainer := retain.New(st.Traces, st.Feedback, st.Outcomes, st.Ledger, count, clock)
	for range 2 {
		_, err := retainer.Prune(ctx, retain.Days(30).Cutoff(now))
		require.NoError(t, err)
	}
	left, err := st.Traces.List(ctx, record.TraceFilter{Name: string(trace.NameCalls)})
	require.NoError(t, err)
	assert.Empty(t, left)
	runs, err := st.Traces.List(ctx, record.TraceFilter{Name: string(trace.NameRun)})
	require.NoError(t, err)
	assert.Len(t, runs, 1, "the cited answer stays")
}

// Calls named past an answer go with the answer after it
// and never pass to a later answer while the answer between stays
func TestRetainerPruneCallsNamedBeforeTheAnswerBefore(t *testing.T) {
	ctx := context.Background()
	st := testkit.Open(t)
	at := now.Add(-40 * 24 * time.Hour)
	run := func(after time.Duration, duration int64) trace.Trace {
		return trace.Trace{
			ID: trace.NewID(at.Add(after)), Name: trace.NameRun, Producer: "bot", SessionID: "s1",
			Time: at.Add(after), DurationMS: duration, Input: []byte(`{"applied":[]}`),
		}
	}
	first, untraced, next := run(time.Millisecond, 1), run(1050*time.Microsecond, 0), run(2100*time.Microsecond, 1)
	for _, r := range []trace.Trace{first, untraced, next} {
		require.NoError(t, st.Traces.Append(ctx, r.Record()))
	}
	judge(t, st, first.ID, feedback.VerdictApprove, "ann")
	judge(t, st, next.ID, feedback.VerdictApprove, "ann")
	input, err := json.Marshal(map[string]any{
		"at":    at.Add(1025 * time.Microsecond).Format(time.RFC3339Nano),
		"turn":  first.Time.Add(time.Nanosecond).Format(time.RFC3339Nano),
		"calls": []map[string]string{{"tool": "Bash"}},
	})
	require.NoError(t, err)
	calls := trace.Trace{
		ID: trace.NewID(at.Add(1025 * time.Microsecond)), Name: trace.NameCalls, Producer: "bot",
		SessionID: "s1", Time: at.Add(1025 * time.Microsecond), Input: input,
	}
	require.NoError(t, st.Traces.Append(ctx, calls.Record()))
	retainer := retain.New(st.Traces, st.Feedback, st.Outcomes, st.Ledger, count, clock)
	_, err = retainer.Prune(ctx, retain.Days(30).Cutoff(now))
	require.NoError(t, err)
	left, err := st.Traces.List(ctx, record.TraceFilter{Name: string(trace.NameCalls)})
	require.NoError(t, err)
	assert.Empty(t, left)
}

// An answer at the same time as another keeps its calls whichever was appended first
func TestRetainerPruneCallsOfAnAnswerAtTheTimeOfAnother(t *testing.T) {
	at := now.Add(-40 * 24 * time.Hour)
	run := func(id string, duration int64) trace.Trace {
		return trace.Trace{
			ID: id, Name: trace.NameRun, Producer: "bot", SessionID: "s1",
			Time: at.Add(10 * time.Second), DurationMS: duration, Input: []byte(`{"applied":[]}`),
		}
	}
	answer, other := run(trace.NewID(at.Add(10*time.Second)), 5000), run(trace.NewID(at.Add(10*time.Second)), 0)
	tcs := []struct {
		name string
		args []trace.Trace
	}{
		{"the answer first", []trace.Trace{answer, other}},
		{"the other first", []trace.Trace{other, answer}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			st := testkit.Open(t)
			for _, r := range tc.args {
				require.NoError(t, st.Traces.Append(ctx, r.Record()))
			}
			judge(t, st, answer.ID, feedback.VerdictApprove, "ann")
			input, err := json.Marshal(map[string]any{
				"at":    at.Add(6 * time.Second).Format(time.RFC3339Nano),
				"turn":  at.Add(5 * time.Second).Format(time.RFC3339Nano),
				"calls": []map[string]string{{"tool": "Bash"}},
			})
			require.NoError(t, err)
			calls := trace.Trace{
				ID: trace.NewID(at.Add(6 * time.Second)), Name: trace.NameCalls, Producer: "bot",
				SessionID: "s1", Time: at.Add(6 * time.Second), Input: input,
			}
			require.NoError(t, st.Traces.Append(ctx, calls.Record()))
			retainer := retain.New(st.Traces, st.Feedback, st.Outcomes, st.Ledger, count, clock)
			_, err = retainer.Prune(ctx, retain.Days(30).Cutoff(now))
			require.NoError(t, err)
			left, err := st.Traces.List(ctx, record.TraceFilter{Name: string(trace.NameCalls)})
			require.NoError(t, err)
			assert.Len(t, left, 1)
		})
	}
}
