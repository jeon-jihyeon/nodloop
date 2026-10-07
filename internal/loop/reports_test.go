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

func TestReplays(t *testing.T) {
	at := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	replayed := func(minute int, output string) trace.Trace {
		return trace.Trace{Name: trace.NameReplay, Time: at.Add(time.Duration(minute) * time.Minute), Output: json.RawMessage(output)}
	}
	failed := `{"id":"a","version":1,"cases":[{"expect":"breaks"},{"expect":"keeps"},{"expect":"keeps"}],"missed":0,"overreach":1}`
	passed := `{"id":"a","version":1,"cases":[{"expect":"breaks"}],"missed":0,"overreach":0}`
	tcs := []struct {
		name string
		args trace.Traces
		want []loop.ReplayRow
	}{
		{"no replay has no row", trace.Traces{{Name: trace.NameRun}}, []loop.ReplayRow{}},
		{
			"the newest replay of a version wins",
			trace.Traces{replayed(2, passed), replayed(1, failed)},
			[]loop.ReplayRow{{ID: "a", Version: 1, Passed: true, Corrected: 1, Time: at.Add(2 * time.Minute)}},
		},
		{
			"cases count by what they expect",
			trace.Traces{replayed(1, failed)},
			[]loop.ReplayRow{{ID: "a", Version: 1, Corrected: 1, Approved: 2, Overreach: 1, Time: at.Add(time.Minute)}},
		},
		{"an output that does not decode is left out", trace.Traces{replayed(1, `[`)}, []loop.ReplayRow{}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, loop.Replays(tc.args))
		})
	}
}
