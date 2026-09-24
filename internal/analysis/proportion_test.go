package analysis_test

import (
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/jeon-jihyeon/nodloop/internal/analysis"
	"github.com/jeon-jihyeon/nodloop/internal/evidence"
)

func TestAnalyzeProportionControl(t *testing.T) {
	t0 := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	hourly := func(metric string, dims map[string]string, values []float64) []evidence.Point {
		points := make([]evidence.Point, len(values))
		for i, v := range values {
			points[i] = evidence.Point{Time: t0.Add(time.Duration(i) * time.Hour), Metric: metric, Value: v, Dims: dims}
		}
		return points
	}
	// Baseline hours at the first value and window hours at the second
	shifted := func(baseline, window float64) []float64 {
		return slices.Concat(slices.Repeat([]float64{baseline}, 24), slices.Repeat([]float64{window}, 6))
	}
	metrics := []string{"conversion_count", "click_count"}
	spec := analysis.RuleSpec{
		Rule: analysis.RuleProportion, Metrics: metrics, Baseline: 24, Window: 6, Threshold: 3, MinSamples: 12,
	}
	a := map[string]string{"source": "a"}
	b := map[string]string{"source": "b"}
	clicks := hourly("click_count", a, slices.Repeat([]float64{1000}, 30))
	window := analysis.Window{Start: t0.Add(24 * time.Hour), End: t0.Add(29 * time.Hour), Points: 6}
	ref := analysis.Ref{EventID: "e1", Start: window.Start, End: window.End}
	// Ratios of variables run at runtime so the want matches the analyzer bit for bit
	baseline, dropped, risen, lagged := 1200.0/24000, 60.0/6000, 600.0/6000, 220.0/6000
	droppedChange, risenChange, laggedChange := dropped/baseline, risen/baseline, lagged/baseline
	// Hour 26 has no conversions and hour 5 no clicks so the paired window starts at hour 23 where clicks doubled
	paired := 100.0 / 7000
	pairedChange := paired / baseline
	pairedWindow := analysis.Window{Start: t0.Add(23 * time.Hour), End: t0.Add(29 * time.Hour), Points: 6}
	laggedConversions := slices.Concat(slices.Repeat([]float64{50}, 28), []float64{10, 10})
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
			name: "rate below the lower limit is a drop",
			args: args{spec: spec, points: slices.Concat(clicks, hourly("conversion_count", a, shifted(50, 10)))},
			want: analysis.Observations{{
				Rule: analysis.RuleProportion, Target: a, Metric: "conversion_count", Window: window,
				Current: dropped, Baseline: baseline, Change: droppedChange, Severity: 1 - droppedChange, Adequate: true,
				Detail: analysis.Detail{PeakTime: window.Start, PeakValue: 0.01, Samples: 30}, Ref: ref,
				Summary: "conversion_count source=a: window rate 0.01 against baseline 0.05 over click_count 6000, " +
					"limits 0.0416 to 0.0584",
			}},
		},
		{
			name: "rate above the upper limit is a rise",
			args: args{spec: spec, points: slices.Concat(clicks, hourly("conversion_count", a, shifted(50, 100)))},
			want: analysis.Observations{{
				Rule: analysis.RuleProportion, Target: a, Metric: "conversion_count", Window: window,
				Current: risen, Baseline: baseline, Change: risenChange, Severity: 1, Adequate: true,
				Detail: analysis.Detail{PeakTime: window.Start, PeakValue: 0.1, Samples: 30}, Ref: ref,
				Summary: "conversion_count source=a: window rate 0.1 against baseline 0.05 over click_count 6000, " +
					"limits 0.0416 to 0.0584",
			}},
		},
		{
			name: "rate inside the limits yields nothing",
			args: args{spec: spec, points: slices.Concat(clicks, hourly("conversion_count", a, shifted(50, 55)))},
			want: nil,
		},
		{
			name: "only the dropping series is reported",
			args: args{spec: spec, points: slices.Concat(
				clicks,
				hourly("click_count", b, slices.Repeat([]float64{1000}, 30)),
				hourly("conversion_count", a, shifted(50, 10)),
				hourly("conversion_count", b, shifted(50, 50)),
			)},
			want: analysis.Observations{{
				Rule: analysis.RuleProportion, Target: a, Metric: "conversion_count", Window: window,
				Current: dropped, Baseline: baseline, Change: droppedChange, Severity: 1 - droppedChange, Adequate: true,
				Detail: analysis.Detail{PeakTime: window.Start, PeakValue: 0.01, Samples: 30}, Ref: ref,
				Summary: "conversion_count source=a: window rate 0.01 against baseline 0.05 over click_count 6000, " +
					"limits 0.0416 to 0.0584",
			}},
		},
		{
			name: "newest points are reported apart when the rate fell only there",
			args: args{
				spec: analysis.RuleSpec{
					Rule: analysis.RuleProportion, Metrics: metrics, Baseline: 24, Window: 6, Threshold: 3, MinSamples: 12, Recent: 2,
				},
				points: slices.Concat(clicks, hourly("conversion_count", a, laggedConversions)),
			},
			want: analysis.Observations{{
				Rule: analysis.RuleProportion, Target: a, Metric: "conversion_count", Window: window,
				Current: lagged, Baseline: baseline, Change: laggedChange, Severity: 1 - laggedChange, Adequate: true,
				Detail: analysis.Detail{PeakTime: t0.Add(28 * time.Hour), PeakValue: 0.01, Samples: 30, RecentRate: 0.01},
				Ref:    ref,
				Summary: "conversion_count source=a: window rate 0.0367 against baseline 0.05 over click_count 6000, " +
					"limits 0.0416 to 0.0584, newest 2 points rate 0.01 against the earlier 4 points 0.05",
			}},
		},
		{
			name: "recent size that leaves no earlier point reports the window as one number",
			args: args{
				spec: analysis.RuleSpec{
					Rule: analysis.RuleProportion, Metrics: metrics, Baseline: 24, Window: 6, Threshold: 3, MinSamples: 12, Recent: 6,
				},
				points: slices.Concat(clicks, hourly("conversion_count", a, laggedConversions)),
			},
			want: analysis.Observations{{
				Rule: analysis.RuleProportion, Target: a, Metric: "conversion_count", Window: window,
				Current: lagged, Baseline: baseline, Change: laggedChange, Severity: 1 - laggedChange, Adequate: true,
				Detail: analysis.Detail{PeakTime: t0.Add(28 * time.Hour), PeakValue: 0.01, Samples: 30}, Ref: ref,
				Summary: "conversion_count source=a: window rate 0.0367 against baseline 0.05 over click_count 6000, " +
					"limits 0.0416 to 0.0584",
			}},
		},
		{
			name: "newest points without clicks have no rate of their own",
			args: args{
				spec: analysis.RuleSpec{
					Rule: analysis.RuleProportion, Metrics: metrics, Baseline: 24, Window: 6, Threshold: 3, MinSamples: 12, Recent: 2,
				},
				points: slices.Concat(
					hourly("click_count", a, slices.Concat(slices.Repeat([]float64{1000}, 28), []float64{0, 0})),
					hourly("conversion_count", a, slices.Concat(slices.Repeat([]float64{50}, 24), []float64{10, 10, 10, 10, 0, 0})),
				),
			},
			want: analysis.Observations{{
				Rule: analysis.RuleProportion, Target: a, Metric: "conversion_count", Window: window,
				Current: 40.0 / 4000, Baseline: baseline, Change: droppedChange, Severity: 1 - droppedChange, Adequate: true,
				Detail: analysis.Detail{PeakTime: window.Start, PeakValue: 0.01, Samples: 30}, Ref: ref,
				Summary: "conversion_count source=a: window rate 0.01 against baseline 0.05 over click_count 4000, " +
					"limits 0.0397 to 0.0603",
			}},
		},
		{
			name: "missing denominator series is inadequate",
			args: args{spec: spec, points: hourly("conversion_count", a, shifted(50, 10))},
			want: analysis.Observations{{
				Rule: analysis.RuleProportion, Target: a, Metric: "conversion_count",
				Detail: analysis.Detail{Missing: 30}, Ref: analysis.Ref{EventID: "e1"},
				Summary: "conversion_count source=a over click_count: 0 baseline points, fewer than 12 required",
			}},
		},
		{
			name: "short numerator series is inadequate",
			args: args{spec: spec, points: slices.Concat(clicks, hourly("conversion_count", a, []float64{1, 2, 3}))},
			want: analysis.Observations{{
				Rule: analysis.RuleProportion, Target: a, Metric: "conversion_count",
				Window:  analysis.Window{Start: t0, End: t0.Add(2 * time.Hour), Points: 3},
				Detail:  analysis.Detail{Samples: 3, Missing: 27},
				Ref:     analysis.Ref{EventID: "e1", Start: t0, End: t0.Add(2 * time.Hour)},
				Summary: "conversion_count source=a over click_count: 0 baseline points, fewer than 12 required",
			}},
		},
		{
			name: "zero window denominator is inadequate",
			args: args{spec: spec, points: slices.Concat(
				hourly("click_count", a, shifted(1000, 0)),
				hourly("conversion_count", a, shifted(50, 0)),
			)},
			want: analysis.Observations{{
				Rule: analysis.RuleProportion, Target: a, Metric: "conversion_count", Window: window,
				Detail: analysis.Detail{Samples: 30}, Ref: ref, Summary: "conversion_count source=a: click_count sums to zero",
			}},
		},
		{
			name: "zero baseline rate is inadequate",
			args: args{spec: spec, points: slices.Concat(clicks, hourly("conversion_count", a, shifted(0, 10)))},
			want: analysis.Observations{{
				Rule: analysis.RuleProportion, Target: a, Metric: "conversion_count", Window: window, Current: dropped,
				Detail: analysis.Detail{Samples: 30}, Ref: ref, Summary: "conversion_count source=a: baseline rate is zero",
			}},
		},
		{
			name: "baseline rate above one is inadequate",
			args: args{spec: spec, points: slices.Concat(clicks, hourly("conversion_count", a, shifted(2000, 10)))},
			want: analysis.Observations{{
				Rule: analysis.RuleProportion, Target: a, Metric: "conversion_count", Window: window, Current: dropped, Baseline: 2,
				Detail: analysis.Detail{Samples: 30}, Ref: ref, Summary: "conversion_count source=a: baseline rate is above one",
			}},
		},
		{
			name: "missing hours that differ between the metrics pair by timestamp",
			args: args{spec: spec, points: slices.Concat(
				slices.Delete(hourly("click_count", a, slices.Concat(
					slices.Repeat([]float64{1000}, 23), []float64{2000}, slices.Repeat([]float64{1000}, 6),
				)), 5, 6),
				slices.Delete(hourly("conversion_count", a, shifted(50, 10)), 26, 27),
			)},
			want: analysis.Observations{{
				Rule: analysis.RuleProportion, Target: a, Metric: "conversion_count", Window: pairedWindow,
				Current: paired, Baseline: baseline, Change: pairedChange, Severity: 1 - pairedChange, Adequate: true,
				Detail: analysis.Detail{PeakTime: window.Start, PeakValue: 0.01, Samples: 28, Missing: 2},
				Ref:    analysis.Ref{EventID: "e1", Start: pairedWindow.Start, End: pairedWindow.End},
				Summary: "conversion_count source=a: window rate 0.0143 against baseline 0.05 over click_count 7000, " +
					"limits 0.0422 to 0.0578",
			}},
		},
		{
			name: "missing numerator metric yields nothing",
			args: args{spec: spec, points: clicks},
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
