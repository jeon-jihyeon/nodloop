package loop_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/loop"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// Every report name answers the value of its report and an unknown name is refused before any read
func TestStoresReport(t *testing.T) {
	st := testkit.Open(t)
	ctx := context.Background()
	run, err := trace.NewRun("session", "", trace.Labels{"repo": {"nodloop"}}, []byte(`{"applied":[]}`), []byte("answer"), st.Clock.Now())
	require.NoError(t, err)
	require.NoError(t, st.Traces.Append(ctx, run))
	stores := loop.Stores{Traces: st.Traces, Verdicts: st.Feedback, Outcomes: st.Outcomes, Items: st.Ledger}
	tcs := []struct {
		name string
		args loop.ReportName
		want any
	}{
		{"loop holds the totals", loop.ReportLoop, loop.LoopReport{Totals: loop.Totals{Runs: 1}, Scopes: []loop.ScopeRow{}, Drafts: []loop.DraftRow{}, Items: []loop.RunItem{}}},
		{"extract has no row without an extraction", loop.ReportExtract, []loop.ExtractRow{}},
		{"critic has no row without a critic", loop.ReportCritic, []loop.CriticRow{}},
		{"effect has a row per arm without runs that received items", loop.ReportEffect, []loop.EffectRow{{Arm: "applied"}, {Arm: "withheld"}}},
		{"health has no row without knowledge", loop.ReportHealth, []loop.Health{}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := stores.Report(ctx, tc.args, st.Clock.Now())
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
	_, err = stores.Report(ctx, "nope", st.Clock.Now())
	assert.ErrorIs(t, err, loop.ErrReportUnknown)
}
