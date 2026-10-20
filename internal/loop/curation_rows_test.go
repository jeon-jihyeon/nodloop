package loop_test

import (
	"context"
	"encoding/json"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	knowledgefile "github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	"github.com/jeon-jihyeon/nodloop/internal/loop"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// A curate event of the name with its input and the envelope of the source
// A source the person chose carries no probability
func event(t *testing.T, name trace.Name, id, ref, session string, at time.Time, source trace.Source, input map[string]any) trace.Trace {
	t.Helper()
	p := 1.0
	draw := &trace.Draw{CycleID: "2026-09-21", PolicyVersion: "0.10.0+1a2b3c4d", Unit: id, Seed: "5eed", DrawID: "0123456789abcdef", P: &p, Drawn: true}
	if source != trace.SourcePush {
		draw = &trace.Draw{CycleID: "2026-09-21", PolicyVersion: "0.10.0+1a2b3c4d"}
	}
	if name == trace.NameIntervention {
		draw = nil
	}
	return trace.Trace{ID: id, Name: name, Ref: ref, SessionID: session, Time: at, Source: source, Draw: draw, Input: mustJSON(t, input)}
}

// A decide of curate in add b1 with the action
func decideTrace(t *testing.T, id, action string, at time.Time) trace.Trace {
	t.Helper()
	tr := event(t, trace.NameDecide, id, "run-1", "", at, trace.SourcePush, map[string]any{"kind": "add", "id": "k-1", "version": 1, "anchor": 1})
	tr.Output = mustJSON(t, map[string]any{"tier": 1, "outcome": "conform", "bin": "b1", "action": action})
	return tr
}

// Touchpoints per cycle and per week with notices, proposals and the interventions of an agent apart but every digest
// The overturns of the person over the same records
func TestCurationTouchpoints(t *testing.T) {
	day := func(d int) time.Time { return cycleAt(0).AddDate(0, 0, d).Add(10 * time.Hour) }
	push, manual := trace.SourcePush, trace.SourceManual
	expose := func(id, ref, session, kind string, runes int, at time.Time, source trace.Source) trace.Trace {
		return event(t, trace.NameExpose, id, ref, session, at, source, map[string]any{"kind": kind, "runes": runes})
	}
	answer := func(id, ref, choice string, overturn bool, at time.Time, source trace.Source) trace.Trace {
		authority := map[bool]string{true: "agent", false: "person"}[choice == "no_answer"]
		return event(t, trace.NameAnswer, id, ref, "s1", at, source, map[string]any{"choice": choice, "overturn": overturn, "authority": authority})
	}
	intervene := func(id, action, target, choice, authority string) trace.Trace {
		return event(t, trace.NameIntervention, id, "", "", day(2), "", map[string]any{
			"action": action, "id": target, "version": 1, "choice": choice, "authority": authority,
		})
	}
	traces := []trace.Trace{
		knobTrace(t, 0, "high", cycleAt(0), true),
		decideTrace(t, "d1", "apply", day(1)), expose("x1", "d1", "s1", "audit", 120, day(1), push), answer("a1", "x1", "same", false, day(1), push),
		decideTrace(t, "d2", "ask", day(1)), expose("x2", "d2", "s1", "direction", 300, day(1), push), answer("a2", "x2", "drop", true, day(1), push),
		decideTrace(t, "d3", "apply", day(1)), expose("x3", "d3", "s2", "direction", 0, day(1), manual), answer("a3", "x3", "keep", false, day(1), manual),
		expose("x4", "d1", "s3", "notice", 40, day(1), push),
		intervene("i1", "judge", "k-9", "keep", "agent"), expose("x5", "i1", "s4", "direction", 200, day(2), push),
		decideTrace(t, "d5", "apply", day(2)), expose("x6", "d5", "s1", "audit", 150, day(2), push), answer("a6", "x6", "no_answer", false, day(3), push),
		intervene("i2", "judge", "k-1", "undo", "person"), intervene("i3", "judge", "k-2", "undo", "person"), intervene("i4", "pause", "", "", "person"), intervene("i5", "digest", "", "", "agent"),
		decideTrace(t, "d4", "shadow", day(8)), expose("x7", "d4", "s5", "shadow", 500, day(8), push), answer("a7", "x7", "drop", true, day(8), push),
		answer("a8", "x-gone", "same", false, day(2), push),
		decideTrace(t, "d6", "ask", day(2)), drawless(expose("x8", "d6", "s1", "direction", 250, day(2), push)), answer("a9", "x8", "drop", true, day(2), push),
		event(t, trace.NameAnswer, "a10", "x2", "s1", day(2), push, map[string]any{"choice": "keep", "authority": "agent"}),
	}
	scope := &knowledge.RunScope{Producer: "session"}
	items := []knowledge.Knowledge{
		{ID: "k-1", Version: 1, Kind: knowledge.KindJudgment, Content: "c", Status: knowledge.StatusApproved, Approver: knowledge.ApproverNodloop, Run: scope},
		{ID: "k-2", Version: 1, Kind: knowledge.KindJudgment, Content: "c", Status: knowledge.StatusApproved, Approver: "ann", Run: scope},
	}

	r := curationOf(t, traces, items, cycleAt(1).AddDate(0, 0, 1))

	median := func(v float64) *float64 { return &v }
	assert.Equal(t, []loop.Touchpoints{
		{
			Period: "cycle", Start: "2026-09-21", Questions: map[string]map[string]int{
				"audit": {"push": 2}, "direction": {"push": 2, "manual": 1}, "shadow": {"push": 1},
			},
			Notices: 1, Proposals: 1, Sessions: 4, Answers: 6, NoAnswer: 1, Interventions: map[string]int{"judge": 2, "pause": 1, "digest": 1},
			RunesMedian: median(225), RunesMax: 500,
		},
		{
			Period: "week", Start: "2026-09-21", Questions: map[string]map[string]int{"audit": {"push": 2}, "direction": {"push": 2, "manual": 1}},
			Notices: 1, Proposals: 1, Sessions: 3, Answers: 5, NoAnswer: 1, Interventions: map[string]int{"judge": 2, "pause": 1, "digest": 1},
			RunesMedian: median(200), RunesMax: 300,
		},
		{
			Period: "week", Start: "2026-09-28", Questions: map[string]map[string]int{"shadow": {"push": 1}}, Sessions: 1, Answers: 1,
			Interventions: map[string]int{}, RunesMedian: median(500), RunesMax: 500,
		},
	}, r.Touchpoints)
	assert.Equal(t, loop.Overturns{
		Direction: 1, DirectionOverturned: 1, Manual: 1, Undo: 1, Pause: 1,
		Repeats: []loop.RepeatRow{{Approver: knowledge.ApproverNodloop, Items: 1}, {Approver: "person", Items: 1}},
	}, r.Overturns)
	assert.Equal(t, []loop.CurationCycle{
		{
			ID: "2026-09-21", Start: cycleAt(0), End: cycleAt(1), Closed: true, Policies: []string{"0.10.0+1a2b3c4d"},
			Model: "claude-sonnet-5-5", Effort: "high", Prompt: "1", Activated: true,
		},
		{
			ID: "2026-10-05", Start: cycleAt(1), End: cycleAt(2), Policies: []string{"0.10.0+1a2b3c4d"},
			Model: "claude-sonnet-5-5", Effort: "high", Prompt: "1", Activated: true,
		},
	}, r.Cycles, "a cycle no pass started runs on the knobs of the one before")
}

// The trace with no draw as a record of an earlier shape holds it
func drawless(tr trace.Trace) trace.Trace {
	tr.Draw = nil
	return tr
}

// Tier 1 in shadow with the bound a closed kind reads from the direction answers the judge found conform and the calibration of the judge
// 1. the open kind and bin reads only audits and shadow answers
// 2. tier 0 carries out with no knob between
func TestCurationShadow(t *testing.T) {
	shadow := func(cycle int, p float64, choice string) ask {
		return ask{
			cycle: cycle, tier: 1, kind: "add", bin: "b1", outcome: "conform", question: "shadow", p: p, choice: choice,
			overturn: choice == "drop", source: trace.SourcePush,
		}
	}
	direction := func(kind, choice string) ask {
		return ask{
			cycle: 1, tier: 2, kind: kind, bin: "b1", outcome: "conform", question: "direction", p: 1, choice: choice,
			overturn: choice == "drop", source: trace.SourcePush,
		}
	}
	asks := []ask{
		shadow(0, 0.25, "keep"), shadow(0, 0.25, "keep"), shadow(1, 0.5, "keep"), shadow(1, 0.5, "drop"),
		{cycle: 1, tier: 1, kind: "add", bin: "b1", outcome: "conform", action: "shadow"},
		direction("update", "keep"), direction("update", "keep"), direction("update", "drop"), direction("add", "keep"),
		{cycle: 1, kind: "drop", action: "drop", question: "audit", p: 0.05, choice: "same", source: trace.SourcePush},
		{cycle: 1, tier: 1, kind: "withdraw", action: "withdraw", question: "audit", p: 0.15, choice: "same", source: trace.SourcePush},
	}
	traces := []trace.Trace{
		knobTrace(t, 0, "high", cycleAt(0), false), knobTrace(t, 1, "high", cycleAt(0), false), knobTrace(t, 2, "high", cycleAt(0), false),
	}
	for i, a := range asks {
		traces = append(traces, a.traces(t, i)...)
	}

	r := curationOf(t, traces, nil, cycleAt(2).AddDate(0, 0, 13))

	type row struct {
		kind, bin                                    string
		decided, carried, asked, drawn, same, differ int
		p                                            []float64
		e                                            float64
		open                                         bool
	}
	var rows []row
	for _, k := range r.Kinds {
		rows = append(rows, row{k.Kind, k.Bin, k.Decided, k.Carried, k.Asked, k.Drawn, k.Same, k.Differ, k.P, math.Round(*k.E*1e6) / 1e6, k.Open})
	}
	assert.Equal(t, []row{
		{"add", "b1", 6, 0, 1, 0, 3, 1, []float64{}, 0.558711, true},
		{"drop", "", 1, 1, 0, 1, 1, 0, []float64{0.05}, 1.055556, true},
		{"update", "b1", 3, 0, 3, 0, 2, 1, []float64{}, 0.538066, false},
		{"withdraw", "", 1, 1, 0, 1, 1, 0, []float64{0.15}, 1.055556, true},
	}, rows)
	require.Len(t, r.Shadow.Kinds, 1)
	s := r.Shadow.Kinds[0]
	assert.Equal(t, []any{2, "add", 5, 4, []float64{0.25, 0.5}, 4, 3, 0.75, 0.558711, false},
		[]any{r.Shadow.Cycles, s.Kind, s.Decided, s.Asked, s.PSelect, s.Answered, s.Same, *s.Share, round6(s.E), s.BoundMet})
	require.Len(t, r.Calibration.Bins, 1)
	cal := r.Calibration.Bins[0]
	assert.Equal(t, []any{"b1", 8, 6, 0.75, 0.409275, 0.928521, 0.2359, 8},
		[]any{cal.Bin, cal.Answered, cal.Same, *cal.Rate, round6(cal.RateLow), round6(cal.RateHigh), round6(r.Calibration.Brier), r.Calibration.Scored})
}

func round6(v *float64) float64 {
	return math.Round(*v*1e6) / 1e6
}

// The CUSUM alarm reaches the knobs until a knob event follows it
// A knob event without a target or CUSUM it can read bounds nothing
func TestCurationKnobs(t *testing.T) {
	// Three differ alarm at the third, a decision left in shadow and one of an earlier shape without its draw
	asked := slices.Concat(
		audit(0, 0.15, "undo").traces(t, 0), audit(0, 0.15, "undo").traces(t, 1), audit(0, 0.15, "undo").traces(t, 2),
		ask{cycle: 0, tier: 1, kind: "add", bin: "b1", outcome: "conform", action: "shadow"}.traces(t, 3),
		[]trace.Trace{drawless(decideTrace(t, "d-old", "apply", cycleAt(0).Add(30*time.Hour)))},
	)
	narrowed := knobTrace(t, 0, "high", cycleAt(0), true)
	narrowed.Time = cycleAt(0).AddDate(0, 0, 2)
	narrowed.ID = trace.NewID(narrowed.Time)
	bare := trace.Trace{
		ID: "k-bare", Name: trace.NameKnob, Time: cycleAt(0).Add(9 * time.Hour),
		Input: json.RawMessage(`{"cycle_id":"2026-09-21","start":"2026-09-21T00:00:00Z","knobs":{"cycle_weeks":2}}`),
	}
	type want struct {
		cycles      int
		steps       []int
		alarm       bool
		narrow      []loop.KindBin
		decided     int
		bound, met  bool
		shadowBound bool
	}
	alarmed := []loop.KindBin{{Kind: "add", Bin: "b1"}}
	tcs := []struct {
		name string
		args []trace.Trace
		want want
	}{
		{
			"an alarm after the newest knob event names its kinds and bins",
			append([]trace.Trace{knobTrace(t, 0, "high", cycleAt(0), true)}, asked...),
			want{1, []int{3}, true, alarmed, 4, true, false, true},
		},
		{
			"a knob event after the alarm shows the pass narrowed already",
			append([]trace.Trace{knobTrace(t, 0, "high", cycleAt(0), true), narrowed}, asked...),
			want{1, []int{3}, false, []loop.KindBin{}, 4, true, false, true},
		},
		{
			"a knob event without a target or a CUSUM bounds and alarms nothing",
			append([]trace.Trace{bare}, asked...),
			want{1, []int{}, false, []loop.KindBin{}, 4, false, false, false},
		},
		{
			"records without a knob event read every decision with nothing to bound against",
			asked,
			want{0, []int{}, false, []loop.KindBin{}, 5, false, false, false},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := curationOf(t, tc.args, nil, cycleAt(0).AddDate(0, 0, 13))

			require.Len(t, r.Kinds, 1)
			require.Len(t, r.Shadow.Kinds, 1)
			steps := []int{}
			for _, a := range r.CUSUM.Alarms {
				steps = append(steps, a.Step)
			}
			assert.Equal(t, tc.want, want{
				len(r.Cycles), steps, r.CUSUM.Alarm, r.CUSUM.Narrow, r.Kinds[0].Decided, r.Kinds[0].Bound != nil, r.Kinds[0].BoundMet,
				r.Shadow.Kinds[0].Bound != nil,
			})
			assert.Equal(t, 3, r.CUSUM.Answers)
		})
	}
}

// A closed cycle read from its rollup gives the item effects it gave from its runs
// and a version nodloop approved compares the runs that included it with the runs the hold kept it from
func TestStoresCurationItems(t *testing.T) {
	ctx := context.Background()
	r := newRollupRecords(t)
	held := knowledge.Ref{ID: "held", Version: 1}
	store, err := knowledgefile.New(r.st.Dir)
	require.NoError(t, err)
	// nodloop approved held and an earlier record of git-c before the person approved it again
	for _, id := range []string{"held", "git-c"} {
		require.NoError(t, store.Append(ctx, knowledge.Knowledge{
			ID: id, Version: 1, Kind: knowledge.KindJudgment, Content: "c", Status: knowledge.StatusApproved, Approver: knowledge.ApproverNodloop,
			Run: &knowledge.RunScope{Producer: "session"}, Time: cycleDay(0),
		}.Record()))
	}
	// The run includes held and a version a person approved
	// No hold draws the version of the person and no item effect reads it
	own := knowledge.Ref{ID: "own", Version: 1}
	included := func(unit string) knowledge.RunInput {
		return knowledge.RunInput{
			Applied: []knowledge.Ref{held, own}, Eligible: []knowledge.Eligible{{Ref: held, PInclude: 0.9}, {Ref: own, PInclude: 1}},
			Drawn: true, Share: 0.5, Scheme: 1, Unit: unit,
		}
	}
	r.run(t, "included", "session", 12, included("u8"))
	r.run(t, "included unjudged", "session", 13, included("u9"))
	r.run(t, "included in the open cycle", "session", 17, included("u8"))
	r.judge(t, "included", feedback.Feedback{Verdict: feedback.VerdictReject, ReasonCode: feedback.ReasonForm, Reviewer: "ann", Proof: trace.ProofKey})
	report := func() string {
		got, err := r.stores(time.Time{}).Curation(ctx, cycleDay(20))
		require.NoError(t, err)
		b, err := json.Marshal(got.Items)
		require.NoError(t, err)
		return string(b)
	}
	before := report()

	r.prune(t)

	after := report()
	assert.JSONEq(t, before, after)
	var items []loop.ItemEffect
	require.NoError(t, json.Unmarshal([]byte(after), &items))
	require.Len(t, items, 2)
	assert.Equal(t, "git-c", items[0].ID)
	it := items[1]
	assert.Equal(t, []any{"held", 3, 3, 1, 1, 1, 1, 1, 1.0, 1.0, 0.0, -0.793451, 0.793451},
		[]any{it.ID, it.Units, it.Included, it.IncludedJudged, it.IncludedCorrected, it.Held, it.HeldJudged, it.HeldCorrected,
			*it.IncludedRate, *it.HeldRate, *it.Difference, round6(it.DifferenceLow), round6(it.DifferenceHigh)})
}

// The judge of a cycle against the last cycle of its configuration by the chi squared p of the outcomes with their bins
// The p of one to three degrees of freedom against values computed apart and the bins whose own share moved
func TestCurationShift(t *testing.T) {
	// n decisions of the cycle the judge found with the outcome in the bin
	decided := func(cycle, n int, outcome, bin string) []ask {
		return repeated(n, ask{cycle: cycle, tier: 2, kind: "add", bin: bin, outcome: outcome})
	}
	type want struct {
		against        string
		judged, before int
		p              float64
		shift          bool
		touched        []string
	}
	tcs := []struct {
		name string
		args []ask
		want want
	}{
		{
			"alike cycles read a p of 1",
			slices.Concat(decided(0, 10, "conform", "b1"), decided(0, 10, "split", ""), decided(1, 10, "conform", "b1"), decided(1, 10, "split", "")),
			want{"2026-09-21", 20, 20, 1, false, []string{}},
		},
		{
			"two cells of one degree of freedom shift below 0.01 with the bin whose share moved",
			slices.Concat(decided(0, 18, "conform", "b1"), decided(0, 2, "split", ""), decided(1, 10, "conform", "b1"), decided(1, 10, "split", "")),
			want{"2026-09-21", 20, 20, 0.005775, true, []string{"b1"}},
		},
		{
			"three cells of two degrees of freedom stay below the shift though one bin moved",
			slices.Concat(
				decided(0, 16, "conform", "b1"), decided(0, 2, "conform", "b2"), decided(0, 2, "split", ""),
				decided(1, 8, "conform", "b1"), decided(1, 2, "conform", "b2"), decided(1, 10, "split", ""),
			),
			want{"2026-09-21", 20, 20, 0.018316, false, []string{"b1"}},
		},
		{
			"four cells of three degrees of freedom",
			slices.Concat(
				decided(0, 12, "conform", "b1"), decided(0, 3, "conform", "b2"), decided(0, 3, "split", ""), decided(0, 2, "against", ""),
				decided(1, 6, "conform", "b1"), decided(1, 4, "conform", "b2"), decided(1, 5, "split", ""), decided(1, 5, "against", ""),
			),
			want{"2026-09-21", 20, 20, 0.269281, false, []string{}},
		},
		{
			"five cells of four degrees of freedom",
			slices.Concat(
				decided(0, 10, "conform", "b1"), decided(0, 3, "conform", "b2"), decided(0, 3, "conform", "b3"), decided(0, 2, "split", ""),
				decided(0, 2, "against", ""), decided(1, 5, "conform", "b1"), decided(1, 5, "conform", "b2"), decided(1, 4, "conform", "b3"),
				decided(1, 3, "split", ""), decided(1, 3, "against", ""),
			),
			want{"2026-09-21", 20, 20, 0.607549, false, []string{}},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := curation(t, []string{"high", "high"}, tc.args)

			require.Len(t, r.Shift, 1)
			s := r.Shift[0]
			assert.Equal(t, tc.want, want{s.Against, s.Judged, s.AgainstJudged, round6(s.P), s.Shift, s.Touched})
			assert.False(t, s.Changed)
		})
	}
}

// Other sizes of the few-shot sets are another judge configuration and the same sizes keep it
func TestCurationShiftSizes(t *testing.T) {
	type want struct {
		changed, shift bool
	}
	tcs := []struct {
		name string
		args int
		want want
	}{
		{"the same sizes keep the judge", 4, want{false, false}},
		{"other sizes are another judge", 1, want{true, true}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sized := func(cycle, kept int) trace.Trace {
				tr := knobTrace(t, cycle, "high", cycleAt(0), true)
				var input map[string]any
				require.NoError(t, json.Unmarshal(tr.Input, &input))
				judge, ok := input["judge"].(map[string]any)
				require.True(t, ok)
				judge["kept"], judge["refused"] = kept, 2
				tr.Input = mustJSON(t, input)
				return tr
			}

			r := curationOf(t, []trace.Trace{sized(0, 4), sized(1, tc.args)}, nil, cycleAt(1).AddDate(0, 0, 13))

			// A report without a shift row reads as no shift
			shifts := append([]loop.ShiftRow{{}}, r.Shift...)
			shift := shifts[len(shifts)-1]
			assert.Equal(t, tc.want, want{shift.Changed, shift.Shift})
		})
	}
}
