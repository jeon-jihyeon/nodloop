package analysis

import (
	"fmt"
	"slices"
)

// A metric or group dimension an analyzer reads that no event of the data set carries
// The data cannot tell a misspelled name from a metric an export lost during an outage
type absence struct {
	rule   Rule
	metric string
	// Empty when the metric itself is absent
	dim string
}

func (a absence) err() error {
	if a.dim != "" {
		return fmt.Errorf("%w: %s reads dimension %q", ErrPolicyUnobserved, a.rule, a.dim)
	}
	return fmt.Errorf("%w: %s reads metric %q", ErrPolicyUnobserved, a.rule, a.metric)
}

// The policy bound to the metrics and dimensions a data set carries
// 1. a data set without events has nothing to analyze so nothing is absent
// 2. checked over the data set because one event cannot tell an absent name from a metric only other events carry
func (p Policy) Observed(metrics, dims []string) Policy {
	out := Policy{Version: p.Version, Contexts: p.Contexts, Analyzers: p.Analyzers, run: p.run}
	if len(metrics) == 0 {
		return out
	}
	for _, spec := range p.Analyzers {
		for _, metric := range spec.Metrics {
			if !slices.Contains(metrics, metric) {
				out.absent = append(out.absent, absence{rule: spec.Rule, metric: metric})
			}
		}
		if spec.GroupBy == "" || slices.Contains(dims, spec.GroupBy) {
			continue
		}
		for _, metric := range spec.Metrics {
			out.absent = append(out.absent, absence{rule: spec.Rule, metric: metric, dim: spec.GroupBy})
		}
	}
	return out
}

// Fails on the first name Observed found absent
// For a caller that must refuse a misspelled name before any review runs
func (p Policy) CheckObserved() error {
	if len(p.absent) == 0 {
		return nil
	}
	return p.absent[0].err()
}

// Whether a point of the event carries the absent name
// Data that came back after the policy was bound stops the report
func (s series) carries(a absence) bool {
	for _, p := range s {
		if _, ok := p.Dims[a.dim]; a.dim != "" && ok {
			return true
		}
		if a.dim == "" && p.Metric == a.metric {
			return true
		}
	}
	return false
}

// Inadequate so the review names the absence and the procedures of the metric still apply
func (s series) absenceObservation(eventID string, a absence) Observation {
	summary := a.metric + ": no event of the data set carries this metric so a data outage or a policy name that matches no metric"
	if a.dim != "" {
		summary = a.metric + ": no event of the data set carries dimension " + a.dim + " to group by"
	}
	return Observation{Rule: a.rule, Metric: a.metric, Window: s.window(), Ref: s.ref(eventID), Summary: summary, Absent: true}
}
