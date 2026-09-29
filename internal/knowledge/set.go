package knowledge

import (
	"fmt"
	"maps"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/jeon-jihyeon/nodloop/internal/evidence"
	"github.com/jeon-jihyeon/nodloop/internal/veto"
)

// Records as the store lists them with the newest first
// The history behind every question about an id
type Set []Knowledge

// The first listed record of every id and version
// That is its newest when the set comes from the store
// Unlike Current it keeps a candidate beside the approved version of its id
func (s Set) Versions() Set {
	type key struct {
		id      string
		version int
	}
	seen := map[key]bool{}
	out := Set{}
	for _, k := range s {
		ref := key{k.ID, k.Version}
		if !seen[ref] {
			out = append(out, k)
			seen[ref] = true
		}
	}
	return out
}

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

// The vetoes of the approved items in id order
// The source names the version and the approver so a reader of the veto file can trace a block back
func (s Set) Vetoes() []veto.Spec {
	var out []veto.Spec
	for _, k := range s.Approved() {
		if k.Veto == nil {
			continue
		}
		spec := k.Veto.spec(k.ID, k.Content)
		spec.Source = fmt.Sprintf("nodloop knowledge %s v%d approved by %s", k.ID, k.Version, k.Approver)
		out = append(out, spec)
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

// Whether some approved item may apply under the change context
// Metrics and dims are left open so the answer is an upper bound of Applicable
func (s Set) Covers(changeContext evidence.Context) bool {
	return slices.ContainsFunc(s.Approved(), func(k Knowledge) bool {
		return !slices.Contains(k.Exceptions, changeContext) &&
			(len(k.Scope.ChangeContexts) == 0 || slices.Contains(k.Scope.ChangeContexts, changeContext))
	})
}

// The approved items one review may carry together with the item
// An upper bound since a review loads only the items whose scope fits its event
// Another version of the item never counts because approval replaces it
func (s Set) folder(item Knowledge) Folder {
	f := Folder{Chars: utf8.RuneCountInString(item.Text()), Items: Set{}}
	replayable := 1
	for _, other := range s.Approved() {
		if other.ID != item.ID && item.sharesFolder(other) {
			f.Items = append(f.Items, other)
			f.Chars += utf8.RuneCountInString(other.Text())
			if other.Evidence.Replayable() {
				replayable++
			}
		}
	}
	if item.Status == StatusApproved && item.Evidence.Replayable() {
		f.Compactable = replayable
	}
	return f
}

// Current items other than id of the same kind whose scope overlaps scope
// Listed for a person and never merged
func (s Set) Overlaps(id string, kind Kind, scope Scope) Set {
	out := Set{}
	for _, other := range s.Current() {
		if other.ID != id && other.Kind == kind && scope.overlaps(other.Scope) {
			out = append(out, other)
		}
	}
	return out
}

// Empty fields mean all
type Filter struct {
	Kinds    []Kind
	Statuses []Status
	// Only the versions stale at this time
	StaleAt time.Time
}

func (f Filter) matches(k Knowledge) bool {
	if len(f.Kinds) > 0 && !slices.Contains(f.Kinds, k.Kind) {
		return false
	}
	if len(f.Statuses) > 0 && !slices.Contains(f.Statuses, k.Status) {
		return false
	}
	return f.StaleAt.IsZero() || k.Stale(f.StaleAt)
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
// Fails with ErrNotFound when the id has no record
func (s Set) historyOf(id string) (Set, error) {
	history := s.history(id)
	if len(history) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return history, nil
}

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
// 1. status and approval fields of the draft are dropped so a proposal never arrives approved
// 2. compaction fields are dropped so only a compaction proposal marks its candidates
func (s Set) propose(draft Knowledge, now time.Time) (Knowledge, error) {
	if draft.Basis == "" {
		draft.Basis = BasisStated
	}
	draft.Version, draft.Status, draft.Time = s.nextVersion(draft.ID), StatusCandidate, now
	draft.Approver, draft.ApprovedAt, draft.ReviewedAt, draft.Supersedes = "", time.Time{}, time.Time{}, 0
	draft.Compaction, draft.CompactionSize = "", 0
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
