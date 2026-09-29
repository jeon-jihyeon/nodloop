package eval_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/eval"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// feedback:on answers tq-003 right and tq-004 wrong while knowledge:on answers both right
// Every review of repeat r applies r knowledge ids the labels do not expect
func TestReportPairs(t *testing.T) {
	type args struct {
		// The tags that mark each repeat
		// A single run carries none
		repeats [][]string
	}
	type want struct {
		pairs     int
		stability []eval.Stability
	}
	agreed := []eval.EventAgreement{{EventID: "tq-003", Runs: 3, Agreement: 1}, {EventID: "tq-004", Runs: 3, Agreement: 1}}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"one run", args{repeats: [][]string{nil}}, want{pairs: 2}},
		{
			"repetition preserves event clusters",
			args{repeats: [][]string{{"repeat:1"}, {"repeat:2"}, {"repeat:3"}}},
			want{pairs: 6, stability: []eval.Stability{
				{
					Condition: eval.ConditionExamples, Runs: 3,
					StatusAccuracyMean: 0.5, StatusAccuracyMin: 0.5, StatusAccuracyMax: 0.5,
					MisappliedMean: 4, MisappliedMin: 2, MisappliedMax: 6, Events: agreed,
				},
				{
					Condition: eval.ConditionKnowledge, Runs: 3,
					StatusAccuracyMean: 1, StatusAccuracyMin: 1, StatusAccuracyMax: 1,
					MisappliedMean: 4, MisappliedMin: 2, MisappliedMax: 6, Events: agreed,
				},
			}},
		},
	}
	ctx := context.Background()
	statuses := map[eval.Condition][2]string{
		eval.ConditionExamples: {"no_action", "hold"}, eval.ConditionKnowledge: {"no_action", "no_action"},
	}
	interval := eval.Interval{
		Reference: eval.ConditionExamples, Condition: eval.ConditionKnowledge, Events: 2,
		Confidence: 0.95, Resamples: 10000, Method: "paired event-cluster percentile bootstrap",
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := testkit.Open(t)
			for repeat, tags := range tc.args.repeats {
				items := []map[string]any{}
				for n := range repeat + 1 {
					items = append(items, map[string]any{"id": fmt.Sprintf("k%d", n), "version": 1, "chars": 10})
				}
				input, err := json.Marshal(map[string]any{"knowledge": items})
				require.NoError(t, err)
				for condition, answers := range statuses {
					for event, status := range answers {
						require.NoError(t, s.Traces.Append(ctx, trace.Trace{
							ID: fmt.Sprintf("%d-%s-%d", repeat, condition, event), SessionID: "pairs", Name: trace.NameDiagnose,
							Subject: fmt.Sprintf("tq-%03d", event+3), Input: input,
							Tags:   append([]string{string(condition)}, tags...),
							Output: json.RawMessage(fmt.Sprintf(`{"status":%q}`, status)),
						}))
					}
				}
			}
			r := eval.New(s.Source, nil, s.Traces, s.Feedback, s.Ledger)
			report, err := r.Report(ctx, "pairs")
			require.NoError(t, err)
			assert.Equal(t, []eval.Pair{{
				Reference: eval.ConditionExamples, Condition: eval.ConditionKnowledge, Events: 2, Fixed: 1, McNemarP: 1,
			}}, report.Pairs)
			accuracy, misapplied := interval, interval
			accuracy.Metric, accuracy.Pairs, accuracy.Difference, accuracy.Upper = eval.MetricStatusAccuracy, tc.want.pairs, 0.5, 1
			misapplied.Metric, misapplied.Pairs = eval.MetricMisapplied, tc.want.pairs
			assert.Equal(t, []eval.Interval{accuracy, misapplied}, report.Intervals)
			assert.Equal(t, tc.want.stability, report.Stability)
			again, err := r.Report(ctx, "pairs")
			require.NoError(t, err)
			assert.Equal(t, report.Intervals, again.Intervals)
			assert.Contains(t, report.Table(), "paired events")
		})
	}
}
