package analysis

import (
	"fmt"
	"math"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/evidence"
)

// Peak z of the window against the baseline mean and population stddev
// 1. the peak is the window point farthest from the baseline mean in either direction
// 2. Change keeps the sign so a drop reads as a negative z
// 3. a flat baseline has no scale for z so the series is inadequate rather than infinite
func (spec RuleSpec) zscore(eventID string, all series) []Observation {
	var out []Observation
	for _, points := range all.bySeries(spec.Metrics[0]) {
		if obs := spec.peakZ(eventID, points); obs != nil {
			out = append(out, *obs)
		}
	}
	return out
}

// Observation of one series
// Nil when the series is adequate and its peak z stays under the threshold
func (spec RuleSpec) peakZ(eventID string, points series) *Observation {
	baseline, window := spec.split(points)
	mean := baseline.mean()
	obs := Observation{
		Rule:     RuleZScore,
		Target:   points[0].Dims,
		Metric:   points[0].Metric,
		Window:   window.window(),
		Current:  window.mean(),
		Baseline: mean,
		Detail:   Detail{Samples: len(points)},
		Ref:      window.ref(eventID),
	}
	name := obs.name()
	if reason := spec.inadequacy(len(baseline)); reason != "" {
		obs.Summary = name + ": " + reason
		return &obs
	}
	stddev := baseline.stddev(mean)
	if stddev == 0 {
		obs.Summary = name + ": baseline stddev is zero"
		return &obs
	}
	peak := window.farthest(mean)
	z := (peak.Value - mean) / stddev
	if math.Abs(z) < spec.Threshold {
		return nil
	}
	obs.Adequate = true
	obs.Change = z
	obs.Severity = spec.severity(z)
	obs.Detail.PeakTime = peak.Time
	obs.Detail.PeakValue = peak.Value
	obs.Summary = fmt.Sprintf("%s: window mean %s against baseline %s, peak z %s at %s", name,
		formatNumber(obs.Current), formatNumber(mean), formatNumber(z), peak.Time.UTC().Format(time.RFC3339))
	return &obs
}

// Point farthest from mean in either direction
// The earliest point wins a tie
func (s series) farthest(mean float64) evidence.Point {
	peak := s[0]
	for _, p := range s[1:] {
		if math.Abs(p.Value-mean) > math.Abs(peak.Value-mean) {
			peak = p
		}
	}
	return peak
}
