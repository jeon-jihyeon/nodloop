package analysis_test

import (
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/jeon-jihyeon/nodloop/internal/analysis"
	"github.com/jeon-jihyeon/nodloop/internal/evidence"
)

func TestAnalyzeCoverageRule(t *testing.T) {
	t0 := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	every := func(step time.Duration, metric string, dims map[string]string, count int) []evidence.Point {
		points := make([]evidence.Point, count)
		for i := range points {
			points[i] = evidence.Point{Time: t0.Add(time.Duration(i) * step), Metric: metric, Value: 1, Dims: dims}
		}
		return points
	}
	drop := func(points []evidence.Point, hours ...int) []evidence.Point {
		dropped := func(p evidence.Point) bool { return slices.Contains(hours, int(p.Time.Sub(t0).Hours())) }
		return slices.DeleteFunc(slices.Clone(points), dropped)
	}
	spec := analysis.RuleSpec{
		Rule: analysis.RuleCoverage, Metrics: []string{"click_count", "conversion_count"},
		Baseline: 24, Window: 6, Threshold: 0.2, MinSamples: 12,
	}
	loose := analysis.RuleSpec{
		Rule: analysis.RuleCoverage, Metrics: []string{"click_count"}, Baseline: 24, Window: 6, Threshold: 0.1,
	}
	a := map[string]string{"source": "a"}
	full := every(time.Hour, "click_count", a, 36)
	// Window slots are hours 30 to 35 so dropping 30 and 31 leaves two empty slots and 26 and 27 fall in the baseline
	gapped := drop(full, 26, 27, 30, 31)
	gappedWindow := analysis.Window{Start: t0.Add(30 * time.Hour), End: t0.Add(35 * time.Hour), Points: 4}
	gappedRef := analysis.Ref{EventID: "e1", Start: gappedWindow.Start, End: gappedWindow.End}
	gappedDetail := analysis.Detail{PeakTime: t0.Add(31 * time.Hour), PeakValue: 2, Samples: 32, Missing: 2}
	eventWindow := analysis.Window{Start: t0, End: t0.Add(35 * time.Hour), Points: 36}
	eventRef := analysis.Ref{EventID: "e1", Start: t0, End: t0.Add(35 * time.Hour)}
	type args struct {
		spec          analysis.RuleSpec
		changeContext evidence.Context
		points        []evidence.Point
	}
	tcs := []struct {
		name string
		args args
		want analysis.Observations
	}{
		{
			name: "gap inside the window is missing points",
			args: args{spec: spec, changeContext: evidence.ContextNoKnownChange, points: gapped},
			want: analysis.Observations{{
				Rule: analysis.RuleCoverage, Target: a, Metric: "click_count", Window: gappedWindow,
				Current: 4, Baseline: 6, Change: 2.0 / 6, Severity: 2.0 / 6, Adequate: true, Detail: gappedDetail, Ref: gappedRef,
				Summary: "click_count source=a: 2 of 6 expected points missing at 1h cadence, " +
					"largest gap ends at 2026-09-23T07:00:00Z",
			}},
		},
		{
			name: "series that stops before the event ends shows its tail gap",
			args: args{
				spec: spec, changeContext: evidence.ContextNoKnownChange,
				points: slices.Concat(drop(full, 34, 35), every(time.Hour, "conversion_count", a, 36)),
			},
			want: analysis.Observations{{
				Rule: analysis.RuleCoverage, Target: a, Metric: "click_count", Window: gappedWindow,
				Current: 4, Baseline: 6, Change: 2.0 / 6, Severity: 2.0 / 6, Adequate: true,
				Detail: analysis.Detail{PeakTime: t0.Add(35 * time.Hour), PeakValue: 2, Samples: 34, Missing: 2}, Ref: gappedRef,
				Summary: "click_count source=a: 2 of 6 expected points missing at 1h cadence, " +
					"largest gap ends at 2026-09-23T11:00:00Z",
			}},
		},
		{
			name: "gap wider than the window counts at most the window",
			args: args{
				spec: loose, changeContext: evidence.ContextNoKnownChange,
				points: slices.Concat(
					every(time.Hour, "click_count", nil, 4),
					[]evidence.Point{{Time: t0.Add(20 * time.Hour), Metric: "click_count", Value: 1}},
				),
			},
			want: analysis.Observations{{
				Rule: analysis.RuleCoverage, Metric: "click_count",
				Window:  analysis.Window{Start: t0.Add(15 * time.Hour), End: t0.Add(20 * time.Hour), Points: 1},
				Current: 1, Baseline: 6, Change: 5.0 / 6, Severity: 5.0 / 6, Adequate: true,
				Detail:  analysis.Detail{PeakTime: t0.Add(19 * time.Hour), PeakValue: 5, Samples: 5, Missing: 5},
				Ref:     analysis.Ref{EventID: "e1", Start: t0.Add(15 * time.Hour), End: t0.Add(20 * time.Hour)},
				Summary: "click_count: 5 of 6 expected points missing at 1h cadence, largest gap ends at 2026-09-22T19:00:00Z",
			}},
		},
		{
			name: "complete window yields nothing",
			args: args{spec: spec, changeContext: evidence.ContextNoKnownChange, points: full},
			want: nil,
		},
		{
			name: "gap below the threshold yields nothing",
			args: args{spec: spec, changeContext: evidence.ContextNoKnownChange, points: drop(full, 33)},
			want: nil,
		},
		{
			name: "second metric is checked too",
			args: args{
				spec: spec, changeContext: evidence.ContextNoKnownChange,
				points: slices.Concat(full, drop(every(time.Hour, "conversion_count", a, 36), 26, 27, 30, 31)),
			},
			want: analysis.Observations{{
				Rule: analysis.RuleCoverage, Target: a, Metric: "conversion_count", Window: gappedWindow,
				Current: 4, Baseline: 6, Change: 2.0 / 6, Severity: 2.0 / 6, Adequate: true, Detail: gappedDetail, Ref: gappedRef,
				Summary: "conversion_count source=a: 2 of 6 expected points missing at 1h cadence, " +
					"largest gap ends at 2026-09-23T07:00:00Z",
			}},
		},
		{
			name: "short baseline is inadequate",
			args: args{spec: spec, changeContext: evidence.ContextNoKnownChange, points: every(time.Hour, "click_count", a, 8)},
			want: analysis.Observations{{
				Rule: analysis.RuleCoverage, Target: a, Metric: "click_count",
				Window:  analysis.Window{Start: t0.Add(2 * time.Hour), End: t0.Add(7 * time.Hour), Points: 6},
				Detail:  analysis.Detail{Samples: 8},
				Ref:     analysis.Ref{EventID: "e1", Start: t0.Add(2 * time.Hour), End: t0.Add(7 * time.Hour)},
				Summary: "click_count source=a: 2 baseline points, fewer than 12 required",
			}},
		},
		{
			name: "measurement change is flagged without missing points",
			args: args{spec: spec, changeContext: evidence.ContextMeasurementChanged, points: full},
			want: analysis.Observations{{
				Rule: analysis.RuleCoverage, Window: eventWindow, Change: 1, Severity: 1, Adequate: true,
				Detail: analysis.Detail{Samples: 36}, Ref: eventRef,
				Summary: "change context measurement_context_changed: " +
					"the baseline comparison is not trusted until the context is resolved",
			}},
		},
		{
			name: "data availability issue is flagged before missing points",
			args: args{spec: spec, changeContext: evidence.ContextDataAvailability, points: gapped},
			want: analysis.Observations{
				{
					Rule: analysis.RuleCoverage, Window: analysis.Window{Start: t0, End: t0.Add(35 * time.Hour), Points: 32},
					Change: 1, Severity: 1, Adequate: true, Detail: analysis.Detail{Samples: 32}, Ref: eventRef,
					Summary: "change context data_availability_issue: " +
						"the baseline comparison is not trusted until the context is resolved",
				},
				{
					Rule: analysis.RuleCoverage, Target: a, Metric: "click_count", Window: gappedWindow,
					Current: 4, Baseline: 6, Change: 2.0 / 6, Severity: 2.0 / 6, Adequate: true, Detail: gappedDetail, Ref: gappedRef,
					Summary: "click_count source=a: 2 of 6 expected points missing at 1h cadence, " +
						"largest gap ends at 2026-09-23T07:00:00Z",
				},
			},
		},
		{
			name: "planned change is not flagged",
			args: args{spec: spec, changeContext: evidence.ContextPlannedChange, points: full},
			want: nil,
		},
		{
			name: "unknown context is not flagged",
			args: args{spec: spec, changeContext: evidence.ContextUnknown, points: full},
			want: nil,
		},
		{
			name: "event without points yields nothing",
			args: args{spec: spec, changeContext: evidence.ContextNoKnownChange},
			want: nil,
		},
		{
			name: "series shorter than the window takes the cadence from the window",
			args: args{
				spec: loose, changeContext: evidence.ContextNoKnownChange,
				points: every(time.Hour, "click_count", nil, 5),
			},
			want: analysis.Observations{{
				Rule: analysis.RuleCoverage, Metric: "click_count",
				Window:  analysis.Window{Start: t0.Add(-time.Hour), End: t0.Add(4 * time.Hour), Points: 5},
				Current: 5, Baseline: 6, Change: 1.0 / 6, Severity: 1.0 / 6, Adequate: true,
				Detail:  analysis.Detail{PeakTime: t0.Add(-time.Hour), PeakValue: 1, Samples: 5, Missing: 1},
				Ref:     analysis.Ref{EventID: "e1", Start: t0.Add(-time.Hour), End: t0.Add(4 * time.Hour)},
				Summary: "click_count: 1 of 6 expected points missing at 1h cadence, largest gap ends at 2026-09-21T23:00:00Z",
			}},
		},
		{
			name: "cadence in whole minutes is named in minutes",
			args: args{
				spec: loose, changeContext: evidence.ContextNoKnownChange,
				points: every(30*time.Minute, "click_count", nil, 5),
			},
			want: analysis.Observations{{
				Rule: analysis.RuleCoverage, Metric: "click_count",
				Window:  analysis.Window{Start: t0.Add(-30 * time.Minute), End: t0.Add(2 * time.Hour), Points: 5},
				Current: 5, Baseline: 6, Change: 1.0 / 6, Severity: 1.0 / 6, Adequate: true,
				Detail:  analysis.Detail{PeakTime: t0.Add(-30 * time.Minute), PeakValue: 1, Samples: 5, Missing: 1},
				Ref:     analysis.Ref{EventID: "e1", Start: t0.Add(-30 * time.Minute), End: t0.Add(2 * time.Hour)},
				Summary: "click_count: 1 of 6 expected points missing at 30m cadence, largest gap ends at 2026-09-21T23:30:00Z",
			}},
		},
		{
			name: "cadence in seconds keeps the duration text",
			args: args{
				spec: loose, changeContext: evidence.ContextNoKnownChange,
				points: every(90*time.Second, "click_count", nil, 5),
			},
			want: analysis.Observations{{
				Rule: analysis.RuleCoverage, Metric: "click_count",
				Window:  analysis.Window{Start: t0.Add(-90 * time.Second), End: t0.Add(6 * time.Minute), Points: 5},
				Current: 5, Baseline: 6, Change: 1.0 / 6, Severity: 1.0 / 6, Adequate: true,
				Detail:  analysis.Detail{PeakTime: t0.Add(-90 * time.Second), PeakValue: 1, Samples: 5, Missing: 1},
				Ref:     analysis.Ref{EventID: "e1", Start: t0.Add(-90 * time.Second), End: t0.Add(6 * time.Minute)},
				Summary: "click_count: 1 of 6 expected points missing at 1m30s cadence, largest gap ends at 2026-09-21T23:58:30Z",
			}},
		},
		{
			name: "duplicate timestamps do not set the cadence and fill one slot",
			args: args{
				spec: loose, changeContext: evidence.ContextNoKnownChange,
				points: slices.Concat(every(time.Hour, "click_count", nil, 1), every(time.Hour, "click_count", nil, 4)),
			},
			want: analysis.Observations{{
				Rule: analysis.RuleCoverage, Metric: "click_count",
				Window:  analysis.Window{Start: t0.Add(-2 * time.Hour), End: t0.Add(3 * time.Hour), Points: 4},
				Current: 4, Baseline: 6, Change: 2.0 / 6, Severity: 2.0 / 6, Adequate: true,
				Detail:  analysis.Detail{PeakTime: t0.Add(-time.Hour), PeakValue: 2, Samples: 5, Missing: 2},
				Ref:     analysis.Ref{EventID: "e1", Start: t0.Add(-2 * time.Hour), End: t0.Add(3 * time.Hour)},
				Summary: "click_count: 2 of 6 expected points missing at 1h cadence, largest gap ends at 2026-09-21T23:00:00Z",
			}},
		},
		{
			name: "measurement change without points is still flagged",
			args: args{spec: spec, changeContext: evidence.ContextMeasurementChanged},
			want: analysis.Observations{{
				Rule: analysis.RuleCoverage, Change: 1, Severity: 1, Adequate: true, Ref: analysis.Ref{EventID: "e1"},
				Summary: "change context measurement_context_changed: " +
					"the baseline comparison is not trusted until the context is resolved",
			}},
		},
		{
			name: "single point has no cadence",
			args: args{
				spec: loose, changeContext: evidence.ContextNoKnownChange,
				points: every(time.Hour, "click_count", nil, 1),
			},
			want: analysis.Observations{{
				Rule: analysis.RuleCoverage, Metric: "click_count", Window: analysis.Window{Start: t0, End: t0, Points: 1},
				Detail: analysis.Detail{Samples: 1}, Ref: analysis.Ref{EventID: "e1", Start: t0, End: t0},
				Summary: "click_count: cadence unknown with 1 points",
			}},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ev := evidence.Event{ID: "e1", ChangeContext: tc.args.changeContext, Points: tc.args.points}
			got, err := analysis.Policy{Version: "t", Analyzers: []analysis.RuleSpec{tc.args.spec}}.Analyze(ev)
			assert.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
