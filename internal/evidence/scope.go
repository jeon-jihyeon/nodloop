package evidence

import (
	"slices"
	"strings"
)

// The events a procedure or a knowledge item is written for
// Every set axis must match and an empty axis matches every event
type Scope struct {
	ChangeContexts []Context `json:"change_contexts,omitempty" yaml:"change_contexts"`
	Metrics        []string  `json:"metrics,omitempty" yaml:"metrics"`
}

// Whether no axis is set
func (s Scope) Empty() bool {
	return len(s.ChangeContexts) == 0 && len(s.Metrics) == 0
}

// The change context matches by membership and the metrics by one shared value
func (s Scope) Matches(changeContext Context, metrics []string) bool {
	return s.MatchesContext(changeContext) && s.admitsMetrics(metrics)
}

// Whether some event of the change context may match whatever metrics it carries
func (s Scope) MatchesContext(changeContext Context) bool {
	return s.admitsContexts([]Context{changeContext})
}

// Whether one event could match both scopes
// Each axis intersects or is empty on either side
func (s Scope) Intersects(other Scope) bool {
	contexts := len(other.ChangeContexts) == 0 || s.admitsContexts(other.ChangeContexts)
	metrics := len(other.Metrics) == 0 || s.admitsMetrics(other.Metrics)
	return contexts && metrics
}

// Whether the context axis is unset or holds one of the contexts
func (s Scope) admitsContexts(contexts []Context) bool {
	return len(s.ChangeContexts) == 0 ||
		slices.ContainsFunc(s.ChangeContexts, func(c Context) bool { return slices.Contains(contexts, c) })
}

// Whether the metric axis is unset or holds one of the metrics
func (s Scope) admitsMetrics(metrics []string) bool {
	return len(s.Metrics) == 0 ||
		slices.ContainsFunc(s.Metrics, func(m string) bool { return slices.Contains(metrics, m) })
}

// One line for a list and the context text
func (s Scope) String() string {
	var parts []string
	if len(s.ChangeContexts) > 0 {
		contexts := make([]string, 0, len(s.ChangeContexts))
		for _, c := range s.ChangeContexts {
			contexts = append(contexts, string(c))
		}
		parts = append(parts, "change contexts "+strings.Join(contexts, " or "))
	}
	if len(s.Metrics) > 0 {
		parts = append(parts, "metrics "+strings.Join(s.Metrics, " or "))
	}
	if len(parts) == 0 {
		return "any event"
	}
	return strings.Join(parts, ". ")
}
