package file

import "context"

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
