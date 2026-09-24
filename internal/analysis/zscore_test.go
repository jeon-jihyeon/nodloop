package analysis_test

import (
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/jeon-jihyeon/nodloop/internal/analysis"
	"github.com/jeon-jihyeon/nodloop/internal/evidence"
)

func TestAnalyzeZScore(t *testing.T) {
	t0 := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	hourly := func(metric string, dims map[string]string, values []float64) []evidence.Point {
		points := make([]evidence.Point, len(values))
		for i, v := range values {
			points[i] = evidence.Point{Time: t0.Add(time.Duration(i) * time.Hour), Metric: metric, Value: v, Dims: dims}
		}
		return points
	}
	// Alternating 100 and 110 so the baseline mean is 105 and the population stddev 5
	wave := slices.Repeat([]float64{100, 110}, 12)
	flat := slices.Repeat([]float64{100}, 5)
	policy := analysis.Policy{Version: "t", Analyzers: []analysis.RuleSpec{
		{Rule: analysis.RuleZScore, Metrics: []string{"click_count"}, Baseline: 24, Window: 6, Threshold: 3, MinSamples: 12},
	}}
	a := map[string]string{"source": "a"}
	b := map[string]string{"source": "b"}
	window := analysis.Window{Start: t0.Add(24 * time.Hour), End: t0.Add(29 * time.Hour), Points: 6}
	ref := analysis.Ref{EventID: "e1", Start: window.Start, End: window.End}
	tcs := []struct {
		name string
		args []evidence.Point
		want analysis.Observations
	}{
		{
			name: "spike in the window is detected",
			args: hourly("click_count", a, slices.Concat(wave, flat, []float64{200})),
			want: analysis.Observations{{
				Rule: analysis.RuleZScore, Target: a, Metric: "click_count", Window: window,
				Current: 700.0 / 6, Baseline: 105, Change: 19, Severity: 1, Adequate: true,
				Detail:  analysis.Detail{PeakTime: window.End, PeakValue: 200, Samples: 30},
				Ref:     ref,
				Summary: "click_count source=a: window mean 116.6667 against baseline 105, peak z 19 at 2026-09-23T05:00:00Z",
			}},
		},
		{
			name: "drop reports a negative z",
			args: hourly("click_count", a, slices.Concat(wave, slices.Repeat([]float64{105}, 5), []float64{80})),
			want: analysis.Observations{{
				Rule: analysis.RuleZScore, Target: a, Metric: "click_count", Window: window,
				Current: 605.0 / 6, Baseline: 105, Change: -5, Severity: 5.0 / 6, Adequate: true,
				Detail:  analysis.Detail{PeakTime: window.End, PeakValue: 80, Samples: 30},
				Ref:     ref,
				Summary: "click_count source=a: window mean 100.8333 against baseline 105, peak z -5 at 2026-09-23T05:00:00Z",
			}},
		},
		{
			name: "normal variation yields nothing",
			args: hourly("click_count", a, slices.Concat(wave, wave[:6])),
			want: nil,
		},
		{
			name: "only the spiking series is reported",
			args: slices.Concat(
				hourly("click_count", a, slices.Concat(wave, wave[:6])),
				hourly("click_count", b, slices.Concat(wave, flat, []float64{200})),
			),
			want: analysis.Observations{{
				Rule: analysis.RuleZScore, Target: b, Metric: "click_count", Window: window,
				Current: 700.0 / 6, Baseline: 105, Change: 19, Severity: 1, Adequate: true,
				Detail:  analysis.Detail{PeakTime: window.End, PeakValue: 200, Samples: 30},
				Ref:     ref,
				Summary: "click_count source=b: window mean 116.6667 against baseline 105, peak z 19 at 2026-09-23T05:00:00Z",
			}},
		},
		{
			name: "short series is inadequate",
			args: hourly("click_count", a, []float64{1, 2, 3, 4, 5, 6, 7, 8}),
			want: analysis.Observations{{
				Rule: analysis.RuleZScore, Target: a, Metric: "click_count",
				Window:  analysis.Window{Start: t0.Add(2 * time.Hour), End: t0.Add(7 * time.Hour), Points: 6},
				Current: 5.5, Baseline: 1.5, Detail: analysis.Detail{Samples: 8},
				Ref:     analysis.Ref{EventID: "e1", Start: t0.Add(2 * time.Hour), End: t0.Add(7 * time.Hour)},
				Summary: "click_count source=a: 2 baseline points, fewer than 12 required",
			}},
		},
		{
			name: "series long enough overall is inadequate when its baseline is short",
			args: hourly("click_count", a, slices.Concat(wave[:9], flat, []float64{200})),
			want: analysis.Observations{{
				Rule: analysis.RuleZScore, Target: a, Metric: "click_count",
				Window:  analysis.Window{Start: t0.Add(9 * time.Hour), End: t0.Add(14 * time.Hour), Points: 6},
				Current: 700.0 / 6, Baseline: 940.0 / 9, Detail: analysis.Detail{Samples: 15},
				Ref:     analysis.Ref{EventID: "e1", Start: t0.Add(9 * time.Hour), End: t0.Add(14 * time.Hour)},
				Summary: "click_count source=a: 9 baseline points, fewer than 12 required",
			}},
		},
		{
			name: "flat baseline is inadequate",
			args: hourly("click_count", a, slices.Concat(slices.Repeat([]float64{100}, 24), flat, []float64{200})),
			want: analysis.Observations{{
				Rule: analysis.RuleZScore, Target: a, Metric: "click_count", Window: window, Current: 700.0 / 6, Baseline: 100,
				Detail: analysis.Detail{Samples: 30}, Ref: ref, Summary: "click_count source=a: baseline stddev is zero",
			}},
		},
		{
			name: "missing metric yields nothing",
			args: hourly("conversion_count", a, slices.Concat(wave, flat, []float64{200})),
			want: nil,
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := policy.Analyze(evidence.Event{ID: "e1", ChangeContext: evidence.ContextNoKnownChange, Points: tc.args})
			assert.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
