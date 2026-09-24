package knowledge

import (
	"maps"
	"slices"
	"strings"

	"github.com/jeon-jihyeon/nodloop/internal/evidence"
)

type Scope struct {
	ChangeContexts []evidence.Context `json:"change_contexts,omitempty"`
	Metrics        []string           `json:"metrics,omitempty"`
	Dims           map[string]string  `json:"dims,omitempty"`
}

// Metrics an observation flagged on the event
// A metric scope matches those and not every series the event carries
type Moved []string

// Dimension values the event carries keyed by dimension name
type Dims map[string]map[string]struct{}

func (d Dims) has(key, value string) bool {
	_, ok := d[key][value]
	return ok
}

func (s Scope) matches(changeContext evidence.Context, moved Moved, dims Dims) bool {
	if len(s.ChangeContexts) > 0 && !slices.Contains(s.ChangeContexts, changeContext) {
		return false
	}
	if len(s.Metrics) > 0 && !sharesValue(s.Metrics, moved) {
		return false
	}
	for key, value := range s.Dims {
		if !dims.has(key, value) {
			return false
		}
	}
	return true
}

// Two scopes intersect when every set axis shares a value or is empty on either side
// Each dimension key is an axis of its own
func (s Scope) intersects(other Scope) bool {
	for key, value := range s.Dims {
		if theirs, ok := other.Dims[key]; ok && theirs != value {
			return false
		}
	}
	contexts := len(s.ChangeContexts) == 0 || len(other.ChangeContexts) == 0 ||
		sharesValue(s.ChangeContexts, other.ChangeContexts)
	metrics := len(s.Metrics) == 0 || len(other.Metrics) == 0 || sharesValue(s.Metrics, other.Metrics)
	return contexts && metrics
}

// Whether a value of ours is also one of theirs
func sharesValue[S ~[]E, E comparable](ours []E, theirs S) bool {
	for _, v := range ours {
		if slices.Contains(theirs, v) {
			return true
		}
	}
	return false
}

// One line for a candidate list and the context text
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
	for _, k := range slices.Sorted(maps.Keys(s.Dims)) {
		parts = append(parts, k+"="+s.Dims[k])
	}
	if len(parts) == 0 {
		return "any event"
	}
	return strings.Join(parts, ". ")
}
