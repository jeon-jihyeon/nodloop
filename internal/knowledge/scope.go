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

// What moved on the event at two levels
// A metric scope matches those and not every series the event carries
type Moved struct {
	// Metrics an adequate observation moved
	// A group diluted by the other groups still moves its metric
	Metrics []string
	// Series an adequate observation flagged with the dimension values it targets
	// A diluted group is left out so a dim value the other groups moved never admits a scope
	Series []evidence.SeriesRef
}

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

// Whether the event fits the procedure scope and the dims of the scope
// 1. with metrics only one moved metric fits
// 2. with metrics and dims one moved series must fit both axes so a dim value that only sits in the event never admits it
// 3. without metrics the event must carry every dim value because such an item reads the event whatever moved
func (s Scope) admits(changeContext evidence.Context, moved Moved, dims Dims) bool {
	if !s.Matches(changeContext, moved.Metrics) {
		return false
	}
	if len(s.Metrics) == 0 {
		return s.carried(dims)
	}
	if len(s.Dims) == 0 {
		return true
	}
	for _, ref := range moved.Series {
		if s.admitsSeries(ref, dims) {
			return true
		}
	}
	return false
}

func (s Scope) carried(dims Dims) bool {
	for key, value := range s.Dims {
		if !dims.Has(key, value) {
			return false
		}
	}
	return true
}

// Whether a moved series fits the metric and every dim value of the scope
// A dim the series target does not name falls back to the values the event carries
// A concentration target names only its group dimension
func (s Scope) admitsSeries(ref evidence.SeriesRef, dims Dims) bool {
	if !slices.Contains(s.Metrics, ref.Metric) {
		return false
	}
	for key, value := range s.Dims {
		got, named := ref.Dims[key]
		if named && got != value || !named && !dims.Has(key, value) {
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
