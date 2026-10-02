package trace

import (
	"fmt"
	"slices"
)

// Situation of a run as keys each holding one or more values
// A knowledge scope matches it by key and value
type Labels map[string][]string

// The same labels with each value list sorted and deduplicated
// An empty key or value fails because a scope could never name it
func (l Labels) normalized() (Labels, error) {
	if len(l) == 0 {
		return nil, nil
	}
	out := make(Labels, len(l))
	for key, values := range l {
		if key == "" {
			return nil, ErrLabelEmpty
		}
		if len(values) == 0 {
			return nil, fmt.Errorf("%w: %s has no value", ErrLabelEmpty, key)
		}
		for _, v := range values {
			if v == "" {
				return nil, fmt.Errorf("%w: %s has an empty value", ErrLabelEmpty, key)
			}
		}
		sorted := slices.Clone(values)
		slices.Sort(sorted)
		out[key] = slices.Compact(sorted)
	}
	return out, nil
}

// Whether the run carries the value under the key
func (l Labels) Has(key, value string) bool {
	return slices.Contains(l[key], value)
}
