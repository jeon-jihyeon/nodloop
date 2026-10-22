package loop_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/loop"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

func TestCurationDelegated(t *testing.T) {
	tcs := []struct {
		name string
		args []ask
		want []loop.DelegatedRow
	}{
		{"no delegation", nil, []loop.DelegatedRow{}},
		{"an audit reversal stays outside judge statistics", []ask{audit(0, 0.15, "undo")},
			[]loop.DelegatedRow{{Grant: "grant-a", Decided: 1, ApplyDecisions: 1, Drawn: 1, Exposed: 1, Sessions: 1, Answers: 1, Overturned: 1}}},
		{"a kept audit stays outside judge statistics", []ask{audit(0, 0.15, "keep")},
			[]loop.DelegatedRow{{Grant: "grant-a", Decided: 1, ApplyDecisions: 1, Drawn: 1, Exposed: 1, Sessions: 1, Answers: 1}}},
		{"an unanswered audit stays outside judge statistics", []ask{audit(0, 0.15, "no_answer")},
			[]loop.DelegatedRow{{Grant: "grant-a", Decided: 1, ApplyDecisions: 1, Drawn: 1, Exposed: 1, Sessions: 1, NoAnswer: 1}}},
		{"independent grants retain separate counts", []ask{audit(0, 0.15, "undo"), audit(1, 0.15, "keep")},
			[]loop.DelegatedRow{
				{Grant: "grant-a", Decided: 1, ApplyDecisions: 1, Drawn: 1, Exposed: 1, Sessions: 1, Answers: 1, Overturned: 1},
				{Grant: "grant-b", Decided: 1, ApplyDecisions: 1, Drawn: 1, Exposed: 1, Sessions: 1, Answers: 1},
			}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			traces := []trace.Trace{knobTrace(t, 0, "high", cycleAt(0), true), knobTrace(t, 1, "high", cycleAt(0), true)}
			traces = append(traces, audit(0, 0.15, "keep").traces(t, 0)...)
			traces = append(traces, audit(1, 0.15, "keep").traces(t, 1)...)
			baseline := curationOf(t, traces, nil, cycleAt(2))
			for i, a := range tc.args {
				added := a.traces(t, i+100)
				grant := []string{"grant-a", "grant-b"}[i]
				added[0].Output = mustJSON(t, map[string]any{
					"tier": 1, "rule_code": "delegated", "action": "apply", "delegation": grant,
					"outcome": "conform", "bin": "b1", "confidence": 0.99,
				})
				traces = append(traces, added...)
			}

			got := curationOf(t, traces, nil, cycleAt(2))

			assert.Equal(t, tc.want, got.Delegated)
			assert.Equal(t, baseline.Kinds, got.Kinds)
			assert.Equal(t, baseline.Calibration, got.Calibration)
			assert.Equal(t, baseline.Estimates, got.Estimates)
			assert.Equal(t, baseline.Shadow, got.Shadow)
			assert.Equal(t, baseline.CUSUM, got.CUSUM)
			assert.Equal(t, baseline.Shift, got.Shift)
		})
	}
}

func TestCurationDelegatedStopped(t *testing.T) {
	tcs := []struct {
		name string
		args []string
		want []bool
	}{
		{"no grant stopped", nil, []bool{false, false}},
		{"the stopped grant alone", []string{"grant-a"}, []bool{true, false}},
		{"a stopped grant with no decide adds no row", []string{"grant-c"}, []bool{false, false}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			knob := knobTrace(t, 0, "high", cycleAt(0), true)
			var in map[string]any
			require.NoError(t, json.Unmarshal(knob.Input, &in))
			in["knobs"].(map[string]any)["delegation_paused"] = tc.args
			knob.Input = mustJSON(t, in)
			traces := []trace.Trace{knob}
			for i, grant := range []string{"grant-a", "grant-b"} {
				added := audit(0, 0.15, "keep").traces(t, i)
				added[0].Output = mustJSON(t, map[string]any{"tier": 1, "rule_code": "delegated", "action": "apply", "delegation": grant})
				traces = append(traces, added...)
			}

			got := curationOf(t, traces, nil, cycleAt(1))

			require.Len(t, got.Delegated, 2)
			assert.Equal(t, tc.want, []bool{got.Delegated[0].Stopped, got.Delegated[1].Stopped})
		})
	}
}

func TestKnobbedLeavesDelegatedOut(t *testing.T) {
	tcs := []struct {
		name string
		args string
	}{
		{"a kept delegated audit", "keep"},
		{"an undone delegated audit", "undo"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			st := testkit.Open(t)
			traces := []trace.Trace{knobTrace(t, 0, "high", cycleAt(0), true)}
			traces = append(traces, audit(0, 0.15, "keep").traces(t, 0)...)
			for _, tr := range traces {
				require.NoError(t, st.Traces.Append(ctx, tr.Record()))
			}
			baseline, err := (loop.Stores{Traces: st.Traces}).Knobbed(ctx, cycleAt(0).Add(48*time.Hour))
			require.NoError(t, err)
			added := audit(0, 0.15, tc.args).traces(t, 1)
			added[0].Output = mustJSON(t, map[string]any{"tier": 1, "rule_code": "delegated", "action": "apply", "delegation": "grant-a"})
			for _, tr := range added {
				require.NoError(t, st.Traces.Append(ctx, tr.Record()))
			}

			got, err := (loop.Stores{Traces: st.Traces}).Knobbed(ctx, cycleAt(0).Add(48*time.Hour))

			require.NoError(t, err)
			assert.Equal(t, baseline, got)
		})
	}
}

func TestCurationDelegatedTouchpoints(t *testing.T) {
	tcs := []struct {
		name string
		args string
		want loop.DelegatedRow
	}{
		{"a place notice is not a grant question", "notice", loop.DelegatedRow{Grant: "grant-a", Decided: 1, ApplyDecisions: 1}},
		{"manual reversals remain separate from drawn audits", "manual", loop.DelegatedRow{Grant: "grant-a", Decided: 1, ApplyDecisions: 1, Exposed: 1, Sessions: 1, Manual: 1, ManualOverturned: 1}},
		{"a missing grant cannot stop any grant", "missing", loop.DelegatedRow{}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := audit(0, 0.15, "undo")
			if tc.args == "notice" {
				a.question = "notice"
			}
			if tc.args == "manual" {
				a.source = trace.SourceManual
			}
			traces := a.traces(t, 0)
			if tc.args == "notice" {
				traces[1].Ref = ""
				traces = traces[:2]
			}
			grant := "grant-a"
			if tc.args == "missing" {
				grant = ""
			}
			traces[0].Output = mustJSON(t, map[string]any{"tier": 1, "rule_code": "delegated", "action": "apply", "delegation": grant})
			if tc.args == "manual" {
				traces[0].Draw.Drawn = false
			}
			traces = append(traces, knobTrace(t, 0, "high", cycleAt(0), true))

			got := curationOf(t, traces, nil, cycleAt(1))

			want := []loop.DelegatedRow{tc.want}
			if tc.args == "missing" {
				want = []loop.DelegatedRow{}
			}
			assert.Equal(t, want, got.Delegated)
			if tc.args == "notice" {
				require.NotEmpty(t, got.Touchpoints)
				for _, row := range got.Touchpoints {
					assert.Equal(t, 1, row.Notices)
				}
			}
		})
	}
}

func TestCurationDelegatedRepeated(t *testing.T) {
	tcs := []struct {
		name string
		args trace.Source
		want loop.DelegatedRow
	}{
		{"pushed answers keep the latest verdict and every reversal", trace.SourcePush,
			loop.DelegatedRow{Grant: "grant-a", Decided: 2, ApplyDecisions: 2, Drawn: 2, Exposed: 3, Sessions: 1, Answers: 2}},
		{"manual answers keep the latest verdict and every reversal", trace.SourceManual,
			loop.DelegatedRow{Grant: "grant-a", Decided: 2, ApplyDecisions: 2, Exposed: 3, Sessions: 1, Manual: 2}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			traces := []trace.Trace{knobTrace(t, 0, "high", cycleAt(0), true)}
			for i, choice := range []string{"undo", "keep"} {
				a := audit(0, 0.15, choice)
				a.source = tc.args
				added := a.traces(t, i)
				added[0].Output = mustJSON(t, map[string]any{"tier": 1, "rule_code": "delegated", "action": "apply", "delegation": "grant-a"})
				if tc.args == trace.SourceManual {
					added[0].Draw.Drawn = false
				}
				traces = append(traces, added...)
				if i == 0 {
					x, a := added[1], added[2]
					x.ID, a.ID, a.Ref = "shown-again", "answered-again", "shown-again"
					x.Time, a.Time = x.Time.Add(time.Hour), a.Time.Add(time.Hour)
					a.Input = mustJSON(t, map[string]any{"choice": "keep", "overturn": false, "authority": "person"})
					traces = append(traces, x, a)
				}
			}

			got := curationOf(t, traces, nil, cycleAt(1))

			assert.Equal(t, []loop.DelegatedRow{tc.want}, got.Delegated)
		})
	}
}
