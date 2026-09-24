package analysis_test

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/analysis"
	"github.com/jeon-jihyeon/nodloop/internal/evidence"
	"github.com/jeon-jihyeon/nodloop/internal/evidence/file"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
)

func TestLoadPolicy(t *testing.T) {
	type want struct {
		policy analysis.Policy
		err    error
	}
	tcs := []struct {
		name string
		args string
		want want
	}{
		{
			name: "default policy reads every analyzer",
			args: analysis.DefaultPolicyYAML,
			want: want{policy: analysis.Policy{Version: "demo-1", Analyzers: []analysis.RuleSpec{
				{
					Rule: analysis.RuleZScore, Metrics: []string{"click_count"},
					Baseline: 36, Window: 12, Threshold: 3, MinSamples: 12,
				},
				{
					Rule: analysis.RuleProportion, Metrics: []string{"conversion_count", "click_count"},
					Baseline: 36, Window: 12, Threshold: 3, MinSamples: 12, Recent: 4,
				},
				{
					Rule: analysis.RuleConcentration, Metrics: []string{"click_count"}, GroupBy: "source",
					Baseline: 36, Window: 12, Threshold: 0.15, MinSamples: 12,
				},
				{
					Rule: analysis.RuleCoverage, Metrics: []string{"click_count", "conversion_count"},
					Baseline: 36, Window: 12, Threshold: 0.2, MinSamples: 12,
				},
			}}},
		},
		{
			name: "limits section is left to diagnose and still loads",
			args: "version: v1\nlimits:\n  knowledge_chars: 100\n  example_chars: 200\n  candidates: 3\n",
			want: want{policy: analysis.Policy{Version: "v1"}},
		},
		{
			name: "broken yaml is malformed",
			args: "version: [",
			want: want{err: analysis.ErrMalformedPolicy},
		},
		{
			name: "unknown rule is rejected",
			args: "version: v1\nanalyzers:\n  - rule: ewma\n    metrics: [x]\n    baseline: 1\n    window: 1\n",
			want: want{err: analysis.ErrUnknownRule},
		},
		{
			name: "missing version is rejected",
			args: "analyzers:\n  - rule: zscore\n    metrics: [x]\n    baseline: 1\n    window: 1\n",
			want: want{err: analysis.ErrMissingVersion},
		},
		{
			name: "analyzer without metrics is incomplete",
			args: "version: v1\nanalyzers:\n  - rule: zscore\n    baseline: 1\n    window: 1\n",
			want: want{err: analysis.ErrIncompleteAnalyzer},
		},
		{
			name: "analyzer without a window is incomplete",
			args: "version: v1\nanalyzers:\n  - rule: zscore\n    metrics: [x]\n    baseline: 1\n",
			want: want{err: analysis.ErrIncompleteAnalyzer},
		},
		{
			name: "analyzer without a baseline is incomplete",
			args: "version: v1\nanalyzers:\n  - rule: zscore\n    metrics: [x]\n    window: 1\n",
			want: want{err: analysis.ErrIncompleteAnalyzer},
		},
		{
			name: "concentration without a group dimension is rejected",
			args: "version: v1\nanalyzers:\n  - rule: concentration_change\n    metrics: [x]\n    baseline: 1\n    window: 1\n",
			want: want{err: analysis.ErrMissingGroupBy},
		},
		{
			name: "proportion with one metric is rejected",
			args: "version: v1\nanalyzers:\n  - rule: proportion_control\n    metrics: [x]\n    baseline: 1\n    window: 1\n",
			want: want{err: analysis.ErrMissingDenominator},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := analysis.LoadPolicy([]byte(tc.args))
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.policy, got)
		})
	}
}

func TestDefaultPolicyMatchesTestdata(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "policy.yaml"))
	require.NoError(t, err)
	want, err := analysis.LoadPolicy(b)
	require.NoError(t, err)
	assert.Equal(t, want, analysis.DefaultPolicy())
}

func TestPolicyAnalyze(t *testing.T) {
	t0 := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	hourly := func(dims map[string]string, values []float64) []evidence.Point {
		points := make([]evidence.Point, len(values))
		for i, v := range values {
			points[i] = evidence.Point{Time: t0.Add(time.Duration(i) * time.Hour), Metric: "click_count", Value: v, Dims: dims}
		}
		return points
	}
	// Alternating 100 and 110 so the baseline mean is 105 and the population stddev 5
	wave := slices.Repeat([]float64{100, 110}, 12)
	zscore := analysis.RuleSpec{
		Rule: analysis.RuleZScore, Metrics: []string{"click_count"}, Baseline: 24, Window: 6, Threshold: 3, MinSamples: 12,
	}
	coverage := analysis.RuleSpec{
		Rule: analysis.RuleCoverage, Metrics: []string{"click_count"}, Baseline: 24, Window: 6, Threshold: 0.2,
	}
	short := map[string]string{"source": "short"}
	mild := map[string]string{"source": "mild"}
	wild := map[string]string{"source": "wild"}
	window := analysis.Window{Start: t0.Add(24 * time.Hour), End: t0.Add(29 * time.Hour), Points: 6}
	ref := analysis.Ref{EventID: "e1", Start: window.Start, End: window.End}
	type args struct {
		policy analysis.Policy
		event  evidence.Event
	}
	type want struct {
		observations analysis.Observations
		err          error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			name: "scored observations come first by severity and inadequate ones last",
			args: args{
				policy: analysis.Policy{Version: "t", Analyzers: []analysis.RuleSpec{zscore}},
				event: evidence.Event{ID: "e1", Points: slices.Concat(
					hourly(short, []float64{1, 2, 3}),
					hourly(mild, slices.Concat(wave, slices.Repeat([]float64{100}, 5), []float64{125})),
					hourly(wild, slices.Concat(wave, slices.Repeat([]float64{100}, 5), []float64{200})),
				)},
			},
			want: want{observations: analysis.Observations{
				{
					Rule: analysis.RuleZScore, Target: wild, Metric: "click_count", Window: window,
					Current: 700.0 / 6, Baseline: 105, Change: 19, Severity: 1, Adequate: true,
					Detail:  analysis.Detail{PeakTime: window.End, PeakValue: 200, Samples: 30},
					Ref:     ref,
					Summary: "click_count source=wild: window mean 116.6667 against baseline 105, peak z 19 at 2026-09-23T05:00:00Z",
				},
				{
					Rule: analysis.RuleZScore, Target: mild, Metric: "click_count", Window: window,
					Current: 625.0 / 6, Baseline: 105, Change: 4, Severity: 4.0 / 6, Adequate: true,
					Detail:  analysis.Detail{PeakTime: window.End, PeakValue: 125, Samples: 30},
					Ref:     ref,
					Summary: "click_count source=mild: window mean 104.1667 against baseline 105, peak z 4 at 2026-09-23T05:00:00Z",
				},
				{
					Rule: analysis.RuleZScore, Target: short, Metric: "click_count",
					Window:  analysis.Window{Start: t0, End: t0.Add(2 * time.Hour), Points: 3},
					Current: 2, Detail: analysis.Detail{Samples: 3},
					Ref:     analysis.Ref{EventID: "e1", Start: t0, End: t0.Add(2 * time.Hour)},
					Summary: "click_count source=short: 0 baseline points, fewer than 12 required",
				},
			}},
		},
		{
			name: "inadequate observation found after a scored one still sorts last",
			args: args{
				policy: analysis.Policy{Version: "t", Analyzers: []analysis.RuleSpec{zscore}},
				event: evidence.Event{ID: "e1", Points: slices.Concat(
					hourly(wild, slices.Concat(wave, slices.Repeat([]float64{100}, 5), []float64{200})),
					hourly(short, []float64{1, 2, 3}),
				)},
			},
			want: want{observations: analysis.Observations{
				{
					Rule: analysis.RuleZScore, Target: wild, Metric: "click_count", Window: window,
					Current: 700.0 / 6, Baseline: 105, Change: 19, Severity: 1, Adequate: true,
					Detail:  analysis.Detail{PeakTime: window.End, PeakValue: 200, Samples: 30},
					Ref:     ref,
					Summary: "click_count source=wild: window mean 116.6667 against baseline 105, peak z 19 at 2026-09-23T05:00:00Z",
				},
				{
					Rule: analysis.RuleZScore, Target: short, Metric: "click_count",
					Window:  analysis.Window{Start: t0, End: t0.Add(2 * time.Hour), Points: 3},
					Current: 2, Detail: analysis.Detail{Samples: 3},
					Ref:     analysis.Ref{EventID: "e1", Start: t0, End: t0.Add(2 * time.Hour)},
					Summary: "click_count source=short: 0 baseline points, fewer than 12 required",
				},
			}},
		},
		{
			name: "breaking change context is flagged once across coverage specs",
			args: args{
				policy: analysis.Policy{Version: "t", Analyzers: []analysis.RuleSpec{coverage, coverage}},
				event:  evidence.Event{ID: "e1", ChangeContext: evidence.ContextDataAvailability},
			},
			want: want{observations: analysis.Observations{{
				Rule: analysis.RuleCoverage, Change: 1, Severity: 1, Adequate: true, Ref: analysis.Ref{EventID: "e1"},
				Summary: "change context data_availability_issue: " +
					"the baseline comparison is not trusted until the context is resolved",
			}}},
		},
		{
			name: "breaking change context is not flagged without a coverage spec",
			args: args{
				policy: analysis.Policy{Version: "t", Analyzers: []analysis.RuleSpec{zscore}},
				event:  evidence.Event{ID: "e1", ChangeContext: evidence.ContextMeasurementChanged},
			},
			want: want{},
		},
		{
			name: "policy without analyzers reports nothing",
			args: args{
				policy: analysis.Policy{Version: "t"},
				event:  evidence.Event{ID: "e1", Points: hourly(wild, []float64{1})},
			},
			want: want{},
		},
		{
			name: "unknown rule fails",
			args: args{
				policy: analysis.Policy{Version: "t", Analyzers: []analysis.RuleSpec{{Rule: "ewma"}}},
				event:  evidence.Event{ID: "e1"},
			},
			want: want{err: analysis.ErrUnknownRule},
		},
		{
			name: "hand built spec without metrics fails instead of panicking",
			args: args{
				policy: analysis.Policy{Version: "t", Analyzers: []analysis.RuleSpec{
					{Rule: analysis.RuleZScore, Baseline: 1, Window: 1},
				}},
				event: evidence.Event{ID: "e1", Points: hourly(wild, []float64{1})},
			},
			want: want{err: analysis.ErrIncompleteAnalyzer},
		},
		{
			name: "invalid spec fails before any analyzer runs",
			args: args{
				policy: analysis.Policy{Version: "t", Analyzers: []analysis.RuleSpec{
					zscore,
					{Rule: analysis.RuleConcentration, Metrics: []string{"x"}, Baseline: 1, Window: 1},
				}},
				event: evidence.Event{ID: "e1"},
			},
			want: want{err: analysis.ErrMissingGroupBy},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := tc.args.policy.Analyze(tc.args.event)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.observations, got)
		})
	}
}

func TestObservationsMetrics(t *testing.T) {
	tcs := []struct {
		name string
		args analysis.Observations
		want []string
	}{
		{
			name: "distinct metrics keep observation order",
			args: analysis.Observations{{Metric: "click_count"}, {Metric: "conversion_count"}, {Metric: "click_count"}},
			want: []string{"click_count", "conversion_count"},
		},
		{
			name: "change context observation adds no metric",
			args: analysis.Observations{{Rule: analysis.RuleCoverage, Adequate: true}, {Metric: "click_count"}},
			want: []string{"click_count"},
		},
		{
			name: "no observations give an empty list",
			args: nil,
			want: []string{},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Metrics())
		})
	}
}

func TestObservationsMoved(t *testing.T) {
	tcs := []struct {
		name string
		args analysis.Observations
		want []string
	}{
		{
			name: "adequate observations name their metric once",
			args: analysis.Observations{
				{Rule: analysis.RuleZScore, Metric: "click_count", Adequate: true},
				{Rule: analysis.RuleConcentration, Metric: "click_count", Adequate: true},
			},
			want: []string{"click_count"},
		},
		{
			name: "inadequate observations do not move a metric",
			args: analysis.Observations{{Rule: analysis.RuleProportion, Metric: "conversion_count"}},
			want: nil,
		},
		{
			name: "coverage gaps do not move a metric",
			args: analysis.Observations{{Rule: analysis.RuleCoverage, Metric: "conversion_count", Adequate: true}},
			want: nil,
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Moved())
		})
	}
}

func TestAnalyzeDemoSetFindsEveryLabeledAnomaly(t *testing.T) {
	src, err := file.New(testkit.DemoDir(t))
	require.NoError(t, err)
	ctx := context.Background()
	labels, err := src.Labels(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, labels)
	// An anomaly is found when an adequate value observation of its metric targets a subset of its dims
	flags := func(o analysis.Observation, a evidence.SeriesRef) bool {
		outside := func(k string) bool { return a.Dims[k] != o.Target[k] }
		return o.Adequate && o.Rule != analysis.RuleCoverage && o.Metric == a.Metric &&
			!slices.ContainsFunc(slices.Collect(maps.Keys(o.Target)), outside)
	}
	for _, l := range labels {
		t.Run(l.EventID+" "+string(l.Type), func(t *testing.T) {
			t.Parallel()
			ev, err := src.Event(ctx, l.EventID)
			require.NoError(t, err)
			got, err := analysis.DefaultPolicy().Analyze(ev)
			require.NoError(t, err)
			missed := func(a evidence.SeriesRef) bool {
				return !slices.ContainsFunc(got, func(o analysis.Observation) bool { return flags(o, a) })
			}
			assert.Equal(t, l.Anomalies, slices.DeleteFunc(slices.Clone(l.Anomalies), missed))
		})
	}
}

func TestAnalyzeDemoSetMovesNothingOnQuietEvents(t *testing.T) {
	src, err := file.New(testkit.DemoDir(t))
	require.NoError(t, err)
	ctx := context.Background()
	labels, err := src.Labels(ctx)
	require.NoError(t, err)
	// A quiet event has no labeled anomaly and no hold
	loud := func(l evidence.Label) bool { return len(l.Anomalies) > 0 || l.IsHold() }
	quiet := slices.DeleteFunc(labels, loud)
	require.NotEmpty(t, quiet)
	for _, l := range quiet {
		t.Run(l.EventID+" "+string(l.Type), func(t *testing.T) {
			t.Parallel()
			ev, err := src.Event(ctx, l.EventID)
			require.NoError(t, err)
			got, err := analysis.DefaultPolicy().Analyze(ev)
			require.NoError(t, err)
			assert.Nil(t, got.Moved())
		})
	}
}
