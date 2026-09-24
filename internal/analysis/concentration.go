package analysis

import (
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/evidence"
)

// Share of each group in the window against its share in the baseline
// 1. share is group sum over the total sum of every group
// 2. Change is the share delta so a gaining group is positive and a losing one negative
// 3. series without the group dimension cannot be attributed and are ignored
// 4. adequacy is judged on the baseline points of every group together because shares are relative
func (spec RuleSpec) concentrationChange(eventID string, all series) []Observation {
	metric := spec.Metrics[0]
	g := spec.group(all, metric)
	if len(g.names) == 0 {
		return nil
	}
	reason := spec.inadequacy(g.baselineSamples)
	if reason == "" {
		reason = g.inadequacy()
	}
	var out []Observation
	for _, name := range g.names {
		obs := Observation{
			Rule:   RuleConcentration,
			Target: map[string]string{spec.GroupBy: name},
			Metric: metric,
			Window: g.windowPoints.window(),
			Detail: Detail{Samples: g.samples, PeakTime: g.peaks[name].Time, PeakValue: g.peaks[name].Value},
			Ref:    g.windowPoints.ref(eventID),
		}
		title := obs.name()
		if reason != "" {
			obs.Summary = title + ": " + reason
			out = append(out, obs)
			continue
		}
		obs.Baseline, obs.Current = g.share(name)
		delta := obs.Current - obs.Baseline
		if math.Abs(delta) < spec.Threshold {
			continue
		}
		obs.Adequate = true
		obs.Change = delta
		obs.Severity = spec.severity(delta)
		obs.Summary = fmt.Sprintf("%s: window share %s against baseline %s, delta %s, peak %s at %s", title,
			formatNumber(obs.Current), formatNumber(obs.Baseline), formatNumber(delta), formatNumber(g.peaks[name].Value),
			g.peaks[name].Time.UTC().Format(time.RFC3339))
		out = append(out, obs)
	}
	return out
}

// Sums per group over the baseline and the window in first seen order
type groups struct {
	names            []string
	baseline, window map[string]float64
	// Highest window point of each group
	peaks map[string]evidence.Point
	// Window points of every group in time order
	// Adequacy and the range are judged on them together
	windowPoints series
	// Points of every group across the baseline and the window
	samples int
	// Baseline points of every group judged against MinSamples
	baselineSamples int
	// Sums over every group so each share divides by the same total
	baselineTotal, windowTotal float64
}

func (spec RuleSpec) group(all series, metric string) groups {
	g := groups{baseline: map[string]float64{}, window: map[string]float64{}, peaks: map[string]evidence.Point{}}
	for _, points := range all.bySeries(metric) {
		name, ok := points[0].Dims[spec.GroupBy]
		if !ok {
			continue
		}
		if _, seen := g.baseline[name]; !seen {
			g.names = append(g.names, name)
		}
		baseline, window := spec.split(points)
		g.baseline[name] += baseline.sum()
		g.window[name] += window.sum()
		g.samples += len(points)
		g.baselineSamples += len(baseline)
		g.windowPoints = append(g.windowPoints, window...)
		for _, p := range window {
			if peak, ok := g.peaks[name]; !ok || p.Value > peak.Value {
				g.peaks[name] = p
			}
		}
	}
	slices.SortStableFunc(g.windowPoints, evidence.Point.Compare)
	for _, name := range g.names {
		g.baselineTotal += g.baseline[name]
		g.windowTotal += g.window[name]
	}
	return g
}

// Why no share can be read from the totals
// Empty when it can
func (g groups) inadequacy() string {
	switch {
	case g.baselineTotal == 0:
		return "baseline total is zero"
	case g.windowTotal == 0:
		return "window total is zero"
	}
	return ""
}

// Baseline share and window share of one group
func (g groups) share(name string) (baseline, window float64) {
	return g.baseline[name] / g.baselineTotal, g.window[name] / g.windowTotal
}
