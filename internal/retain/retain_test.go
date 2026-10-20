package retain_test

import (
	"context"
	"errors"
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
	judge(t, st, ids["judged"], feedback.VerdictApprove, "")
	judge(t, st, ids["inferred"], feedback.VerdictReject, feedback.ReviewerSession)
	judge(t, st, ids["withdrawn"], feedback.VerdictWithdraw, "")
	o, err := feedback.NewOutcome(ids["checked"], feedback.ResultRefuted, "", "", "", now)
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
	retainer := retain.New(st.Traces, st.Feedback, st.Outcomes, st.Ledger)
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
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, kept[ids[tc.args]])
		})
	}
	assert.ElementsMatch(t, []string{ids["unjudged"], ids["earlier use"], ids["failed use"], ids["use appended late"]}, plan.Runs)
	assert.ElementsMatch(t, []string{ids["classify of a removed run"], ids["young classify of a removed run"]}, plan.Classify)
	assert.Equal(t, 8, plan.Kept)
	assert.Equal(t, 6, removed)
}

// A verdict recorded between the plan and its removal keeps the run and its classify trace
func TestRetainerApplyReadsTheCitationsAgain(t *testing.T) {
	st := testkit.Open(t)
	ctx := context.Background()
	run := add(t, st, trace.NameRun, 40, "", `{"applied":[]}`)
	classify := add(t, st, trace.NameClassify, 40, run, `{}`)
	retainer := retain.New(st.Traces, st.Feedback, st.Outcomes, st.Ledger)
	plan, err := retainer.Plan(ctx, retain.Days(30).Cutoff(now))
	require.NoError(t, err)
	require.Equal(t, []string{run}, plan.Runs)
	judge(t, st, run, feedback.VerdictApprove, "")

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
	traces := judgingTraces{Store: st.Traces, judge: func() { judge(t, st, run, feedback.VerdictReject, "") }}
	retainer := retain.New(traces, st.Feedback, st.Outcomes, st.Ledger)
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

			removed, err := retain.New(traces, verdicts, st.Outcomes, ledger).Prune(ctx, tc.args.days.Cutoff(now))

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.removed, removed)
			left, err := st.Traces.List(ctx, record.TraceFilter{})
			require.NoError(t, err)
			assert.Len(t, left, tc.want.left)
		})
	}
}

// A corrupt verdict line fails the plan since the verdict it held would leave its run unprotected
func TestRetainerPlanFailsOnACorruptVerdict(t *testing.T) {
	st := testkit.Open(t)
	add(t, st, trace.NameRun, 40, "", `{"applied":[]}`)
	require.NoError(t, os.WriteFile(filepath.Join(st.Dir, "feedback.jsonl"), []byte("not json\n"), 0o600))

	_, err := retain.New(st.Traces, st.Feedback, st.Outcomes, st.Ledger).Plan(context.Background(), retain.Days(30).Cutoff(now))

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
			clock := now
			retainer := retain.New(st.Traces, st.Feedback, st.Outcomes, st.Ledger)
			open := func() (*retain.Retainer, error) { return retainer, tc.args.openErr }
			daily := retain.NewDaily(retain.NewMarker(filepath.Join(st.Dir, tc.args.sub)), tc.args.days, open, func() time.Time { return clock })

			first, err := daily.Run(ctx)
			// One more old run arrives as from a slow import so a second prune would remove it
			add(t, st, trace.NameRun, 40, "", `{"applied":[]}`)
			clock = clock.Add(tc.args.after)
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
	retainer := retain.New(st.Traces, st.Feedback, st.Outcomes, st.Ledger)
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
		{"no retention is never due", args{0, nil}, want{false, nil}},
		{"days Check refuses fail", args{3, nil}, want{false, retain.ErrDaysTooShort}},
		{"no marker is due", args{30, nil}, want{true, nil}},
		{"a fresh marker is not due", args{30, map[string]string{retain.MarkerFile: `{"at":"2026-10-12T06:00:00Z"}`}}, want{false, nil}},
		{"a marker a day old is due", args{30, map[string]string{retain.MarkerFile: `{"at":"2026-10-11T12:00:00Z"}`}}, want{true, nil}},
		{"a marker that does not decode is due", args{30, map[string]string{retain.MarkerFile: "not json"}}, want{true, nil}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			for name, content := range tc.args.markers {
				require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600))
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
