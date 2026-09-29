package eval

import (
	"fmt"
	"maps"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
)

// A per event metric the paired bootstrap compares
type Metric string

const (
	MetricStatusAccuracy Metric = "status_accuracy" // one when the status matches the label
	MetricMisapplied     Metric = "misapplied"      // knowledge ids used that the label does not expect
)

// The valid metrics in report order
func metrics() []Metric {
	return []Metric{MetricStatusAccuracy, MetricMisapplied}
}

// Not a number for a metric outside the set so a stray value never passes for a score
func (m Metric) value(score Score) float64 {
	switch m {
	case MetricStatusAccuracy:
		if score.StatusOK {
			return 1
		}
		return 0
	case MetricMisapplied:
		return float64(score.Misapplications)
	default:
		return math.NaN()
	}
}

// The percentile bootstrap settings every interval shares
const (
	bootstrapMethod     = "paired event-cluster percentile bootstrap"
	bootstrapConfidence = 0.95
	bootstrapLower      = 0.025
	bootstrapUpper      = 0.975
	bootstrapResamples  = 10000
	bootstrapSeed       = 0
)

// The paired difference of one metric between two conditions with its percentile interval
type Interval struct {
	// The condition earlier in report order
	Reference  Condition `json:"reference"`
	Condition  Condition `json:"condition"`
	Metric     Metric    `json:"metric"`
	Events     int       `json:"events"`
	Pairs      int       `json:"pairs"`
	Difference float64   `json:"difference"`
	Lower      float64   `json:"lower"`
	Upper      float64   `json:"upper"`
	Confidence float64   `json:"confidence"`
	Resamples  int       `json:"resamples"`
	Seed       uint64    `json:"seed"`
	Method     string    `json:"method"`
}

// The repeats of a session in repeat order
type runs []RunReport

// One interval per holdout condition pair and metric over every repeat
// An event is one cluster so the repeats of an event never count as independent events
func (rs runs) intervals() intervals {
	conditions := holdoutConditions()
	var out intervals
	for i, reference := range conditions {
		for _, condition := range conditions[i+1:] {
			matched := rs.matched(reference, condition)
			if len(matched) == 0 {
				continue
			}
			for _, metric := range metrics() {
				interval := matched.clusters(metric).interval()
				interval.Reference, interval.Condition, interval.Metric = reference, condition, metric
				interval.Pairs = len(matched)
				out = append(out, interval)
			}
		}
	}
	return out
}

// The scores of one event under two conditions in the same repeat
type matchedScores struct {
	reference, condition Score
}

// Every event and repeat that both conditions scored
type matches []matchedScores

func (rs runs) matched(reference, condition Condition) matches {
	var out matches
	for _, run := range rs {
		base := scores(run.Scores).of(reference).byEvent()
		for _, score := range scores(run.Scores).of(condition) {
			if original, ok := base[score.EventID]; ok {
				out = append(out, matchedScores{reference: original, condition: score})
			}
		}
	}
	return out
}

// The mean difference per event over its repeats in event order so every event weighs the same
func (ms matches) clusters(metric Metric) clusters {
	totals, counts := map[string]float64{}, map[string]int{}
	for _, m := range ms {
		totals[m.condition.EventID] += metric.value(m.condition) - metric.value(m.reference)
		counts[m.condition.EventID]++
	}
	ids := slices.Sorted(maps.Keys(totals))
	out := make(clusters, 0, len(ids))
	for _, id := range ids {
		out = append(out, totals[id]/float64(counts[id]))
	}
	return out
}

// Per event mean differences
type clusters []float64

// The mean of the clusters with the percentiles of the resampled means
func (cs clusters) interval() Interval {
	interval := Interval{Events: len(cs), Confidence: bootstrapConfidence, Resamples: bootstrapResamples,
		Seed: bootstrapSeed, Method: bootstrapMethod}
	for _, value := range cs {
		interval.Difference += value / float64(len(cs))
	}
	rng := rand.New(rand.NewPCG(interval.Seed, 0))
	resampled := make(means, interval.Resamples)
	for i := range resampled {
		for range cs {
			resampled[i] += cs[rng.IntN(len(cs))] / float64(len(cs))
		}
	}
	slices.Sort(resampled)
	interval.Lower = resampled.quantile(bootstrapLower)
	interval.Upper = resampled.quantile(bootstrapUpper)
	return interval
}

// Resampled means in ascending order
type means []float64

// Linear interpolation between the two nearest ranks
func (ms means) quantile(probability float64) float64 {
	position := probability * float64(len(ms)-1)
	left := int(position)
	weight := position - float64(left)
	return ms[left]*(1-weight) + ms[min(left+1, len(ms)-1)]*weight
}

// Intervals in report order
type intervals []Interval

func (is intervals) table() string {
	if len(is) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n%s%s: %.0f%% interval, %d resamples, seed %d\n",
		strings.ToUpper(bootstrapMethod[:1]), bootstrapMethod[1:], bootstrapConfidence*100, bootstrapResamples, bootstrapSeed)
	b.WriteString("Each event is one cluster; repeated reviews do not increase independent event count.\n\n")
	b.WriteString("| reference | condition | metric | events | pairs | difference | lower | upper |\n" +
		"|---|---|---|---|---|---|---|---|\n")
	for _, interval := range is {
		fmt.Fprintf(&b, "| %s | %s | %s | %d | %d | %.3f | %.3f | %.3f |\n",
			interval.Reference, interval.Condition, interval.Metric, interval.Events, interval.Pairs,
			interval.Difference, interval.Lower, interval.Upper)
	}
	return b.String()
}
