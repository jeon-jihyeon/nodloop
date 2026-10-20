package trace

import (
	"fmt"
	"maps"
	"slices"
)

// Situation of a run as keys each holding values
// A run holds one value per key and a knowledge scope may list several, any of which matches
type Labels map[string][]string

// The key of the agent host a session hook ran in such as claude-code or codex
// It names the tool and not the place of the work
const LabelHost = "host"

// The labels of the place without the host
// A scope filled from a run takes these so a lesson approved in one host reaches the same place in every host
// Nil stays nil
func (l Labels) Place() Labels {
	if _, ok := l[LabelHost]; !ok {
		return l
	}
	out := maps.Clone(l)
	delete(out, LabelHost)
	return out
}

// The same labels with each value list sorted and deduplicated
// An empty key or value fails because a scope could never name it
func (l Labels) Normalized() (Labels, error) {
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

// The labels of one run: normalized and one value per key
// Two values under one key would let two items split by that key both reach the run while they never overlap
func (l Labels) runLabels() (Labels, error) {
	out, err := l.Normalized()
	if err != nil {
		return nil, err
	}
	for key, values := range out {
		if len(values) > 1 {
			return nil, fmt.Errorf("%w: %s has %d values and a run has one per key", ErrLabelValues, key, len(values))
		}
	}
	return out, nil
}

// Whether the run carries the value under the key
func (l Labels) Has(key, value string) bool {
	return slices.Contains(l[key], value)
}

// Every label key and value the run traces of the producer carry
// A scope may name only these so a typo fails before it becomes an item that matches nothing
func (ts Traces) Vocabulary(producer string) Labels {
	out := Labels{}
	for _, t := range ts {
		if t.Name != NameRun || t.Producer != producer {
			continue
		}
		for key, values := range t.Labels {
			for _, v := range values {
				if !out.Has(key, v) {
					out[key] = append(out[key], v)
				}
			}
		}
	}
	return out
}
