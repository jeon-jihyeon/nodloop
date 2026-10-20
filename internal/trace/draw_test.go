package trace_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// One seed and unit always draw alike and the draw id names the hash the draw read
func TestNewDraw(t *testing.T) {
	type args struct {
		unit string
		p    float64
	}
	type want struct {
		id    string
		drawn bool
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a unit below p is drawn", args{"d1", 0.15}, want{"1d87ed65a2835e3c", true}},
		{"the same unit above p is not", args{"d1", 0.1}, want{"1d87ed65a2835e3c", false}},
		{"p 1 always draws", args{"d2", 1}, want{"5e2e4f12adbcf22e", true}},
		{"p 0 never draws", args{"d2", 0}, want{"5e2e4f12adbcf22e", false}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := trace.NewDraw("2026-09-21", "0.10.0+1a2b3c4d", "seed", tc.args.unit, "audit", tc.args.p)

			assert.Equal(t, tc.want, want{got.DrawID, got.Drawn})
			assert.Equal(t, tc.args.p, *got.P)
			assert.Equal(t, []string{"2026-09-21", "0.10.0+1a2b3c4d", tc.args.unit, "seed"}, []string{got.CycleID, got.PolicyVersion, got.Unit, got.Seed})
		})
	}
}

// A share of 0.15 over 1000 units draws about 150 of them
func TestNewDrawShare(t *testing.T) {
	drawn := 0
	for i := range 1000 {
		if trace.NewDraw("2026-09-21", "v", "seed", fmt.Sprintf("d%d", i), "audit", 0.15).Drawn {
			drawn++
		}
	}

	assert.InDelta(t, 150, drawn, 30)
}

func TestDrawCheck(t *testing.T) {
	pushed := trace.NewDraw("2026-09-21", "0.10.0+1a2b3c4d", "seed", "d1", "audit", 0.15)
	with := func(change func(d *trace.Draw)) trace.Draw {
		d := pushed
		p := *d.P
		d.P = &p
		change(&d)
		return d
	}
	manual := trace.Draw{CycleID: "2026-09-21", PolicyVersion: "0.10.0+1a2b3c4d"}
	type args struct {
		draw   trace.Draw
		source trace.Source
	}
	tcs := []struct {
		name string
		args args
		want error
	}{
		{"a pushed decision with its draw passes", args{pushed, trace.SourcePush}, nil},
		{"a manual answer without a probability passes", args{manual, trace.SourceManual}, nil},
		{"a command without a probability passes", args{manual, trace.SourceCommand}, nil},
		{"an unknown source is refused", args{pushed, "model"}, trace.ErrDrawInvalid},
		{"a cycle that is no Monday is refused", args{with(func(d *trace.Draw) { d.CycleID = "2026-09-22" }), trace.SourcePush}, trace.ErrDrawInvalid},
		{"a cycle that is no date is refused", args{with(func(d *trace.Draw) { d.CycleID = "week 39" }), trace.SourcePush}, trace.ErrDrawInvalid},
		{"no policy version is refused", args{with(func(d *trace.Draw) { d.PolicyVersion = "" }), trace.SourcePush}, trace.ErrDrawInvalid},
		{"a manual record with a probability is refused", args{pushed, trace.SourceManual}, trace.ErrDrawInvalid},
		{"a manual record marked drawn is refused", args{trace.Draw{CycleID: "2026-09-21", PolicyVersion: "v", Drawn: true}, trace.SourceManual}, trace.ErrDrawInvalid},
		{"a pushed record without a probability is refused", args{manual, trace.SourcePush}, trace.ErrDrawInvalid},
		{"a probability above 1 is refused", args{with(func(d *trace.Draw) { *d.P = 1.5 }), trace.SourcePush}, trace.ErrDrawInvalid},
		{"a probability below 0 is refused", args{with(func(d *trace.Draw) { *d.P = -0.1 }), trace.SourcePush}, trace.ErrDrawInvalid},
		{"a draw at p 0 is refused", args{with(func(d *trace.Draw) { *d.P, d.Drawn = 0, true }), trace.SourcePush}, trace.ErrDrawInvalid},
		{"a pushed record without a unit is refused", args{with(func(d *trace.Draw) { d.Unit = "" }), trace.SourcePush}, trace.ErrDrawInvalid},
		{"a draw id of another shape is refused", args{with(func(d *trace.Draw) { d.DrawID = "1D87ED65A2835E3C" }), trace.SourcePush}, trace.ErrDrawInvalid},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.ErrorIs(t, tc.args.draw.Check(tc.args.source), tc.want)
		})
	}
}

// A decision with its envelope and a manual answer without a probability read back to the same bytes
func TestDrawRoundTrip(t *testing.T) {
	at := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	pushed := trace.NewDraw("2026-09-21", "0.10.0+1a2b3c4d", "seed", "d1", "audit", 0.15)
	manual := trace.Draw{CycleID: "2026-09-21", PolicyVersion: "0.10.0+1a2b3c4d"}
	tcs := []struct {
		name string
		args trace.Trace
		want string
	}{
		{
			"a decision keeps its draw",
			trace.Trace{ID: "d1", Name: "decide", Time: at, Input: json.RawMessage(`{}`), Draw: &pushed, Source: trace.SourcePush},
			`"draw":{"cycle_id":"2026-09-21","policy_version":"0.10.0+1a2b3c4d","unit":"d1","seed":"seed","draw_id":"1d87ed65a2835e3c",` +
				`"p":0.15,"drawn":true},"source":"push"`,
		},
		{
			"a manual answer has no probability",
			trace.Trace{ID: "a1", Name: "answer", Time: at, Input: json.RawMessage(`{}`), Draw: &manual, Source: trace.SourceManual},
			`"draw":{"cycle_id":"2026-09-21","policy_version":"0.10.0+1a2b3c4d","drawn":false},"source":"manual"`,
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b, err := json.Marshal(tc.args.Record())
			require.NoError(t, err)
			var back trace.Trace
			require.NoError(t, json.Unmarshal(b, &back))
			again, err := json.Marshal(back.Record())
			require.NoError(t, err)

			assert.Contains(t, string(b), tc.want)
			assert.Equal(t, string(b), string(again))
		})
	}
}
