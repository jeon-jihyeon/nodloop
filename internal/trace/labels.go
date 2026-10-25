package trace

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
)

// Situation of a run as keys each holding values
// A run holds one value per key and a knowledge scope may list several matching alternatives
type Labels map[string][]string

const (
	LabelHost        = "host"        // agent host rather than the place of the work
	LabelCorrelation = "correlation" // external conversation or request id rather than knowledge scope
	LabelEvent       = "event"       // caller supplied event grouping rather than an application condition
)

// The labels of the place without host and correlation and event metadata
// A scope filled from a run takes these so a lesson approved in one host reaches the same place in every host
// Nil stays nil
func (l Labels) Place() Labels {
	_, host := l[LabelHost]
	_, correlation := l[LabelCorrelation]
	_, event := l[LabelEvent]
	if !host && !correlation && !event {
		return l
	}
	out := maps.Clone(l)
	delete(out, LabelHost)
	delete(out, LabelCorrelation)
	delete(out, LabelEvent)
	return out
}

func (t Trace) EvidenceUnit() string {
	kind, key := "run", t.ID
	if t.SessionID != "" {
		kind, key = "session", t.SessionID
	}
	if event := t.Labels[LabelEvent]; len(event) == 1 && event[0] != "" {
		kind, key = "event", event[0]
	}
	hash := sha256.Sum256([]byte(t.Producer + "\x00" + kind + "\x00" + key))
	return hex.EncodeToString(hash[:])
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
func (l Labels) ForRun() (Labels, error) {
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
