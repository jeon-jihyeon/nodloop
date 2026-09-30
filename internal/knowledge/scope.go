package knowledge

import (
	"fmt"
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

func (d Dims) Has(key, value string) bool {
	_, ok := d[key][value]
	return ok
}

// Never nil so a data set without dimensions answers an empty list and not null
func (d Dims) Names() []string {
	names := slices.AppendSeq(make([]string, 0, len(d)), maps.Keys(d))
	slices.Sort(names)
	return names
}

// Whether the event fits the procedure scope and carries every dim value the scope names
func (s Scope) admits(changeContext evidence.Context, moved Moved, dims Dims) bool {
	if !s.Matches(changeContext, moved) {
		return false
	}
	for key, value := range s.Dims {
		if !dims.Has(key, value) {
			return false
		}
	}
	return true
}

// Fails with ErrScopeUnobserved naming each metric and each dim value that no event carries
// An item scoped to one of them could never apply
func (s Scope) Observed(metrics []string, dims Dims) error {
	var missing []string
	for _, m := range s.Metrics {
		if !slices.Contains(metrics, m) {
			missing = append(missing, "metric "+m)
		}
	}
	for _, key := range slices.Sorted(maps.Keys(s.Dims)) {
		if !dims.Has(key, s.Dims[key]) {
			missing = append(missing, "dim "+key+"="+s.Dims[key])
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrScopeUnobserved, strings.Join(missing, ", "))
}

// Two scopes overlap when every set axis shares a value or is empty on either side
// Each dimension key is an axis of its own
func (s Scope) overlaps(other Scope) bool {
	return !s.splitByDims(other) && s.Intersects(other.Scope)
}

// Whether both name one dimension key with a different value
func (s Scope) splitByDims(other Scope) bool {
	for key, value := range s.Dims {
		if theirs, ok := other.Dims[key]; ok && theirs != value {
			return true
		}
	}
	return false
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
