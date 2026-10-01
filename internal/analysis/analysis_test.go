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

func TestLoadContexts(t *testing.T) {
	type want struct {
		contexts evidence.Contexts
		err      error
	}
	tcs := []struct {
		name string
		args string
		want want
	}{
		{"a policy without contexts declares the five defaults", "version: v\n", want{contexts: evidence.DefaultContexts()}},
		{"an empty list declares the five defaults", "contexts: []\n", want{contexts: evidence.DefaultContexts()}},
		{
			"declared contexts keep their order and unknown is appended",
			"contexts:\n  - name: deploy\n    breaks_baseline: true\n  - name: campaign_start\n",
			want{contexts: evidence.Contexts{{Name: "deploy", BreaksBaseline: true}, {Name: "campaign_start"}, {Name: evidence.ContextUnknown}}},
		},
		{
			"unknown declared in place stays there",
			"contexts:\n  - name: unknown\n  - name: deploy\n",
			want{contexts: evidence.Contexts{{Name: evidence.ContextUnknown}, {Name: "deploy"}}},
		},
		{"a context without a name fails", "contexts:\n  - breaks_baseline: true\n", want{err: analysis.ErrUnknownContextDecl}},
		{"a context declared twice fails", "contexts:\n  - name: deploy\n  - name: deploy\n", want{err: analysis.ErrUnknownContextDecl}},
		{"unknown that breaks fails", "contexts:\n  - name: unknown\n    breaks_baseline: true\n", want{err: analysis.ErrUnknownContextDecl}},
		{"a file that does not parse fails", "contexts: [\n", want{err: analysis.ErrMalformedPolicy}},
		{"broken analyzers never stop the contexts", "contexts:\n  - name: deploy\nanalyzers:\n  - rule: nope\n",
			want{contexts: evidence.Contexts{{Name: "deploy"}, {Name: evidence.ContextUnknown}}}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := analysis.LoadContexts([]byte(tc.args))
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.contexts, got)
		})
	}
}

func TestLoadPolicy(t *testing.T) {
	demo, err := os.ReadFile(filepath.Join(testkit.DemoDir(t), "policy.yaml"))
	require.NoError(t, err)
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
			name: "demo policy reads every analyzer",
			args: string(demo),
			want: want{policy: analysis.Policy{Version: "demo-1", Contexts: evidence.DefaultContexts(), Analyzers: []analysis.RuleSpec{
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
			name: "a limits section is refused because the review caps are internal",
			args: "version: v1\nlimits:\n  knowledge_chars: 100\n  example_chars: 200\n  candidates: 3\n",
			want: want{err: analysis.ErrLimitsSection},
		},
		{
			name: "an empty limits section is refused too",
			args: "version: v1\nlimits:\n",
			want: want{err: analysis.ErrLimitsSection},
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
			want: want{err: analysis.ErrProportionMetrics},
		},
		{
			name: "proportion with a third metric is rejected instead of dropping it",
			args: "version: v1\nanalyzers:\n  - rule: proportion_control\n    metrics: [x, y, z]\n    baseline: 1\n    window: 1\n",
			want: want{err: analysis.ErrProportionMetrics},
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
				policy: analysis.Policy{Version: "t", Contexts: evidence.DefaultContexts(), Analyzers: []analysis.RuleSpec{coverage, coverage}},
				event:  evidence.Event{ID: "e1", ChangeContext: evidence.ContextDataAvailability},
			},
			want: want{observations: analysis.Observations{{
				Rule: analysis.RuleCoverage, Change: 1, Severity: 1, Adequate: true, Ref: analysis.Ref{EventID: "e1"},
				Summary: "change context data_availability_issue: " +
					"the baseline comparison is not trusted until the context is resolved",
			}}},
		},
		{
			name: "a declared context that breaks the baseline is flagged like a default one",
			args: args{
				policy: analysis.Policy{
					Version: "t", Contexts: evidence.Contexts{{Name: "deploy", BreaksBaseline: true}}, Analyzers: []analysis.RuleSpec{coverage},
				},
				event: evidence.Event{ID: "e1", ChangeContext: "deploy"},
			},
			want: want{observations: analysis.Observations{{
				Rule: analysis.RuleCoverage, Change: 1, Severity: 1, Adequate: true, Ref: analysis.Ref{EventID: "e1"},
				Summary: "change context deploy: the baseline comparison is not trusted until the context is resolved",
			}}},
		},
		{
			name: "a default breaking context another data set does not declare is not flagged",
			args: args{
				policy: analysis.Policy{
					Version: "t", Contexts: evidence.Contexts{{Name: "deploy", BreaksBaseline: true}}, Analyzers: []analysis.RuleSpec{coverage},
				},
				event: evidence.Event{ID: "e1", ChangeContext: evidence.ContextDataAvailability},
			},
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

// A bound policy flags the change contexts its file declares
func TestPolicyObservedKeepsContexts(t *testing.T) {
	declared := evidence.Contexts{{Name: "deploy", BreaksBaseline: true}, {Name: evidence.ContextUnknown}}
	policy := analysis.Policy{Version: "t", Contexts: declared}
	tcs := []struct {
		name string
		args []string
	}{
		{"bound to the metrics of a data set", []string{"click_count"}},
		{"bound to a data set without events", nil},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, declared, policy.Observed(tc.args, nil).Contexts)
		})
	}
}

func TestPolicyCheckObserved(t *testing.T) {
	policy := func(specs ...analysis.RuleSpec) analysis.Policy {
		return analysis.Policy{Version: "t", Analyzers: specs}
	}
	zscore := analysis.RuleSpec{Rule: analysis.RuleZScore, Metrics: []string{"click_count"}, Baseline: 1, Window: 1}
	proportion := analysis.RuleSpec{
		Rule: analysis.RuleProportion, Metrics: []string{"conversion_count", "click_count"}, Baseline: 1, Window: 1,
	}
	concentration := analysis.RuleSpec{
		Rule: analysis.RuleConcentration, Metrics: []string{"click_count"}, GroupBy: "source", Baseline: 1, Window: 1,
	}
	typo := zscore
	typo.Metrics = []string{"click count"}
	denominator := proportion
	denominator.Metrics = []string{"conversion_count", "clicks"}
	group := concentration
	group.GroupBy = "sources"
	type args struct {
		policy  analysis.Policy
		metrics []string
		dims    []string
	}
	tcs := []struct {
		name string
		args args
		want error
	}{
		{
			name: "every metric and dimension is carried",
			args: args{
				policy:  policy(zscore, proportion, concentration),
				metrics: []string{"click_count", "conversion_count"}, dims: []string{"source"},
			},
		},
		{
			name: "a misspelled metric fails",
			args: args{policy: policy(typo), metrics: []string{"click_count"}, dims: []string{"source"}},
			want: analysis.ErrPolicyUnobserved,
		},
		{
			name: "a misspelled proportion denominator fails",
			args: args{policy: policy(denominator), metrics: []string{"click_count", "conversion_count"}},
			want: analysis.ErrPolicyUnobserved,
		},
		{
			name: "a misspelled group dimension fails",
			args: args{policy: policy(group), metrics: []string{"click_count"}, dims: []string{"source"}},
			want: analysis.ErrPolicyUnobserved,
		},
		{
			name: "a coverage metric the export lost fails",
			args: args{
				policy:  policy(analysis.RuleSpec{Rule: analysis.RuleCoverage, Metrics: []string{"click_count", "conversion_count"}, Baseline: 1, Window: 1}),
				metrics: []string{"click_count"},
			},
			want: analysis.ErrPolicyUnobserved,
		},
		{
			name: "metrics that only other events carry still pass",
			args: args{
				policy: policy(zscore, analysis.RuleSpec{
					Rule: analysis.RuleZScore, Metrics: []string{"p99_ms"}, Baseline: 1, Window: 1,
				}),
				metrics: []string{"p99_ms", "click_count"}, dims: []string{"region", "source"},
			},
		},
		{
			name: "a data set without events has nothing absent",
			args: args{policy: policy(typo, group)},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.ErrorIs(t, tc.args.policy.Observed(tc.args.metrics, tc.args.dims).CheckObserved(), tc.want)
		})
	}
}

func TestPolicyAnalyzeAbsent(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	click := func(i int) evidence.Point {
		return evidence.Point{Time: start.Add(time.Duration(i) * time.Hour), Metric: "click_count", Value: 10, Dims: map[string]string{"region": "kr"}}
	}
	conversion := evidence.Point{Time: start.Add(time.Hour), Metric: "conversion_count", Value: 1, Dims: map[string]string{"region": "kr"}}
	demo, err := os.ReadFile(filepath.Join(testkit.DemoDir(t), "policy.yaml"))
	require.NoError(t, err)
	policy, err := analysis.LoadPolicy(demo)
	require.NoError(t, err)
	window := analysis.Window{Start: start, End: start.Add(time.Hour), Points: 2}
	ref := analysis.Ref{EventID: "ev", Start: start, End: start.Add(time.Hour)}
	lost := "conversion_count: no event of the data set carries this metric so a data outage or a policy name that matches no metric"
	type args struct {
		metrics, dims []string
		points        []evidence.Point
	}
	tcs := []struct {
		name string
		args args
		want analysis.Observations
	}{
		{
			name: "a metric the export lost is reported once per rule that reads it",
			args: args{metrics: []string{"click_count"}, dims: []string{"region", "source"}, points: []evidence.Point{click(0), click(1)}},
			want: analysis.Observations{
				{Rule: analysis.RuleProportion, Metric: "conversion_count", Window: window, Ref: ref, Summary: lost, Absent: true},
				{Rule: analysis.RuleCoverage, Metric: "conversion_count", Window: window, Ref: ref, Summary: lost, Absent: true},
			},
		},
		{
			name: "a group dimension no event carries is reported for each metric the rule groups",
			args: args{
				metrics: []string{"click_count", "conversion_count"}, dims: []string{"region"},
				points: []evidence.Point{click(0), click(1)},
			},
			want: analysis.Observations{{
				Rule: analysis.RuleConcentration, Metric: "click_count", Window: window, Ref: ref,
				Summary: "click_count: no event of the data set carries dimension source to group by", Absent: true,
			}},
		},
		{
			name: "an event that carries the metric after the policy was bound reports nothing absent",
			args: args{metrics: []string{"click_count"}, dims: []string{"region", "source"}, points: []evidence.Point{click(0), conversion}},
		},
		{
			name: "a data set that carries every name reports nothing absent",
			args: args{
				metrics: []string{"click_count", "conversion_count"}, dims: []string{"region", "source"},
				points: []evidence.Point{click(0), click(1)},
			},
		},
		{
			name: "a data set without events reports nothing absent",
			args: args{points: []evidence.Point{click(0), click(1)}},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := policy.Observed(tc.args.metrics, tc.args.dims).Analyze(evidence.Event{ID: "ev", Points: tc.args.points})
			require.NoError(t, err)
			require.GreaterOrEqual(t, len(got), len(tc.want))
			var absent analysis.Observations
			for _, o := range got {
				if o.Absent {
					absent = append(absent, o)
				}
			}
			assert.Equal(t, tc.want, absent)
			assert.Equal(t, tc.want, append(analysis.Observations(nil), got[len(got)-len(tc.want):]...), "absent rows sort last")
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

func TestObservationsMeasured(t *testing.T) {
	tcs := []struct {
		name string
		args analysis.Observations
		want []string
	}{
		{
			name: "an absent row adds no metric so a quiet event stays quiet",
			args: analysis.Observations{{Rule: analysis.RuleCoverage, Metric: "conversion_count", Absent: true}},
			want: []string{},
		},
		{
			name: "a metric observed beside its absent group dimension stays",
			args: analysis.Observations{
				{Rule: analysis.RuleZScore, Metric: "click_count"},
				{Rule: analysis.RuleConcentration, Metric: "click_count", Absent: true},
				{Rule: analysis.RuleZScore, Metric: "conversion_count", Absent: true},
			},
			want: []string{"click_count"},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Measured())
		})
	}
}

func TestObservationsMoved(t *testing.T) {
	type want struct {
		metrics []string
		series  []evidence.SeriesRef
	}
	onA := map[string]string{"source": "source-a", "topic": "shopping"}
	group := map[string]string{"source": "source-a"}
	tcs := []struct {
		name string
		args analysis.Observations
		want want
	}{
		{
			name: "adequate observations name their metric once and each series with its target",
			args: analysis.Observations{
				{Rule: analysis.RuleZScore, Metric: "click_count", Target: onA, Adequate: true},
				{Rule: analysis.RuleConcentration, Metric: "click_count", Target: group, Adequate: true},
			},
			want: want{
				metrics: []string{"click_count"},
				series:  []evidence.SeriesRef{{Metric: "click_count", Dims: onA}, {Metric: "click_count", Dims: group}},
			},
		},
		{
			name: "a diluted group names no moved series beside an undiluted one",
			args: analysis.Observations{
				{Rule: analysis.RuleConcentration, Metric: "click_count", Target: group, Adequate: true},
				{
					Rule: analysis.RuleConcentration, Metric: "click_count", Target: map[string]string{"source": "source-b"},
					Adequate: true, Detail: analysis.Detail{Diluted: true},
				},
			},
			want: want{
				metrics: []string{"click_count"},
				series:  []evidence.SeriesRef{{Metric: "click_count", Dims: group}},
			},
		},
		{
			name: "a metric whose only group is diluted still moves but names no series",
			args: analysis.Observations{
				{
					Rule: analysis.RuleConcentration, Metric: "click_count", Target: map[string]string{"source": "source-d"},
					Adequate: true, Detail: analysis.Detail{Diluted: true},
				},
			},
			want: want{metrics: []string{"click_count"}},
		},
		{
			name: "inadequate observations do not move a metric",
			args: analysis.Observations{{Rule: analysis.RuleProportion, Metric: "conversion_count", Target: onA}},
		},
		{
			name: "coverage gaps do not move a metric",
			args: analysis.Observations{{Rule: analysis.RuleCoverage, Metric: "conversion_count", Target: onA, Adequate: true}},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, want{metrics: tc.args.Moved(), series: tc.args.MovedSeries()})
		})
	}
}

func TestAnalyzeDemoSetFindsEveryLabeledAnomaly(t *testing.T) {
	src, err := file.New(testkit.DemoDir(t), evidence.DefaultContexts())
	require.NoError(t, err)
	policy := testkit.Policy(t)
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
			got, err := policy.Analyze(ev)
			require.NoError(t, err)
			missed := func(a evidence.SeriesRef) bool {
				return !slices.ContainsFunc(got, func(o analysis.Observation) bool { return flags(o, a) })
			}
			assert.Equal(t, l.Anomalies, slices.DeleteFunc(slices.Clone(l.Anomalies), missed))
		})
	}
}

func TestAnalyzeDemoSetMovesNothingOnQuietEvents(t *testing.T) {
	src, err := file.New(testkit.DemoDir(t), evidence.DefaultContexts())
	require.NoError(t, err)
	policy := testkit.Policy(t)
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
			got, err := policy.Analyze(ev)
			require.NoError(t, err)
			assert.Nil(t, got.Moved())
		})
	}
}
