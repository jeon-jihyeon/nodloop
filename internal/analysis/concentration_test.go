package analysis_test

import (
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/jeon-jihyeon/nodloop/internal/analysis"
	"github.com/jeon-jihyeon/nodloop/internal/evidence"
)

func TestAnalyzeConcentrationChange(t *testing.T) {
	t0 := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	hourly := func(metric string, dims map[string]string, values []float64) []evidence.Point {
		points := make([]evidence.Point, len(values))
		for i, v := range values {
			points[i] = evidence.Point{Time: t0.Add(time.Duration(i) * time.Hour), Metric: metric, Value: v, Dims: dims}
		}
		return points
	}
	threshold := 0.15
	metrics := []string{"click_count"}
	spec := analysis.RuleSpec{
		Rule: analysis.RuleConcentration, Metrics: metrics, GroupBy: "source",
		Baseline: 24, Window: 6, Threshold: threshold, MinSamples: 12,
	}
	a := map[string]string{"source": "a"}
	b := map[string]string{"source": "b"}
	c := map[string]string{"source": "c"}
	steady := slices.Repeat([]float64{100}, 30)
	fresh := slices.Concat(slices.Repeat([]float64{0}, 24), slices.Repeat([]float64{100}, 6))
	shifted := func(window float64) []float64 {
		return slices.Concat(slices.Repeat([]float64{100}, 24), slices.Repeat([]float64{window}, 6))
	}
	windowOf := func(points int) analysis.Window {
		return analysis.Window{Start: t0.Add(24 * time.Hour), End: t0.Add(29 * time.Hour), Points: points}
	}
	ref := analysis.Ref{EventID: "e1", Start: t0.Add(24 * time.Hour), End: t0.Add(29 * time.Hour)}
	early := analysis.Window{Start: t0, End: t0.Add(2 * time.Hour), Points: 6}
	earlyRef := analysis.Ref{EventID: "e1", Start: t0, End: t0.Add(2 * time.Hour)}
	later := analysis.Window{Start: t0.Add(2 * time.Hour), End: t0.Add(7 * time.Hour), Points: 12}
	laterRef := analysis.Ref{EventID: "e1", Start: t0.Add(2 * time.Hour), End: t0.Add(7 * time.Hour)}
	// A third of the baseline against five ninths of the window
	thirdBaseline, thirdWindow := 2400.0/7200, 1500.0/2700
	thirdDelta := thirdWindow - thirdBaseline
	// Runtime subtraction so the want matches the analyzer bit for bit
	gainingShare, losingShare := 660.0/1260, 600.0/1260
	type args struct {
		spec   analysis.RuleSpec
		points []evidence.Point
	}
	tcs := []struct {
		name string
		args args
		want analysis.Observations
	}{
		{
			name: "share shift is reported for both groups",
			args: args{spec: spec, points: slices.Concat(
				hourly("click_count", a, shifted(300)),
				hourly("click_count", b, steady),
			)},
			want: analysis.Observations{
				{
					Rule: analysis.RuleConcentration, Target: a, Metric: "click_count", Window: windowOf(12),
					Current: 0.75, Baseline: 0.5, Change: 0.25, Severity: 0.25 / (2 * threshold), Adequate: true,
					Detail: analysis.Detail{PeakTime: t0.Add(24 * time.Hour), PeakValue: 300, Samples: 60}, Ref: ref,
					Summary: "click_count source=a: window share 0.75 against baseline 0.5, delta 0.25, " +
						"peak 300 at 2026-09-23T00:00:00Z",
				},
				{
					Rule: analysis.RuleConcentration, Target: b, Metric: "click_count", Window: windowOf(12),
					Current: 0.25, Baseline: 0.5, Change: -0.25, Severity: 0.25 / (2 * threshold), Adequate: true,
					Detail: analysis.Detail{PeakTime: t0.Add(24 * time.Hour), PeakValue: 100, Samples: 60}, Ref: ref,
					Summary: "click_count source=b: window share 0.25 against baseline 0.5, delta -0.25, " +
						"peak 100 at 2026-09-23T00:00:00Z",
				},
			},
		},
		{
			name: "only the group past the threshold is reported",
			args: args{spec: spec, points: slices.Concat(
				hourly("click_count", a, shifted(250)),
				hourly("click_count", b, steady),
				hourly("click_count", c, steady),
			)},
			want: analysis.Observations{{
				Rule: analysis.RuleConcentration, Target: a, Metric: "click_count", Window: windowOf(18),
				Current: thirdWindow, Baseline: thirdBaseline, Change: thirdDelta,
				Severity: thirdDelta / (2 * threshold), Adequate: true,
				Detail: analysis.Detail{PeakTime: t0.Add(24 * time.Hour), PeakValue: 250, Samples: 90}, Ref: ref,
				Summary: "click_count source=a: window share 0.5556 against baseline 0.3333, delta 0.2222, " +
					"peak 250 at 2026-09-23T00:00:00Z",
			}},
		},
		{
			name: "zero threshold reports any shift at full severity",
			args: args{
				spec: analysis.RuleSpec{
					Rule: analysis.RuleConcentration, Metrics: metrics, GroupBy: "source", Baseline: 24, Window: 6, MinSamples: 12,
				},
				points: slices.Concat(hourly("click_count", a, shifted(110)), hourly("click_count", b, steady)),
			},
			want: analysis.Observations{
				{
					Rule: analysis.RuleConcentration, Target: a, Metric: "click_count", Window: windowOf(12),
					Current: gainingShare, Baseline: 0.5, Change: gainingShare - 0.5, Severity: 1, Adequate: true,
					Detail: analysis.Detail{PeakTime: t0.Add(24 * time.Hour), PeakValue: 110, Samples: 60}, Ref: ref,
					Summary: "click_count source=a: window share 0.5238 against baseline 0.5, delta 0.0238, " +
						"peak 110 at 2026-09-23T00:00:00Z",
				},
				{
					Rule: analysis.RuleConcentration, Target: b, Metric: "click_count", Window: windowOf(12),
					Current: losingShare, Baseline: 0.5, Change: losingShare - 0.5, Severity: 1, Adequate: true,
					Detail: analysis.Detail{PeakTime: t0.Add(24 * time.Hour), PeakValue: 100, Samples: 60}, Ref: ref,
					Summary: "click_count source=b: window share 0.4762 against baseline 0.5, delta -0.0238, " +
						"peak 100 at 2026-09-23T00:00:00Z",
				},
			},
		},
		{
			name: "stable shares yield nothing",
			args: args{spec: spec, points: slices.Concat(
				hourly("click_count", a, shifted(110)),
				hourly("click_count", b, shifted(105)),
			)},
			want: nil,
		},
		{
			name: "few window points are inadequate for every group",
			args: args{spec: spec, points: slices.Concat(
				hourly("click_count", a, []float64{1, 2, 3}),
				hourly("click_count", b, []float64{1, 2, 3}),
			)},
			want: analysis.Observations{
				{
					Rule: analysis.RuleConcentration, Target: a, Metric: "click_count", Window: early,
					Detail: analysis.Detail{PeakTime: t0.Add(2 * time.Hour), PeakValue: 3, Samples: 6}, Ref: earlyRef,
					Summary: "click_count source=a: 0 baseline points, fewer than 12 required",
				},
				{
					Rule: analysis.RuleConcentration, Target: b, Metric: "click_count", Window: early,
					Detail: analysis.Detail{PeakTime: t0.Add(2 * time.Hour), PeakValue: 3, Samples: 6}, Ref: earlyRef,
					Summary: "click_count source=b: 0 baseline points, fewer than 12 required",
				},
			},
		},
		{
			name: "enough window points are inadequate when the baselines are short",
			args: args{spec: spec, points: slices.Concat(
				hourly("click_count", a, steady[:8]),
				hourly("click_count", b, steady[:8]),
			)},
			want: analysis.Observations{
				{
					Rule: analysis.RuleConcentration, Target: a, Metric: "click_count", Window: later,
					Detail: analysis.Detail{PeakTime: t0.Add(2 * time.Hour), PeakValue: 100, Samples: 16}, Ref: laterRef,
					Summary: "click_count source=a: 4 baseline points, fewer than 12 required",
				},
				{
					Rule: analysis.RuleConcentration, Target: b, Metric: "click_count", Window: later,
					Detail: analysis.Detail{PeakTime: t0.Add(2 * time.Hour), PeakValue: 100, Samples: 16}, Ref: laterRef,
					Summary: "click_count source=b: 4 baseline points, fewer than 12 required",
				},
			},
		},
		{
			name: "zero window total is inadequate",
			args: args{spec: spec, points: slices.Concat(
				hourly("click_count", a, shifted(0)),
				hourly("click_count", b, shifted(0)),
			)},
			want: analysis.Observations{
				{
					Rule: analysis.RuleConcentration, Target: a, Metric: "click_count", Window: windowOf(12),
					Detail: analysis.Detail{PeakTime: t0.Add(24 * time.Hour), Samples: 60}, Ref: ref,
					Summary: "click_count source=a: window total is zero",
				},
				{
					Rule: analysis.RuleConcentration, Target: b, Metric: "click_count", Window: windowOf(12),
					Detail: analysis.Detail{PeakTime: t0.Add(24 * time.Hour), Samples: 60}, Ref: ref,
					Summary: "click_count source=b: window total is zero",
				},
			},
		},
		{
			name: "zero baseline total is inadequate",
			args: args{spec: spec, points: slices.Concat(hourly("click_count", a, fresh), hourly("click_count", b, fresh))},
			want: analysis.Observations{
				{
					Rule: analysis.RuleConcentration, Target: a, Metric: "click_count", Window: windowOf(12),
					Detail: analysis.Detail{PeakTime: t0.Add(24 * time.Hour), PeakValue: 100, Samples: 60}, Ref: ref,
					Summary: "click_count source=a: baseline total is zero",
				},
				{
					Rule: analysis.RuleConcentration, Target: b, Metric: "click_count", Window: windowOf(12),
					Detail: analysis.Detail{PeakTime: t0.Add(24 * time.Hour), PeakValue: 100, Samples: 60}, Ref: ref,
					Summary: "click_count source=b: baseline total is zero",
				},
			},
		},
		{
			name: "series without the dimension are ignored",
			args: args{spec: spec, points: hourly("click_count", nil, steady)},
			want: nil,
		},
		{
			name: "missing metric yields nothing",
			args: args{spec: spec, points: hourly("conversion_count", a, steady)},
			want: nil,
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ev := evidence.Event{ID: "e1", ChangeContext: evidence.ContextNoKnownChange, Points: tc.args.points}
			got, err := analysis.Policy{Version: "t", Analyzers: []analysis.RuleSpec{tc.args.spec}}.Analyze(ev)
			assert.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
