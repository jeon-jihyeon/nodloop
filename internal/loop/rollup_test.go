package loop_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/loop"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

var cycleStart = time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)

// A day of the first cycle or of the cycle after it at 9 UTC
func cycleDay(days int) time.Time {
	return cycleStart.Add(time.Duration(days)*24*time.Hour + 9*time.Hour)
}

// Records of two cycles of two weeks with drawn runs of two producers
// The first cycle is closed and the second open as of the last run
type rollupRecords struct {
	st  testkit.Stores
	ids map[string]string
}

func (r rollupRecords) run(t *testing.T, name, producer string, days int, in knowledge.RunInput) {
	t.Helper()
	at := cycleDay(days)
	raw, err := json.Marshal(in)
	require.NoError(t, err)
	tr := trace.Trace{ID: trace.NewID(at), Name: trace.NameRun, Producer: producer, Time: at, Input: raw}
	require.NoError(t, r.st.Traces.Append(context.Background(), tr.Record()))
	r.ids[name] = tr.ID
}

func (r rollupRecords) judge(t *testing.T, name string, fb feedback.Feedback) {
	t.Helper()
	fb.TraceID, fb.Time = r.ids[name], cycleDay(30)
	require.NoError(t, r.st.Feedback.Append(context.Background(), fb.Record()))
}

func newRollupRecords(t *testing.T) rollupRecords {
	t.Helper()
	ctx := context.Background()
	r := rollupRecords{st: testkit.Open(t), ids: map[string]string{}}
	gitC, held := knowledge.Ref{ID: "git-c", Version: 1}, knowledge.Ref{ID: "held", Version: 1}
	applied := func(unit string) knowledge.RunInput {
		return knowledge.RunInput{Applied: []knowledge.Ref{gitC}, Drawn: true, Share: 0.5, Scheme: 1, Unit: unit}
	}
	withheld := func(unit string) knowledge.RunInput {
		return knowledge.RunInput{Applied: []knowledge.Ref{}, Withheld: []knowledge.Ref{gitC}, Drawn: true, Share: 0.5, Scheme: 1, Unit: unit}
	}
	knob := trace.Trace{
		ID: trace.NewID(cycleDay(0)), Name: trace.NameKnob, Time: cycleDay(0),
		Input: json.RawMessage(`{"cycle_id":"2026-08-31","start":"2026-08-31T00:00:00Z","knobs":{"cycle_weeks":2}}`),
	}
	require.NoError(t, r.st.Traces.Append(ctx, knob.Record()))
	r.run(t, "before the cycles", "session", -3, applied("u0"))
	r.run(t, "taught", "session", 1, knowledge.RunInput{Applied: []knowledge.Ref{}})
	r.run(t, "proven", "session", 2, applied("u1"))
	r.run(t, "unjudged", "session", 3, applied("u1"))
	r.run(t, "inferred audit", "session", 4, applied("u2"))
	r.run(t, "named", "session", 5, withheld("u3"))
	r.run(t, "refuted", "session", 6, withheld("u3"))
	r.run(t, "undrawn", "session", 7, knowledge.RunInput{Applied: []knowledge.Ref{gitC}})
	r.run(t, "other producer", "bot", 8, knowledge.RunInput{Applied: []knowledge.Ref{gitC}, Drawn: true, Share: 0.2, Scheme: 1})
	hold := applied("u5")
	hold.Applied = []knowledge.Ref{gitC}
	hold.Eligible = []knowledge.Eligible{{Ref: gitC, PInclude: 1}, {Ref: held, PInclude: 0.9}}
	hold.Held = []knowledge.Ref{held}
	r.run(t, "held", "session", 9, hold)
	away := withheld("u7")
	away.Eligible = []knowledge.Eligible{{Ref: gitC}}
	r.run(t, "withheld eligible", "session", 10, away)
	other := applied("u6")
	other.Share = 0.4
	r.run(t, "other share", "session", 11, other)
	r.run(t, "next cycle", "session", 15, applied("u1"))
	r.run(t, "next cycle unjudged", "session", 16, withheld("u4"))
	form := feedback.ReasonForm
	r.judge(t, "taught", feedback.Feedback{Verdict: feedback.VerdictReject, ReasonCode: form, Reviewer: "ann", Proof: trace.ProofKey})
	r.judge(t, "proven", feedback.Feedback{Verdict: feedback.VerdictReject, ReasonCode: form, Reviewer: "ann", Proof: trace.ProofKey})
	r.judge(t, "inferred audit", feedback.Feedback{Verdict: feedback.VerdictApprove, Reviewer: feedback.ReviewerSession, Audit: true})
	r.judge(t, "named", feedback.Feedback{Verdict: feedback.VerdictReject, ReasonCode: feedback.ReasonFact, Reviewer: "ci", Proof: trace.ProofNone})
	r.judge(t, "held", feedback.Feedback{Verdict: feedback.VerdictEdit, ReasonCode: form, Reviewer: "ann", Proof: trace.ProofKey, Audit: true})
	r.judge(t, "next cycle", feedback.Feedback{Verdict: feedback.VerdictReject, ReasonCode: form, Reviewer: "ann", Proof: trace.ProofKey})
	o, err := feedback.NewOutcome(r.ids["refuted"], feedback.ResultRefuted, "", "", "ann", "", cycleDay(30))
	require.NoError(t, err)
	require.NoError(t, r.st.Outcomes.Append(ctx, o.Record()))
	_, _, err = r.st.Ledger.Propose(ctx, knowledge.Knowledge{
		ID: "git-c", Kind: knowledge.KindJudgment, Content: "use git -C", Author: "ann",
		Run: &knowledge.RunScope{Producer: "session"}, Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{r.ids["taught"]}},
	})
	require.NoError(t, err)
	require.NoError(t, testkit.Err(r.st.Ledger.Approve(ctx, "git-c", 1, "ann")))
	return r
}

func (r rollupRecords) stores(since time.Time) loop.Stores {
	return loop.Stores{Traces: r.st.Traces, Verdicts: r.st.Feedback, Outcomes: r.st.Outcomes, Items: r.st.Ledger, Since: since}
}

// The effect rows as one JSON value
func (r rollupRecords) effect(t *testing.T, since time.Time) string {
	t.Helper()
	snap, err := r.stores(since).Snapshot(context.Background(), loop.ReportEffect)
	require.NoError(t, err)
	b, err := json.Marshal(snap.Effect())
	require.NoError(t, err)
	return string(b)
}

// The first cycle closed by its length
func firstCycle() trace.Cycle {
	return trace.Cycle{ID: "2026-08-31", Start: cycleStart, End: cycleStart.Add(14 * 24 * time.Hour)}
}

// Appends the rollup of the first cycle and removes its runs that nothing keeps as a prune would
func (r rollupRecords) prune(t *testing.T) json.RawMessage {
	t.Helper()
	ctx := context.Background()
	rollup, err := r.stores(time.Time{}).Rollup(ctx, firstCycle())
	require.NoError(t, err)
	at := cycleDay(22)
	tr := trace.Trace{ID: trace.NewID(at), Name: trace.NameRollup, Subject: "2026-08-31", Time: at, Input: rollup}
	require.NoError(t, r.st.Traces.Append(ctx, tr.Record()))
	gone := []string{r.ids["unjudged"], r.ids["undrawn"], r.ids["other producer"]}
	removed, err := r.st.Traces.Prune(ctx, func(context.Context) ([]string, error) { return gone, nil })
	require.NoError(t, err)
	require.Equal(t, len(gone), removed)
	return rollup
}

// A closed cycle read from its rollup gives the effect rows it gave from its runs
// 1. the runs a prune left in the cycle count nothing beside the rollup
// 2. a cycle is read whole from its start whatever the window and the runs before the first cycle by the window
func TestStoresEffectReadsRollups(t *testing.T) {
	tcs := []struct {
		name string
		args time.Time
		want time.Time
	}{
		{"no window reads every run", time.Time{}, time.Time{}},
		{"a window inside the first cycle reads from its start", cycleDay(5), cycleStart},
		{"a window inside the open cycle reads from the start of the first cycle", cycleDay(18), cycleStart},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRollupRecords(t)
			before := r.effect(t, tc.args)

			r.prune(t)

			after := r.effect(t, tc.args)
			assert.JSONEq(t, before, after)
			snap, err := r.stores(tc.args).Snapshot(context.Background(), loop.ReportEffect)
			require.NoError(t, err)
			assert.Equal(t, tc.want, snap.Since())
		})
	}
}

// The rollup counts every run of the cycle per unit with what effect reads of them and the versions the hold drew
func TestStoresRollup(t *testing.T) {
	r := newRollupRecords(t)

	var got loop.Rollup
	require.NoError(t, json.Unmarshal(r.prune(t), &got))

	unit := func(u loop.UnitRollup) string {
		b, _ := json.Marshal(u)
		return string(b)
	}
	var units []string
	for _, p := range got.Producers {
		for _, u := range p.Units {
			units = append(units, p.Producer+" "+unit(u))
		}
	}
	assert.Equal(t, []any{"2026-08-31", cycleStart, firstCycle().End}, []any{got.CycleID, got.Start, got.End})
	assert.Equal(t, []string{
		`bot {"unit":"` + r.ids["other producer"] + `","arm":"applied","share":0.2,"runs":1,"judged":0,"corrected":0,"same_reason":0,` +
			`"named":0,"inferred":0,"audit_judged":0,"audit_corrected":0,"confirmed":0,"refuted":0}`,
		`session {"unit":"u1","arm":"applied","share":0.5,"runs":2,"judged":1,"corrected":1,"same_reason":1,` +
			`"named":0,"inferred":0,"audit_judged":0,"audit_corrected":0,"confirmed":0,"refuted":0}`,
		`session {"unit":"u2","arm":"applied","share":0.5,"runs":1,"judged":1,"corrected":0,"same_reason":0,` +
			`"named":0,"inferred":1,"audit_judged":1,"audit_corrected":0,"confirmed":0,"refuted":0}`,
		`session {"unit":"u5","arm":"applied","share":0.5,"runs":1,"judged":1,"corrected":1,"same_reason":1,` +
			`"named":0,"inferred":0,"audit_judged":1,"audit_corrected":1,"confirmed":0,"refuted":0}`,
		`session {"unit":"u6","arm":"applied","share":0.4,"runs":1,"judged":0,"corrected":0,"same_reason":0,` +
			`"named":0,"inferred":0,"audit_judged":0,"audit_corrected":0,"confirmed":0,"refuted":0}`,
		`session {"unit":"u3","arm":"withheld","share":0.5,"runs":2,"judged":1,"corrected":1,"same_reason":0,` +
			`"named":1,"inferred":0,"audit_judged":0,"audit_corrected":0,"confirmed":0,"refuted":1}`,
		`session {"unit":"u7","arm":"withheld","share":0.5,"runs":1,"judged":0,"corrected":0,"same_reason":0,` +
			`"named":0,"inferred":0,"audit_judged":0,"audit_corrected":0,"confirmed":0,"refuted":0}`,
	}, units)
	assert.Equal(t, []any{0, 1}, []any{got.Producers[0].Undrawn, got.Producers[1].Undrawn})
	assert.Equal(t, []loop.DrawShare{{Share: 0.4, Scheme: 1}, {Share: 0.5, Scheme: 1}}, got.Producers[1].Draws)
	assert.Equal(t, []loop.ItemRollup{
		{ID: "git-c", Version: 1, Units: []loop.ItemUnit{{Unit: "u5", Included: 1, IncludedJudged: 1, IncludedCorrected: 1}}},
		{ID: "held", Version: 1, Units: []loop.ItemUnit{{Unit: "u5", Held: 1, HeldJudged: 1, HeldCorrected: 1}}},
	}, got.Items)
}

// The first rollup of a cycle counts and one that does not decode counts nothing
func TestNewRollups(t *testing.T) {
	rollup := func(id string, days int, input string) trace.Trace {
		return trace.Trace{ID: id, Name: trace.NameRollup, Time: cycleDay(days), Input: json.RawMessage(input)}
	}
	first := `{"cycle_id":"2026-08-31","start":"2026-08-31T00:00:00Z","end":"2026-09-14T00:00:00Z","producers":[{"producer":"first"}]}`
	later := `{"cycle_id":"2026-08-31","start":"2026-08-31T00:00:00Z","end":"2026-09-14T00:00:00Z","producers":[{"producer":"later"}]}`
	next := `{"cycle_id":"2026-09-14","start":"2026-09-14T00:00:00Z","end":"2026-09-28T00:00:00Z","producers":[{"producer":"next"}]}`
	traces := trace.Traces{
		rollup("r4", 40, next), rollup("r1", 30, first), rollup("r3", 31, later), rollup("r2", 30, `not json`), rollup("r5", 30, later),
		{ID: "k1", Name: trace.NameKnob, Time: cycleDay(0), Input: json.RawMessage(first)},
	}

	got := loop.NewRollups(traces)

	var producers []string
	for _, r := range got {
		producers = append(producers, r.Producers[0].Producer)
	}
	assert.Equal(t, []string{"first", "next"}, producers)
}

// Rollups alone give the rows their units sum to
// 1. a unit of an arm no release knows counts nowhere
// 2. the draws of two rollups under two shares warn of mixed shares
func TestRunsEffectFromRollups(t *testing.T) {
	unit := func(name string, arm loop.Arm, runs, judged, corrected int) loop.UnitRollup {
		return loop.UnitRollup{Unit: name, Arm: arm, Share: 0.5, Runs: runs, Judged: judged, Corrected: corrected, Named: judged}
	}
	rolled := loop.Rollups{
		{CycleID: "2026-08-31", Start: cycleStart, End: firstCycle().End, Producers: []loop.ProducerRollup{{
			Producer: "bot", Undrawn: 2, Draws: []loop.DrawShare{{Share: 0.5, Scheme: 1}},
			Units: []loop.UnitRollup{unit("u1", loop.ArmApplied, 3, 2, 1), unit("u2", loop.ArmWithheld, 2, 2, 2), unit("u3", "control", 9, 9, 9)},
		}}},
		{CycleID: "2026-09-14", Start: firstCycle().End, End: firstCycle().End.Add(14 * 24 * time.Hour), Producers: []loop.ProducerRollup{{
			Producer: "bot", Draws: []loop.DrawShare{{Share: 0.4, Scheme: 1}}, Units: []loop.UnitRollup{unit("u1", loop.ArmApplied, 1, 1, 0)},
		}}},
	}
	type want struct {
		arm                                   loop.Arm
		runs, judged, corrected, named, units int
		undrawn                               int
		warnings                              []loop.EffectWarning
	}

	rows := loop.NewRuns(nil, nil).Effect(nil, nil, rolled, time.Time{})

	var got []want
	for _, row := range rows {
		got = append(got, want{row.Arm, row.Runs, row.Judged, row.Corrected, row.Named, row.Units, row.Undrawn, row.Warnings})
	}
	warnings := []loop.EffectWarning{loop.WarnInsufficient, loop.WarnMixedShares}
	assert.Equal(t, []want{
		{loop.ArmApplied, 4, 3, 1, 3, 1, 2, warnings},
		{loop.ArmWithheld, 2, 2, 2, 2, 1, 2, warnings},
	}, got)
}

// A store that fails to read fails the rollup and the effect and curation reports that read it
func TestStoresRollupFails(t *testing.T) {
	tcs := []struct {
		name string
		// The record file a corrupt line is appended to
		args string
	}{
		{"a corrupt trace fails", "traces.jsonl"},
		{"a corrupt verdict fails", "feedback.jsonl"},
		{"a corrupt outcome fails", "outcomes.jsonl"},
		{"a corrupt knowledge record fails", "knowledge.jsonl"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRollupRecords(t)
			path := filepath.Join(r.st.Dir, tc.args)
			f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
			require.NoError(t, err)
			_, err = f.WriteString("not json\n")
			require.NoError(t, err)
			require.NoError(t, f.Close())

			_, rollupErr := r.stores(time.Time{}).Rollup(context.Background(), firstCycle())
			_, effectErr := r.stores(time.Time{}).Snapshot(context.Background(), loop.ReportEffect)
			_, curationErr := r.stores(time.Time{}).Curation(context.Background(), cycleDay(20))

			assert.Error(t, rollupErr)
			assert.Error(t, effectErr)
			assert.Error(t, curationErr)
		})
	}
}
