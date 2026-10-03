package knowledge

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// Where an item applies among the runs of one producer
// 1. every key of Labels must hold a value the run carries
// 2. no key of Except may hold one
type RunScope struct {
	Producer string       `json:"producer"`
	Labels   trace.Labels `json:"labels,omitempty"`
	Except   trace.Labels `json:"except,omitempty"`
}

// Fails with ErrScopeInvalid on a missing producer or an empty label
// Values are compared as given so a draft is normalized before it is stored
func (r RunScope) check() error {
	if r.Producer == "" {
		return fmt.Errorf("%w: a run scope names its producer", ErrScopeInvalid)
	}
	for _, labels := range []trace.Labels{r.Labels, r.Except} {
		if _, err := labels.Normalized(); err != nil {
			return fmt.Errorf("%w: %w", ErrScopeInvalid, err)
		}
	}
	return nil
}

// The same scope with each value list sorted and deduplicated
func (r RunScope) normalized() (RunScope, error) {
	labels, err := r.Labels.Normalized()
	if err != nil {
		return RunScope{}, fmt.Errorf("%w: %w", ErrScopeInvalid, err)
	}
	except, err := r.Except.Normalized()
	if err != nil {
		return RunScope{}, fmt.Errorf("%w: %w", ErrScopeInvalid, err)
	}
	return RunScope{Producer: r.Producer, Labels: labels, Except: except}, nil
}

// Whether a run of the producer with the labels receives an item of this scope
func (r RunScope) Admits(producer string, labels trace.Labels) bool {
	if r.Producer != producer {
		return false
	}
	for key, values := range r.Labels {
		if !carriesAny(labels, key, values) {
			return false
		}
	}
	for key, values := range r.Except {
		if carriesAny(labels, key, values) {
			return false
		}
	}
	return true
}

// Whether one run could carry both scopes
// Each key both name must share a value
// Exceptions are left open so a pair is listed for a person rather than missed
func (r RunScope) overlaps(other RunScope) bool {
	if r.Producer != other.Producer {
		return false
	}
	for key, values := range r.Labels {
		theirs, ok := other.Labels[key]
		if ok && !slices.ContainsFunc(values, func(v string) bool { return slices.Contains(theirs, v) }) {
			return false
		}
	}
	return true
}

// Whether r reaches a run that old never reached
// 1. another producer or a key old requires that r drops widens it
// 2. a value r allows under a key that old does not allow widens it
// 3. an exception old makes that r lifts widens it unless the labels r requires leave that value out
func (r RunScope) widens(old RunScope) bool {
	if r.Producer != old.Producer {
		return true
	}
	for key, values := range old.Labels {
		mine, ok := r.Labels[key]
		if !ok || slices.ContainsFunc(mine, func(v string) bool { return !slices.Contains(values, v) }) {
			return true
		}
	}
	for key, values := range old.Except {
		allowed, required := r.Labels[key]
		lifted := func(v string) bool { return !r.Except.Has(key, v) && (!required || slices.Contains(allowed, v)) }
		if slices.ContainsFunc(values, lifted) {
			return true
		}
	}
	return false
}

// Fails with ErrScopeUnobserved naming each key and value that no recorded run of the producer carries
// An item scoped to one of them could never apply
func (r RunScope) Recorded(vocab trace.Labels) error {
	var missing []string
	for _, labels := range []trace.Labels{r.Labels, r.Except} {
		for _, key := range slices.Sorted(maps.Keys(labels)) {
			for _, v := range labels[key] {
				if !vocab.Has(key, v) {
					missing = append(missing, key+"="+v)
				}
			}
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("%w: no run of %s carries %s", ErrScopeUnobserved, r.Producer, strings.Join(missing, ", "))
}

func (r RunScope) String() string {
	parts := []string{"runs of " + r.Producer}
	for _, key := range slices.Sorted(maps.Keys(r.Labels)) {
		parts = append(parts, key+"="+strings.Join(r.Labels[key], "|"))
	}
	for _, key := range slices.Sorted(maps.Keys(r.Except)) {
		parts = append(parts, "except "+key+"="+strings.Join(r.Except[key], "|"))
	}
	return strings.Join(parts, ". ")
}

func carriesAny(labels trace.Labels, key string, values []string) bool {
	return slices.ContainsFunc(values, func(v string) bool { return labels.Has(key, v) })
}

// The approved items that apply to a run of the producer with the labels
// A judgment with a veto acts through the guard and never reaches a run
func (s Set) For(producer string, labels trace.Labels) Set {
	out := Set{}
	for _, k := range s.Current() {
		if k.Status == StatusApproved && k.Veto == nil && k.Run != nil && k.Run.Admits(producer, labels) {
			out = append(out, k)
		}
	}
	return out
}

// The candidates a person has yet to approve that would reach a run of the producer with the labels
// 1. a candidate beside the approved version of its id counts because it waits all the same
// 2. a candidate of a compaction is left out because only its coverage check leads to approval
func (s Set) Waiting(producer string, labels trace.Labels) Set {
	out := Set{}
	for _, k := range s.Versions() {
		if k.Status == StatusCandidate && k.Compaction == "" && k.Run != nil && k.Run.Admits(producer, labels) {
			out = append(out, k)
		}
	}
	return out
}

// The approved run items a run may carry together with the item
// Every approved item of the producer whose scope overlaps the item may reach the same run
func (s Set) runFolder(item Knowledge) Folder {
	carried := s.runCarried(item.ID, *item.Run)
	return Folder{Chars: utf8.RuneCountInString(item.Text()) + carried.runes(), Carried: carried, Producer: item.Run.Producer}
}

// The approved run items other than id whose scope overlaps run
func (s Set) runCarried(id string, run RunScope) Set {
	out := Set{}
	for _, other := range s.Approved() {
		if other.ID != id && other.Veto == nil && other.Run != nil && other.Run.overlaps(run) {
			out = append(out, other)
		}
	}
	return out
}
