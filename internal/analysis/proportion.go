package analysis

import (
	"fmt"
	"math"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/evidence"
)

// p chart on the window rate against the baseline rate
// 1. the denominator series is the one with the same dimensions as the numerator series
// 2. points pair by timestamp so a missing hour in one metric never shifts the other
// 3. rate is numerator sum over denominator sum for the baseline and for the window
// 4. margin is threshold times the square root of p times 1 minus p over the window denominator sum where p is the baseline rate
// 5. a window rate outside baseline rate plus or minus the margin is reported and Change is the rate ratio
// 6. the peak is the window point whose own rate is farthest from the baseline rate
// 7. a baseline rate of zero has no ratio and one above one is not a proportion so both are inadequate
func (spec RuleSpec) proportionControl(eventID string, all series) []Observation {
	partners := map[string]series{}
	for _, points := range all.bySeries(spec.Metrics[1]) {
		partners[points[0].SeriesKey()] = points
	}
	var out []Observation
	for _, points := range all.bySeries(spec.Metrics[0]) {
		key := evidence.SeriesRef{Metric: spec.Metrics[1], Dims: points[0].Dims}.Key()
		if obs := spec.proportion(eventID, points, partners[key]); obs != nil {
			out = append(out, *obs)
		}
	}
	return out
}

// Observation of one numerator series against the denominator series of the same dimensions
// Nil when the series is adequate and its window rate stays inside the limits
func (spec RuleSpec) proportion(eventID string, points, partners series) *Observation {
	denominator := spec.Metrics[1]
	numerators, denominators, unpaired := points.pair(partners)
	numeratorBaseline, numeratorWindow := spec.split(numerators)
	denominatorBaseline, denominatorWindow := spec.split(denominators)
	obs := Observation{
		Rule:   RuleProportion,
		Target: points[0].Dims,
		Metric: points[0].Metric,
		Window: numeratorWindow.window(),
		Detail: Detail{Samples: len(numerators), Missing: unpaired},
		Ref:    numeratorWindow.ref(eventID),
	}
	name := obs.name()
	baselineTotal, windowTotal := denominatorBaseline.sum(), denominatorWindow.sum()
	if baselineTotal != 0 && windowTotal != 0 {
		obs.Baseline = numeratorBaseline.sum() / baselineTotal
		obs.Current = numeratorWindow.sum() / windowTotal
	}
	switch reason := spec.inadequacy(len(numeratorBaseline)); {
	case reason != "":
		obs.Summary = name + " over " + denominator + ": " + reason
		return &obs
	case baselineTotal == 0 || windowTotal == 0:
		obs.Summary = name + ": " + denominator + " sums to zero"
		return &obs
	case obs.Baseline == 0:
		obs.Summary = name + ": baseline rate is zero"
		return &obs
	case obs.Baseline > 1:
		obs.Summary = name + ": baseline rate is above one"
		return &obs
	}
	margin := spec.Threshold * math.Sqrt(obs.Baseline*(1-obs.Baseline)/windowTotal)
	lower, upper := obs.Baseline-margin, obs.Baseline+margin
	if obs.Current >= lower && obs.Current <= upper {
		return nil
	}
	obs.Adequate = true
	obs.Change = obs.Current / obs.Baseline
	obs.Severity = min(math.Abs(1-obs.Change), 1)
	obs.Detail.PeakTime, obs.Detail.PeakValue = numeratorWindow.peakRate(denominatorWindow, obs.Baseline)
	obs.Summary = fmt.Sprintf("%s: window rate %s against baseline %s over %s %s, limits %s to %s", name,
		formatNumber(obs.Current), formatNumber(obs.Baseline), denominator, formatNumber(windowTotal),
		formatNumber(lower), formatNumber(upper))
	if recent, earlier, ok := numeratorWindow.recent(denominatorWindow, spec.Recent); ok {
		obs.Detail.RecentRate = recent
		obs.Summary += fmt.Sprintf(", newest %d points rate %s against the earlier %d points %s",
			spec.Recent, formatNumber(recent), len(numeratorWindow)-spec.Recent, formatNumber(earlier))
	}
	return &obs
}

// Numerator and denominator points at the times both series have
// The count of points left without a partner goes to the observation so the reader sees what the rate leaves out
func (s series) pair(partners series) (numerators, denominators series, unpaired int) {
	byTime := make(map[time.Time]evidence.Point, len(partners))
	for _, p := range partners {
		byTime[p.Time] = p
	}
	for _, p := range s {
		partner, ok := byTime[p.Time]
		if !ok {
			continue
		}
		delete(byTime, p.Time)
		numerators, denominators = append(numerators, p), append(denominators, partner)
	}
	return numerators, denominators, len(s) + len(partners) - 2*len(numerators)
}

// Rates of the newest n numerator points and of the points before them
// The denominators pair with the numerators by index
// Nothing when n is zero or leaves no earlier point
func (s series) recent(denominators series, n int) (float64, float64, bool) {
	if n <= 0 || n >= len(s) {
		return 0, 0, false
	}
	cut := len(s) - n
	recentTotal, earlierTotal := denominators[cut:].sum(), denominators[:cut].sum()
	if recentTotal == 0 || earlierTotal == 0 {
		return 0, 0, false
	}
	return s[cut:].sum() / recentTotal, s[:cut].sum() / earlierTotal, true
}

// Numerator point whose rate is farthest from the baseline rate
// The denominators pair with the numerators by index
// Points without a positive denominator have no rate and are skipped
func (s series) peakRate(denominators series, baseline float64) (time.Time, float64) {
	var at time.Time
	var rate float64
	found := false
	for i, p := range s {
		denominator := denominators[i].Value
		if denominator <= 0 {
			continue
		}
		r := p.Value / denominator
		if !found || math.Abs(r-baseline) > math.Abs(rate-baseline) {
			at, rate, found = p.Time, r, true
		}
	}
	return at, rate
}
