package file

import (
	"context"
	"maps"
)

// Every metric of the events in first seen order
// Reads contexts.csv as an event read does so a broken file fails here too
// Without events there is nothing a context could apply to so contexts.csv is not read
func (s *Source) Metrics(_ context.Context) ([]string, error) {
	events, err := s.loadEvents()
	if err != nil {
		return nil, err
	}
	if len(events) > 0 {
		if _, err := s.loadContexts(); err != nil {
			return nil, err
		}
	}
	seen := map[string]bool{}
	var metrics []string
	for _, ev := range events {
		for _, point := range ev.Points {
			if !seen[point.Metric] {
				metrics = append(metrics, point.Metric)
				seen[point.Metric] = true
			}
		}
	}
	return metrics, nil
}

// Every dimension value of the events keyed by dimension name
func (s *Source) Dims(_ context.Context) (map[string]map[string]struct{}, error) {
	events, err := s.loadEvents()
	if err != nil {
		return nil, err
	}
	dims := map[string]map[string]struct{}{}
	for _, ev := range events {
		for key, values := range ev.Dims() {
			if dims[key] == nil {
				dims[key] = map[string]struct{}{}
			}
			maps.Copy(dims[key], values)
		}
	}
	return dims, nil
}
