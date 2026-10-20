package trace_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// A knob trace of the cycle that starts at the date and lasts the weeks recorded at the time
func knob(id, cycle string, weeks int, at time.Time) trace.Trace {
	start, _ := time.Parse(trace.CycleDate, cycle)
	input := fmt.Sprintf(`{"cycle_id":%q,"start":%q,"knobs":{"cycle_weeks":%d}}`, cycle, start.Format(time.RFC3339), weeks)
	return trace.Trace{ID: id, Name: trace.NameKnob, Time: at, Input: json.RawMessage(input)}
}

func TestTracesCycles(t *testing.T) {
	day := func(d string) time.Time {
		at, _ := time.Parse(trace.CycleDate, d)
		return at.Add(9 * time.Hour)
	}
	type args struct {
		traces trace.Traces
		now    time.Time
	}
	tcs := []struct {
		name string
		args args
		want []string
	}{
		{"no knob trace marks no cycle", args{trace.Traces{{ID: "r1", Name: trace.NameRun, Time: day("2026-09-01")}}, day("2026-09-22")}, nil},
		{
			"a cycle closes by its length and the next one follows with the same length",
			args{trace.Traces{knob("k1", "2026-08-31", 2, day("2026-08-31"))}, day("2026-09-22")},
			[]string{"2026-08-31 2026-08-31 2026-09-14", "2026-09-14 2026-09-14 2026-09-28"},
		},
		{
			"a later knob trace starts a cycle of its own length",
			args{trace.Traces{knob("k2", "2026-09-14", 1, day("2026-09-15")), knob("k1", "2026-08-31", 2, day("2026-08-31"))}, day("2026-09-22")},
			[]string{"2026-08-31 2026-08-31 2026-09-14", "2026-09-14 2026-09-14 2026-09-21", "2026-09-21 2026-09-21 2026-09-28"},
		},
		{
			"a knob trace that starts early cuts the cycle before it",
			args{trace.Traces{knob("k2", "2026-09-07", 2, day("2026-09-07")), knob("k1", "2026-08-31", 2, day("2026-08-31"))}, day("2026-09-08")},
			[]string{"2026-08-31 2026-08-31 2026-09-07", "2026-09-07 2026-09-07 2026-09-21"},
		},
		{
			"a narrowing inside a cycle keeps the start and the length of its first knob trace",
			args{trace.Traces{knob("k2", "2026-08-31", 4, day("2026-09-03")), knob("k1", "2026-08-31", 2, day("2026-08-31"))}, day("2026-09-08")},
			[]string{"2026-08-31 2026-08-31 2026-09-14"},
		},
		{
			"the first knob trace of a cycle wins whatever the order of the list",
			args{trace.Traces{knob("k1", "2026-08-31", 2, day("2026-08-31")), knob("k2", "2026-08-31", 4, day("2026-09-03"))}, day("2026-09-08")},
			[]string{"2026-08-31 2026-08-31 2026-09-14"},
		},
		{
			"a knob trace that does not decode or has no start or no length marks nothing",
			args{trace.Traces{
				{ID: "k4", Name: trace.NameKnob, Time: day("2026-08-17"), Input: json.RawMessage(`{"cycle_id":"2026-08-17","knobs":{"cycle_weeks":1}}`)},
				{ID: "k3", Name: trace.NameKnob, Time: day("2026-08-31"), Input: json.RawMessage(`not json`)},
				knob("k2", "2026-08-24", 0, day("2026-08-24")),
				knob("k1", "2026-08-31", 1, day("2026-08-31")),
			}, day("2026-09-01")},
			[]string{"2026-08-31 2026-08-31 2026-09-07"},
		},
		{
			"a cycle that starts after now is the only one",
			args{trace.Traces{knob("k1", "2026-09-28", 2, day("2026-09-28"))}, day("2026-09-22")},
			[]string{"2026-09-28 2026-09-28 2026-10-12"},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var got []string
			for _, c := range tc.args.traces.Cycles(tc.args.now) {
				got = append(got, c.ID+" "+c.Start.Format(trace.CycleDate)+" "+c.End.Format(trace.CycleDate))
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

// The open cycle is the first that has not ended
func TestCyclesOpen(t *testing.T) {
	start := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	cycles := trace.Traces{knob("k1", "2026-08-31", 2, start)}.Cycles(start.Add(20 * 24 * time.Hour))
	type want struct {
		id   string
		open bool
	}
	tcs := []struct {
		name string
		args time.Time
		want want
	}{
		{"now inside the first cycle", start.Add(time.Hour), want{"2026-08-31", true}},
		{"now at the end of the first cycle opens the next", start.Add(14 * 24 * time.Hour), want{"2026-09-14", true}},
		{"now past every cycle opens none", start.Add(60 * 24 * time.Hour), want{"", false}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, ok := cycles.Open(tc.args)
			assert.Equal(t, tc.want, want{c.ID, ok})
		})
	}
}

// A time lies in the cycle from its start up to its end
func TestCycleHolds(t *testing.T) {
	start := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	cycle := trace.Cycle{ID: "2026-08-31", Start: start, End: start.Add(14 * 24 * time.Hour)}
	tcs := []struct {
		name string
		args time.Time
		want bool
	}{
		{"the start lies in the cycle", start, true},
		{"a second before the start does not", start.Add(-time.Second), false},
		{"the end belongs to the next cycle", cycle.End, false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, cycle.Holds(tc.args))
		})
	}
}

// The first cycle stands however far the cycles run on and there is none before the first knob trace
func TestTracesFirstCycle(t *testing.T) {
	start := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	type want struct {
		cycle trace.Cycle
		ok    bool
	}
	tcs := []struct {
		name string
		args trace.Traces
		want want
	}{
		{"no knob trace has no first cycle", trace.Traces{{ID: "r1", Name: trace.NameRun, Time: start}}, want{}},
		{
			"the first of two knob traces",
			trace.Traces{knob("k2", "2026-09-28", 1, start.Add(28*24*time.Hour)), knob("k1", "2026-08-31", 2, start)},
			want{trace.Cycle{ID: "2026-08-31", Start: start, End: start.Add(14 * 24 * time.Hour)}, true},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, ok := tc.args.FirstCycle()
			assert.Equal(t, tc.want, want{c, ok})
		})
	}
}
