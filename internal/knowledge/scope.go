package knowledge

import (
	"maps"
	"slices"
	"strings"

	"github.com/jeon-jihyeon/nodloop/internal/evidence"
)

// The procedure scope with the dims a knowledge item may also name
// JSON keys stay change_contexts and metrics and dims
type Scope struct {
	evidence.Scope
	Dims map[string]string `json:"dims,omitempty"`
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

// Whether the event fits the procedure scope and carries every dim value the scope names
func (s Scope) admits(changeContext evidence.Context, moved Moved, dims Dims) bool {
	if !s.Matches(changeContext, moved) {
		return false
	}
	for key, value := range s.Dims {
		if !dims.has(key, value) {
			return false
		}
	}
	return true
}

// Two scopes overlap when every set axis shares a value or is empty on either side
// Each dimension key is an axis of its own
func (s Scope) overlaps(other Scope) bool {
	for key, value := range s.Dims {
		if theirs, ok := other.Dims[key]; ok && theirs != value {
			return false
		}
	}
	return s.Intersects(other.Scope)
}

// One line for a candidate list and the context text
func (s Scope) String() string {
	var parts []string
	if !s.Empty() {
		parts = append(parts, s.Scope.String())
	}
	for _, k := range slices.Sorted(maps.Keys(s.Dims)) {
		parts = append(parts, k+"="+s.Dims[k])
	}
	if len(parts) == 0 {
		return "any event"
	}
	return strings.Join(parts, ". ")
}
