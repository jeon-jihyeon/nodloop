package analysis_test

import (
	"maps"
	"os"
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

// tq-017 and tq-018 hold series of 39 points that must not move the length of the demo
func TestProfileDemo(t *testing.T) {
	src, err := file.New(testkit.DemoDir(t), testkit.Policy(t).Contexts)
	require.NoError(t, err)
	all, err := src.All(t.Context())
	require.NoError(t, err)
	gaps := slices.DeleteFunc(slices.Clone(all), func(ev evidence.Event) bool { return ev.ID != "tq-017" && ev.ID != "tq-018" })
	tcs := []struct {
		name string
		args []evidence.Event
		want int
	}{
		{"the gap events alone hold 39 points", gaps, 39},
		{"every event holds 48 points", all, 48},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := analysis.NewProfile(tc.args)

			assert.Equal(t, analysis.Profile{
				Points: tc.want, Cadence: "1h0m0s",
				Metrics:    []analysis.MetricKind{{Name: "click_count", Count: true}, {Name: "conversion_count", Count: true}},
				Dimensions: got.Dimensions,
			}, got)
		})
	}
	assert.Equal(t, []analysis.DimensionKind{{Name: "source", Values: 3}, {Name: "topic", Values: 1}}, analysis.NewProfile(all).Dimensions)
}

func TestNewProfile(t *testing.T) {
	at := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	event := func(id string, n int, metric string, value float64, dims map[string]string) evidence.Event {
		points := make([]evidence.Point, 0, n)
		for i := range n {
			points = append(points, evidence.Point{Time: at.Add(time.Duration(i) * 10 * time.Minute), Metric: metric, Value: value, Dims: dims})
		}
		return evidence.Event{ID: id, Points: points}
	}
	tcs := []struct {
		name string
		args []evidence.Event
		want analysis.Profile
	}{
		{
			"a continuous metric is not a count",
			[]evidence.Event{event("e", 20, "latency_ms", 12.5, map[string]string{"region": "kr"})},
			analysis.Profile{
				Points: 20, Cadence: "10m0s", Metrics: []analysis.MetricKind{{Name: "latency_ms"}},
				Dimensions: []analysis.DimensionKind{{Name: "region", Values: 1}},
			},
		},
		{
			"a negative value is not a count",
			[]evidence.Event{event("e", 3, "delta", -2, nil)},
			analysis.Profile{Points: 3, Cadence: "10m0s", Metrics: []analysis.MetricKind{{Name: "delta"}}, Dimensions: []analysis.DimensionKind{}},
		},
		{
			"a shorter series loses only to a more common length",
			[]evidence.Event{
				event("a", 16, "orders", 3, map[string]string{"shop": "s1"}),
				event("b", 16, "orders", 4, map[string]string{"shop": "s2"}),
				event("c", 9, "orders", 1, map[string]string{"shop": "s3"}),
			},
			analysis.Profile{
				Points: 16, Cadence: "10m0s", Metrics: []analysis.MetricKind{{Name: "orders", Count: true}},
				Dimensions: []analysis.DimensionKind{{Name: "shop", Values: 3}},
			},
		},
		{
			"a tie goes to the shorter length",
			[]evidence.Event{event("a", 16, "orders", 3, nil), event("b", 9, "orders", 1, nil)},
			analysis.Profile{Points: 9, Cadence: "10m0s", Metrics: []analysis.MetricKind{{Name: "orders", Count: true}}, Dimensions: []analysis.DimensionKind{}},
		},
		{
			"no events give an empty profile",
			nil,
			analysis.Profile{Cadence: "0s", Metrics: []analysis.MetricKind{}, Dimensions: []analysis.DimensionKind{}},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, analysis.NewProfile(tc.args))
		})
	}
}

// The policy the setup skill writes from its defaults over the demo profile
func TestProposedPolicyDemo(t *testing.T) {
	src, err := file.New(testkit.DemoDir(t), evidence.DefaultContexts())
	require.NoError(t, err)
	b, err := os.ReadFile("testdata/proposed-demo.yaml")
	require.NoError(t, err)

	policy, err := analysis.LoadPolicy(b)

	require.NoError(t, err)
	metrics, err := src.Metrics(t.Context())
	require.NoError(t, err)
	dims, err := src.Dims(t.Context())
	require.NoError(t, err)
	assert.NoError(t, policy.Observed(metrics, slices.Collect(maps.Keys(dims))).CheckObserved())
	for _, c := range evidence.DefaultContexts() {
		assert.Equal(t, c.BreaksBaseline, policy.Contexts.Breaks(c.Name), c.Name)
	}
	assert.Len(t, policy.Contexts, len(evidence.DefaultContexts()))
}
