package knowledge

import (
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/evidence"
)

// Records as the store lists them with the newest first
// The history behind every question about an id
type Set []Knowledge

// Active record per id
// 1. the latest record per id and version decides that version's status
// 2. an approved version wins
// 3. otherwise the newest candidate
// Retired and superseded versions are never current
func (s Set) Current() Set {
	type key struct {
		id      string
		version int
	}
	latest := map[key]Knowledge{}
	for _, k := range s {
		if _, ok := latest[key{k.ID, k.Version}]; !ok {
			latest[key{k.ID, k.Version}] = k
		}
	}
	byID := map[string]Knowledge{}
	for _, k := range latest {
		if cur, ok := byID[k.ID]; !ok || k.outranks(cur.Status, cur.Version) {
			byID[k.ID] = k
		}
	}
	out := make(Set, 0, len(byID))
	for _, id := range slices.Sorted(maps.Keys(byID)) {
		if k := byID[id]; k.Status.active() {
			out = append(out, k)
		}
	}
	return out
}

// Current approved items
func (s Set) Approved() Set {
	out := Set{}
	for _, k := range s.Current() {
		if k.Status == StatusApproved {
			out = append(out, k)
		}
	}
	return out
}

// Approved current items whose scope matches the event and whose exceptions do not
func (s Set) Applicable(changeContext evidence.Context, moved Moved, dims Dims) Set {
	out := Set{}
	for _, k := range s.Current() {
		if k.applies(changeContext, moved, dims) {
			out = append(out, k)
		}
	}
	return out
}

// Current items other than id of the same kind whose scope intersects with scope
// Listed for a person and never merged
func (s Set) Overlaps(id string, kind Kind, scope Scope) Set {
	out := Set{}
	for _, other := range s.Current() {
		if other.ID != id && other.Kind == kind && scope.intersects(other.Scope) {
			out = append(out, other)
		}
	}
	return out
}

// Empty fields mean all
type Filter struct {
	Kinds    []Kind
	Statuses []Status
}

func (f Filter) matches(k Knowledge) bool {
	if len(f.Kinds) > 0 && !slices.Contains(f.Kinds, k.Kind) {
		return false
	}
	if len(f.Statuses) > 0 && !slices.Contains(f.Statuses, k.Status) {
		return false
	}
	return true
}

// Items the filter keeps in their order
func (s Set) Matching(f Filter) Set {
	out := Set{}
	for _, k := range s {
		if f.matches(k) {
			out = append(out, k)
		}
	}
	return out
}

// Records of id in their order
func (s Set) history(id string) Set {
	out := Set{}
	for _, k := range s {
		if k.ID == id {
			out = append(out, k)
		}
	}
	return out
}

// Nil when no version of id is approved or a candidate
func (s Set) current(id string) *Knowledge {
	cur := s.history(id).Current()
	if len(cur) == 0 {
		return nil
	}
	return &cur[0]
}

// Overlaps of the current record of id
func (s Set) overlapsOf(id string) (Set, error) {
	k := s.current(id)
	if k == nil {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return s.Overlaps(k.ID, k.Kind, k.Scope), nil
}

// The draft as the next candidate version of its id
// Status and approval fields of the draft are dropped so a proposal never arrives approved
func (s Set) propose(draft Knowledge, now time.Time) (Knowledge, error) {
	if draft.Basis == "" {
		draft.Basis = BasisStated
	}
	draft.Version, draft.Status, draft.Time = s.nextVersion(draft.ID), StatusCandidate, now
	draft.Approver, draft.ApprovedAt, draft.Supersedes = "", time.Time{}, 0
	if err := draft.validate(); err != nil {
		return Knowledge{}, err
	}
	return draft, nil
}

// The approved record and the approved record it supersedes when the id has one
// A version older than the approved one is refused so an approval never rolls the id back
func (s Set) approve(id string, version int, approver string, now time.Time) (Knowledge, *Knowledge, error) {
	from, err := s.latest(id, version)
	if err != nil {
		return Knowledge{}, nil, err
	}
	to, err := from.transition(StatusApproved, approver, now)
	if err != nil {
		return Knowledge{}, nil, err
	}
	cur := s.current(id)
	if cur == nil || cur.Status != StatusApproved {
		return to, nil, nil
	}
	if cur.Version > version {
		return Knowledge{}, nil, fmt.Errorf("%w: %s version %d is older than approved version %d",
			ErrTransitionInvalid, id, version, cur.Version)
	}
	to.Supersedes = cur.Version
	superseded := cur.changed(StatusSuperseded, approver, now)
	return to, &superseded, nil
}

func (s Set) retire(id string, version int, approver string, now time.Time) (Knowledge, error) {
	from, err := s.latest(id, version)
	if err != nil {
		return Knowledge{}, err
	}
	return from.transition(StatusRetired, approver, now)
}

func (s Set) checkApproved(id string, version int) (Knowledge, error) {
	k, err := s.latest(id, version)
	if err != nil {
		return Knowledge{}, err
	}
	if k.Status != StatusApproved {
		return Knowledge{}, fmt.Errorf("%w: %s version %d is %s", ErrVersionUnapproved, id, version, k.Status)
	}
	return k, nil
}

// 1 for a new id
func (s Set) nextVersion(id string) int {
	n := 0
	for _, k := range s {
		if k.ID == id {
			n = max(n, k.Version)
		}
	}
	return n + 1
}

func (s Set) latest(id string, version int) (Knowledge, error) {
	for _, k := range s {
		if k.ID == id && k.Version == version {
			return k, nil
		}
	}
	return Knowledge{}, fmt.Errorf("%w: %s version %d", ErrNotFound, id, version)
}
