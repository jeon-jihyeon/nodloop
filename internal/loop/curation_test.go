package loop_test

import (
	"cmp"
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	knowledgefile "github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	"github.com/jeon-jihyeon/nodloop/internal/loop"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
)

// The Monday the first cycle of the curation fixtures starts on
var curationStart = time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)

// The start of the cycle of two weeks counted from the first
func cycleAt(cycle int) time.Time {
	return curationStart.AddDate(0, 0, 14*cycle)
}

// One decision of curate with the question about it and what the person or nobody answered
// A decision with no question is recorded alone
type ask struct {
	cycle   int
	tier    int
	kind    string
	bin     string
	outcome string
	// What the decide did and by the question when empty
	action   string
	question string
	// The probability of the audit draw of the decide or else of the expose
	p        float64
	choice   string
	overturn bool
	source   trace.Source
}

// A change tier 1 carried out in add b1 and drawn for audit at p with the choice the person made
func audit(cycle int, p float64, choice string) ask {
	return ask{
		cycle: cycle, tier: 1, kind: "add", bin: "b1", outcome: "conform", question: "audit", p: p, choice: choice, overturn: choice == "undo",
		source: trace.SourcePush,
	}
}

// The ask repeated n times
func repeated(n int, a ask) []ask {
	out := make([]ask, n)
	for i := range out {
		out[i] = a
	}
	return out
}

// The knob trace a pass appended on the morning of the start of the cycle
// Its judge runs at the effort since the first cycle of that effort and tier 1 carries out what it decides once activated
func knobTrace(t *testing.T, cycle int, effort string, judgeStart time.Time, activated bool) trace.Trace {
	t.Helper()
	start := cycleAt(cycle)
	input, err := json.Marshal(map[string]any{
		"cycle_id": start.Format(trace.CycleDate), "start": start, "policy_version": "0.10.0+1a2b3c4d", "seed": "5eed", "cause": "start",
		"knobs": map[string]any{
			"activated": activated, "item_hold": 0.1, "cycle_weeks": 2, "open": []map[string]string{{"kind": "add", "bin": "b1"}, {"kind": "withdraw"}},
			"audit": map[string]float64{"tier0": 0.05, "tier1": 0.15}, "target": map[string]float64{"rate": 0.1, "alpha": 0.05},
			"cusum": map[string]float64{"p0": 0.05, "p1": 0.15, "h": 2.5},
		},
		"judge": map[string]any{"model": "claude-sonnet-5-5", "effort": effort, "prompt": "1", "start": judgeStart, "sets": [][]any{{}, {}, {}}},
	})
	require.NoError(t, err)
	at := start.Add(9 * time.Hour)
	return trace.Trace{ID: trace.NewID(at), Name: trace.NameKnob, Subject: start.Format(trace.CycleDate), Time: at, Input: input}
}

// The decide, expose and answer traces of the ask in the order curate and the hooks write them
func (a ask) traces(t *testing.T, i int) []trace.Trace {
	t.Helper()
	at := cycleAt(a.cycle).Add(24*time.Hour + time.Duration(i)*time.Minute)
	draw := func(unit string, p *float64, drawn bool) *trace.Draw {
		return &trace.Draw{
			CycleID: cycleAt(a.cycle).Format(trace.CycleDate), PolicyVersion: "0.10.0+1a2b3c4d", Unit: unit, Seed: "5eed", DrawID: "0123456789abcdef",
			P: p, Drawn: drawn,
		}
	}
	action := cmp.Or(a.action, map[string]string{"audit": "apply", "shadow": "shadow", "direction": "ask", "": "ask"}[a.question])
	decideID := trace.NewID(at)
	auditP := map[bool]float64{true: a.p, false: 0.15}[a.question == "audit"]
	decide := trace.Trace{
		ID: decideID, Name: trace.NameDecide, Ref: "run-1", Subject: a.kind, Time: at, Source: trace.SourcePush,
		Input:  mustJSON(t, map[string]any{"kind": a.kind, "id": "k-1", "version": 1, "anchor": 1}),
		Output: mustJSON(t, map[string]any{"tier": a.tier, "outcome": a.outcome, "bin": a.bin, "action": action, "confidence": 0.97}),
		Draw:   draw(decideID, &auditP, a.question == "audit"),
	}
	if a.question == "" {
		return []trace.Trace{decide}
	}
	shown := map[bool]*float64{true: &a.p, false: nil}[a.source == trace.SourcePush]
	exposeID := trace.NewID(at.Add(10 * time.Second))
	expose := trace.Trace{
		ID: exposeID, Name: trace.NameExpose, SessionID: "s1", Subject: a.question, Ref: decideID, Time: at.Add(10 * time.Second), Source: a.source,
		Input: mustJSON(t, map[string]any{"kind": a.question, "runes": 200}), Draw: draw("s1", shown, shown != nil),
	}
	authority := map[bool]string{true: "agent", false: "person"}[a.choice == "no_answer"]
	answer := trace.Trace{
		ID: trace.NewID(at.Add(20 * time.Second)), Name: trace.NameAnswer, Subject: a.choice, Ref: exposeID, Time: at.Add(20 * time.Second),
		Source: a.source, Draw: draw("s1", shown, shown != nil),
		Input: mustJSON(t, map[string]any{"choice": a.choice, "decide": decideID, "overturn": a.overturn, "authority": authority}),
	}
	return []trace.Trace{decide, expose, answer}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

// The curation report over records of cycles whose judge ran at the efforts and the asks in order
// The report is read on the last day of the last cycle with tier 1 active
func curation(t *testing.T, efforts []string, asks []ask) loop.CurationReport {
	t.Helper()
	judgeStart := cycleAt(0)
	var traces []trace.Trace
	for c, effort := range efforts {
		if c > 0 && effort != efforts[c-1] {
			judgeStart = cycleAt(c)
		}
		traces = append(traces, knobTrace(t, c, effort, judgeStart, true))
	}
	for i, a := range asks {
		traces = append(traces, a.traces(t, i)...)
	}
	return curationOf(t, traces, nil, cycleAt(len(efforts)-1).AddDate(0, 0, 13))
}

// The curation report over the traces and the knowledge records read at now
func curationOf(t *testing.T, traces []trace.Trace, items []knowledge.Knowledge, now time.Time) loop.CurationReport {
	t.Helper()
	ctx := context.Background()
	st := testkit.Open(t)
	for _, tr := range traces {
		require.NoError(t, st.Traces.Append(ctx, tr.Record()))
	}
	store, err := knowledgefile.New(st.Dir)
	require.NoError(t, err)
	for _, k := range items {
		require.NoError(t, store.Append(ctx, k.Record()))
	}
	stores := loop.Stores{Traces: st.Traces, Verdicts: st.Feedback, Outcomes: st.Outcomes, Items: st.Ledger}
	report, err := stores.Curation(ctx, now)
	require.NoError(t, err)
	return report
}

// The tier 2 estimate reads the direction answers about changes and none about a widening or the activation
// since those ask what curate may do and prepare no change the choice could overturn
func TestCurationEstimateOffers(t *testing.T) {
	direction := func(kind, choice string, overturn bool) ask {
		return ask{cycle: 0, tier: 2, kind: kind, question: "direction", p: 1, choice: choice, overturn: overturn, source: trace.SourcePush}
	}

	r := curation(t, []string{"high"}, []ask{
		direction("add", "drop", true), direction("add", "keep", false), direction("widen", "yes", false), direction("activate", "notyet", false),
	})

	require.Len(t, r.Estimates, 1)
	assert.Equal(t, []int{2, 2, 1}, []int{r.Estimates[0].Tier, r.Estimates[0].Answered, r.Estimates[0].Differ})
}

// The statistics of curation over the answers of the person
// 1. the anytime valid bound against values computed apart: 43 answers with no differ and 66 with one meet it and 42 or 65 with one do not
// 2. the Hájek estimate and the Kish size over two p values and the worst case over no answer
// 3. a manual answer left out of every estimate and the bound and counted as an overturn
// 4. the CUSUM of a fixed sequence alarming at its step and starting again
// 5. a changed effort a shift that starts the bound again and a shift of the same configuration that keeps the bound and starts the posterior of its bin again
func TestCuration(t *testing.T) {
	type args struct {
		efforts []string
		asks    []ask
	}
	// Of the row of add in b1, the tier 1 estimate of the last cycle, the overturns, the CUSUM and the shift of the last cycle
	type want struct {
		same, differ, noAnswer, posterior int
		e                                 float64
		met                               bool
		rate, kish, worst                 float64
		manual, overturned                int
		alarms                            []int
		shift, changed                    bool
	}
	high := []string{"high"}
	manual := ask{
		cycle: 0, tier: 1, kind: "add", bin: "b1", outcome: "conform", question: "direction", choice: "drop", overturn: true,
		source: trace.SourceManual,
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"43 answers with no differ meet the bound",
			args{high, repeated(43, audit(0, 0.15, "same"))},
			want{43, 0, 0, 43, 20.887535, true, 0, 43, 0, 0, 0, []int{}, false, false},
		},
		{
			"42 answers with no differ do not",
			args{high, repeated(42, audit(0, 0.15, "same"))},
			want{42, 0, 0, 42, 19.215032, false, 0, 42, 0, 0, 0, []int{}, false, false},
		},
		{
			"66 answers with one differ meet the bound",
			args{high, append(repeated(65, audit(0, 0.15, "same")), audit(0, 0.15, "undo"))},
			want{65, 1, 0, 66, 21.157022, true, 0.015152, 66, 0.015152, 0, 0, []int{}, false, false},
		},
		{
			"65 answers with one differ do not",
			args{high, append(repeated(64, audit(0, 0.15, "same")), audit(0, 0.15, "undo"))},
			want{64, 1, 0, 65, 19.61336, false, 0.015385, 65, 0.015385, 0, 0, []int{}, false, false},
		},
		{
			"two p values weigh the Hájek estimate and a no answer counts as differ only in the worst case",
			args{high, slices.Concat(
				[]ask{audit(0, 0.15, "undo")}, repeated(2, audit(0, 0.15, "same")), repeated(2, audit(0, 0.3, "same")), []ask{audit(0, 0.15, "no_answer")},
			)},
			want{4, 1, 1, 5, 0.580526, false, 0.25, 4.571429, 0.4, 0, 0, []int{}, false, false},
		},
		{
			"a manual answer is left out of every estimate and the bound and counted as an overturn",
			args{high, append(repeated(2, audit(0, 0.15, "same")), manual)},
			want{2, 0, 0, 2, 1.115226, false, 0, 2, 0, 1, 1, []int{}, false, false},
		},
		{
			"the CUSUM alarms when it reaches h and starts again from zero",
			args{high, []ask{
				audit(0, 0.15, "undo"), audit(0, 0.15, "same"), audit(0, 0.15, "undo"), audit(0, 0.15, "same"), audit(0, 0.15, "undo"), audit(0, 0.15, "undo"),
			}},
			want{2, 4, 0, 6, 0.207525, false, 0.666667, 6, 0.666667, 0, 0, []int{5}, false, false},
		},
		{
			"a question closed with no answer counts only in the worst case",
			args{high, []ask{audit(0, 0.15, "no_answer")}},
			want{0, 0, 1, 0, 1, false, -1, -1, 1, 0, 0, []int{}, false, false},
		},
		{
			"two cycles of one configuration whose judge found alike read no shift and keep the bound",
			args{[]string{"high", "high"}, slices.Concat(
				repeated(20, audit(0, 0.15, "same")), []ask{{cycle: 0, tier: 2, kind: "add", bin: "b2", outcome: "conform"}},
				repeated(23, audit(1, 0.15, "same")),
			)},
			want{43, 0, 0, 43, 20.887535, true, 0, 23, 0, 0, 0, []int{}, false, false},
		},
		{
			"a changed effort is a shift that starts the bound again",
			args{[]string{"high", "max"}, append(repeated(43, audit(0, 0.15, "same")), repeated(2, audit(1, 0.15, "same"))...)},
			want{2, 0, 0, 2, 1.115226, false, 0, 2, 0, 0, 0, []int{}, true, true},
		},
		{
			"a shift with the same configuration keeps the bound and starts the posterior of its bin again",
			args{[]string{"high", "high"}, slices.Concat(
				repeated(40, audit(0, 0.15, "same")), repeated(3, audit(1, 0.15, "same")),
				repeated(10, ask{cycle: 1, tier: 2, kind: "add", outcome: "split"}),
			)},
			want{43, 0, 0, 3, 20.887535, true, 0, 3, 0, 0, 0, []int{}, true, false},
		},
	}
	round := func(v *float64) float64 {
		if v == nil {
			return -1
		}
		return math.Round(*v*1e6) / 1e6
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := curation(t, tc.args.efforts, tc.args.asks)

			i := slices.IndexFunc(r.Kinds, func(k loop.KindRow) bool { return k.Kind == "add" && k.Bin == "b1" })
			require.GreaterOrEqual(t, i, 0)
			row := r.Kinds[i]
			require.NotEmpty(t, r.Estimates)
			estimate := r.Estimates[len(r.Estimates)-1]
			require.Equal(t, 1, estimate.Tier)
			// A report without a shift row reads as no shift
			shifts := append([]loop.ShiftRow{{}}, r.Shift...)
			shift := shifts[len(shifts)-1]
			alarms := []int{}
			for _, a := range r.CUSUM.Alarms {
				alarms = append(alarms, a.Step)
			}
			assert.Equal(t, tc.want, want{
				row.Same, row.Differ, row.NoAnswer, row.PosteriorAnswers, round(row.E), row.BoundMet,
				round(estimate.Rate), round(estimate.Kish), round(estimate.Worst), r.Overturns.Manual, r.Overturns.ManualOverturned,
				alarms, shift.Shift, shift.Changed,
			})
		})
	}
}

// The parts of the curation report the knobs read as numbers: the kinds, the shadow, the CUSUM and the newest closed cycle with its shift
type knobbed struct {
	kinds  []loop.KindRow
	shadow loop.ShadowReport
	cusum  loop.CUSUMReport
	closed string
	shift  loop.ShiftRow
}

func knobbedOf(r loop.CurationReport) knobbed {
	out := knobbed{kinds: r.Kinds, shadow: r.Shadow, cusum: r.CUSUM}
	for _, c := range r.Cycles {
		if c.Closed {
			out.closed = c.ID
		}
	}
	if i := slices.IndexFunc(r.Shift, func(s loop.ShiftRow) bool { return s.Cycle == out.closed }); i >= 0 {
		out.shift = r.Shift[i]
	}
	return out
}

// How the records of a knobbed fixture are written
type knobbedFixture struct {
	// The effort of the judge of each cycle
	efforts []string
	// A line that does not decode comes before every record
	corrupt bool
	// The knob traces name no start of their judge
	unstarted bool
	// The knob traces of an earlier release record no length of their cycle so they mark no cycle
	weekless bool
}

// The knob trace following the knob event of the id as curate links every knob event but the first
// An earlier release records no cycle length when weekless
func linked(t *testing.T, tr trace.Trace, before string, weekless bool) trace.Trace {
	t.Helper()
	var in map[string]any
	require.NoError(t, json.Unmarshal(tr.Input, &in))
	in["before"] = before
	if weekless {
		delete(in["knobs"].(map[string]any), "cycle_weeks")
	}
	tr.Input = mustJSON(t, in)
	return tr
}

// The records of cycles whose judge ran at the efforts with audits that alarm the CUSUM, a shadow and a direction answer
func knobbedStores(t *testing.T, f knobbedFixture) testkit.Stores {
	t.Helper()
	ctx := context.Background()
	st := testkit.Open(t)
	if f.corrupt {
		require.NoError(t, os.WriteFile(filepath.Join(st.Dir, "traces.jsonl"), []byte("{\n"), 0o600))
	}
	// A run of the time before curate
	earlier := cycleAt(0).Add(-24 * time.Hour)
	traces := trace.Traces{{ID: trace.NewID(earlier), Name: trace.NameRun, Producer: "session", Time: earlier, Input: json.RawMessage(`{"applied":[]}`)}}
	judgeStart, before := cycleAt(0), ""
	for c, effort := range f.efforts {
		if c > 0 && effort != f.efforts[c-1] {
			judgeStart = cycleAt(c)
		}
		knob := linked(t, knobTrace(t, c, effort, map[bool]time.Time{false: judgeStart}[f.unstarted], true), before, f.weekless)
		traces, before = append(traces, knob), knob.ID
	}
	asks := slices.Concat(
		repeated(3, audit(0, 0.15, "undo")), repeated(2, audit(1, 0.15, "same")), []ask{audit(1, 0.15, "undo")},
		repeated(3, audit(2, 0.15, "undo")), []ask{audit(3, 0.15, "same")},
		[]ask{{cycle: 2, tier: 1, kind: "add", outcome: "conform", question: "shadow", p: 0.5, choice: "keep", source: trace.SourcePush}},
		[]ask{{cycle: 3, tier: 2, kind: "add", bin: "b2", outcome: "conform", question: "direction", p: 1, choice: "keep", source: trace.SourcePush}},
	)
	for i, a := range asks {
		traces = append(traces, a.traces(t, i)...)
	}
	// The store reads backward and stops at the first trace older than a bound since every writer appends in time order
	slices.SortStableFunc(traces, func(a, b trace.Trace) int { return a.Time.Compare(b.Time) })
	for _, tr := range traces {
		require.NoError(t, st.Traces.Append(ctx, tr.Record()))
	}
	return st
}

// Knobbed and the full report share the same retained event history
func TestStoresKnobbed(t *testing.T) {
	tcs := []struct {
		name string
		args knobbedFixture
	}{
		{"one configuration over every cycle", knobbedFixture{efforts: []string{"high", "high", "high", "high"}}},
		{"a configuration of the third cycle", knobbedFixture{efforts: []string{"high", "high", "low", "low"}}},
		{"a configuration of the last cycle", knobbedFixture{efforts: []string{"high", "high", "high", "low"}}},
		{"one cycle", knobbedFixture{efforts: []string{"high"}}},
		{"a knob trace that names no start", knobbedFixture{efforts: []string{"high", "high"}, unstarted: true}},
		{"knob traces that mark no cycle", knobbedFixture{efforts: []string{"high", "high"}, weekless: true}},
		{"no knob trace", knobbedFixture{}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			st := knobbedStores(t, tc.args)
			stores := loop.Stores{Traces: st.Traces, Verdicts: st.Feedback, Outcomes: st.Outcomes, Items: st.Ledger}
			now := cycleAt(len(tc.args.efforts)).AddDate(0, 0, 1)

			whole, wholeErr := stores.Curation(ctx, now)
			got, err := stores.Knobbed(ctx, now)

			require.NoError(t, wholeErr)
			require.NoError(t, err)
			assert.Equal(t, knobbedOf(whole), knobbedOf(got))
		})
	}
}

func TestStoresKnobbedRefusesCorruptHistory(t *testing.T) {
	tcs := []struct {
		name string
		args []string
	}{
		{"a configuration of the third cycle", []string{"high", "high", "low", "low"}},
		{"one configuration over every cycle", []string{"high", "high", "high", "high"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			st := knobbedStores(t, knobbedFixture{efforts: tc.args, corrupt: true})
			stores := loop.Stores{Traces: st.Traces, Verdicts: st.Feedback, Outcomes: st.Outcomes, Items: st.Ledger}
			now := cycleAt(4).AddDate(0, 0, 1)

			_, wholeErr := stores.Curation(ctx, now)
			_, err := stores.Knobbed(ctx, now)

			assert.Error(t, wholeErr)
			assert.ErrorIs(t, err, tracefile.ErrCorrupt)
		})
	}
}

func TestStoresKnobbedFails(t *testing.T) {
	tcs := []struct {
		name string
		args int
	}{
		{"the retained events", 0},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := knobbedStores(t, knobbedFixture{efforts: []string{"high", "high", "low", "low"}})
			traces := &testkit.FlakyTraces{Store: st.Traces, Lists: testkit.Reads{Allowed: tc.args, Err: assert.AnError}}
			stores := loop.Stores{Traces: traces, Verdicts: st.Feedback, Outcomes: st.Outcomes, Items: st.Ledger}

			_, err := stores.Knobbed(context.Background(), cycleAt(4).AddDate(0, 0, 1))

			assert.ErrorIs(t, err, assert.AnError)
		})
	}
}
