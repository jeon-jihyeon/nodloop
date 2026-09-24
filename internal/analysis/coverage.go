package analysis

import (
	"fmt"
	"math"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/evidence"
)

// Empty slots in the window against the cadence of the baseline
// 1. cadence is the most common gap between consecutive baseline timestamps
// 2. a series shorter than the window has no baseline so the cadence comes from the window itself
// 3. the window is Window slots at that cadence ending at the last timestamp of the event so a series that stops early shows its tail gap
// 4. a point fills the slot nearest to it and Change is the empty slots over the window size
func (spec RuleSpec) coverageRule(eventID string, all series) []Observation {
	var out []Observation
	end := all.window().End
	for _, metric := range spec.Metrics {
		for _, points := range all.bySeries(metric) {
			if obs := spec.coverage(eventID, points, end); obs != nil {
				out = append(out, *obs)
			}
		}
	}
	return out
}

// Observation of one series whose window ends at end
// Nil when the series is adequate and its gaps stay under the threshold
func (spec RuleSpec) coverage(eventID string, points series, end time.Time) *Observation {
	baseline, window := spec.split(points)
	obs := Observation{
		Rule:   RuleCoverage,
		Target: points[0].Dims,
		Metric: points[0].Metric,
		Window: window.window(),
		Detail: Detail{Samples: len(points)},
		Ref:    window.ref(eventID),
	}
	name := obs.name()
	if reason := spec.inadequacy(len(baseline)); reason != "" {
		obs.Summary = name + ": " + reason
		return &obs
	}
	reference := baseline
	if len(reference) < 2 {
		reference = window
	}
	cadence := reference.cadence()
	if cadence == 0 {
		obs.Summary = fmt.Sprintf("%s: cadence unknown with %d points", name, len(points))
		return &obs
	}
	g := points.gaps(spec.Window, cadence, end)
	obs.Window = Window{Start: g.start, End: end, Points: spec.Window - g.missing}
	obs.Ref = Ref{EventID: eventID, Start: g.start, End: end}
	obs.Current, obs.Baseline = float64(spec.Window-g.missing), float64(spec.Window)
	obs.Detail.Missing, obs.Detail.PeakTime, obs.Detail.PeakValue = g.missing, g.longestEnd, float64(g.longest)
	ratio := float64(g.missing) / float64(spec.Window)
	if ratio < spec.Threshold {
		return nil
	}
	obs.Adequate = true
	obs.Change = ratio
	obs.Severity = min(ratio, 1)
	obs.Summary = fmt.Sprintf("%s: %d of %d expected points missing at %s cadence",
		name, g.missing, spec.Window, durationOf(cadence))
	if g.longest > 0 {
		obs.Summary += ", largest gap ends at " + g.longestEnd.UTC().Format(time.RFC3339)
	}
	return &obs
}

// One observation with no numbers for a change context that breaks the baseline comparison
// Samples is the whole event so the reader does not mistake the context flag for an empty series
func (s series) contextObservation(eventID string, changeContext evidence.Context) Observation {
	return Observation{
		Rule:     RuleCoverage,
		Window:   s.window(),
		Change:   1,
		Severity: 1,
		Adequate: true,
		Detail:   Detail{Samples: len(s)},
		Ref:      s.ref(eventID),
		Summary: "change context " + string(changeContext) +
			": the baseline comparison is not trusted until the context is resolved",
	}
}

// Window slots a series leaves empty
type gaps struct {
	// First slot of the window
	start   time.Time
	missing int
	// Slots in the longest run of empty slots
	longest int
	// Last slot of the longest run
	longestEnd time.Time
}

// Slots end at end and step back by cadence
// Missing never exceeds size because each slot counts once
func (s series) gaps(size int, cadence time.Duration, end time.Time) gaps {
	g := gaps{start: end.Add(-time.Duration(size-1) * cadence)}
	filled := make([]bool, size)
	for _, p := range s {
		slot := int(math.Round(float64(p.Time.Sub(g.start)) / float64(cadence)))
		if slot >= 0 && slot < size {
			filled[slot] = true
		}
	}
	run := 0
	for slot, ok := range filled {
		if ok {
			run = 0
			continue
		}
		g.missing++
		run++
		if run > g.longest {
			g.longest, g.longestEnd = run, g.start.Add(time.Duration(slot)*cadence)
		}
	}
	return g
}

func durationOf(d time.Duration) string {
	switch {
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	}
	return d.String()
}
