package analysis

import (
	"maps"
	"math"
	"slices"

	"github.com/jeon-jihyeon/nodloop/internal/evidence"
)

// What a policy is built from
// Counted by the rules the analyzers use so whoever proposes a policy never recounts them another way
type Profile struct {
	// The most common series length with ties to the shorter
	// An event with a data gap never shrinks the window of every other event
	Points int `json:"points"`
	// The most common gap of a series of that length
	Cadence string `json:"cadence"`
	// In first seen order
	Metrics []MetricKind `json:"metrics"`
	// In name order
	Dimensions []DimensionKind `json:"dimensions"`
}

type MetricKind struct {
	Name string `json:"name"`
	// Every value is whole and not negative
	Count bool `json:"count"`
}

type DimensionKind struct {
	Name string `json:"name"`
	// Distinct values across every event
	Values int `json:"values"`
}

func NewProfile(events []evidence.Event) Profile {
	var all []series
	var metrics []string
	counts := map[string]bool{}
	values := map[string]map[string]struct{}{}
	for _, ev := range events {
		points := series(ev.Points)
		for _, m := range points.metrics() {
			if _, seen := counts[m]; !seen {
				metrics, counts[m] = append(metrics, m), true
			}
			for _, one := range points.bySeries(m) {
				counts[m] = counts[m] && one.counts()
				all = append(all, one)
			}
		}
		for name, vs := range ev.Dims() {
			if values[name] == nil {
				values[name] = map[string]struct{}{}
			}
			maps.Copy(values[name], vs)
		}
	}
	typical := seriesSet(all).typical()
	p := Profile{
		Points: len(typical), Cadence: typical.cadence().String(),
		Metrics: make([]MetricKind, 0, len(metrics)), Dimensions: make([]DimensionKind, 0, len(values)),
	}
	for _, m := range metrics {
		p.Metrics = append(p.Metrics, MetricKind{Name: m, Count: counts[m]})
	}
	for _, name := range slices.Sorted(maps.Keys(values)) {
		p.Dimensions = append(p.Dimensions, DimensionKind{Name: name, Values: len(values[name])})
	}
	return p
}

func (p Profile) MetricNames() []string {
	out := make([]string, 0, len(p.Metrics))
	for _, m := range p.Metrics {
		out = append(out, m.Name)
	}
	return out
}

func (p Profile) DimensionNames() []string {
	out := make([]string, 0, len(p.Dimensions))
	for _, d := range p.Dimensions {
		out = append(out, d.Name)
	}
	return out
}

// Every series of a data set
type seriesSet []series

// The first series of the most common length with ties to the shorter
// Empty when there is none
func (ss seriesSet) typical() series {
	lengths := map[int]int{}
	for _, s := range ss {
		lengths[len(s)]++
	}
	best := -1
	for n, c := range lengths {
		if best < 0 || c > lengths[best] || (c == lengths[best] && n < best) {
			best = n
		}
	}
	i := slices.IndexFunc(ss, func(s series) bool { return len(s) == best })
	if i < 0 {
		return nil
	}
	return ss[i]
}

// The metrics of the points in first seen order
func (s series) metrics() []string {
	var out []string
	for _, p := range s {
		if !slices.Contains(out, p.Metric) {
			out = append(out, p.Metric)
		}
	}
	return out
}

// Whether every value is whole and not negative
func (s series) counts() bool {
	for _, p := range s {
		if p.Value < 0 || p.Value != math.Trunc(p.Value) {
			return false
		}
	}
	return true
}
