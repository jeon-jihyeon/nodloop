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
	byID := map[string]Knowledge{}
	for _, k := range s.Versions() {
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
	for _, k := range s.Current() {
		if k.mayApply(changeContext) {
			return true
		}
	}
	return false
}

// The approved items one review may carry together with the item
// 1. measured per change context the item reaches because an event carries one and a review loads only its items
// 2. an upper bound per event since metrics and dims are left open
// Another version of the item never counts because approval replaces it
func (s Set) folder(item Knowledge) Folder {
	f := Folder{Chars: utf8.RuneCountInString(item.Text()), Carried: Set{}}
	for _, c := range evidence.Contexts() {
		if !item.reaches(c) {
			continue
		}
		if next := s.folderIn(item, c); f.Context == "" || next.heavier(f) {
			f = next
		}
	}
	if item.Status == StatusApproved && item.Evidence.Replayable() {
		f.Compactable = s.compactable(item).heaviest()
	}
	return f
}

// The folder of the item in one change context it reaches
func (s Set) folderIn(item Knowledge, changeContext evidence.Context) Folder {
	carried := s.carried(item.ID, changeContext)
	return Folder{Chars: utf8.RuneCountInString(item.Text()) + carried.runes(), Carried: carried, Context: changeContext}
}

// The approved items other than id that a review of the change context may load
func (s Set) carried(id string, changeContext evidence.Context) Set {
	out := Set{}
	for _, other := range s.Approved() {
		if other.ID != id && other.mayApply(changeContext) {
			out = append(out, other)
		}
	}
	return out
}

// Runes of the texts a review sees
func (s Set) runes() int {
	n := 0
	for _, k := range s {
		n += utf8.RuneCountInString(k.Text())
	}
	return n
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
// 3. the base is the current version of the id so approval can tell which versions the candidate was built on
func (s Set) propose(draft Knowledge, now time.Time) (Knowledge, error) {
	if draft.Basis == "" {
		draft.Basis = BasisStated
	}
	draft.Version, draft.Status, draft.Time = s.nextVersion(draft.ID), StatusCandidate, now
	draft.Approver, draft.ApprovedAt, draft.ReviewedAt, draft.Supersedes = "", time.Time{}, time.Time{}, 0
	draft.Compaction, draft.CompactionSize, draft.Base = "", 0, 0
	if cur := s.current(draft.ID); cur != nil {
		draft.Base = cur.Version
	}
	if err := draft.validate(); err != nil {
		return Knowledge{}, err
	}
	return draft, nil
}

// The records of an approval with the approved record first and the record it supersedes after it
// 1. a candidate of a compaction is refused because only ApproveCompaction checks its replay
// 2. a candidate built on a version that a compaction retired is refused because approving it would undo that compaction
// 3. a new version that drops or weakens the veto of the approved version is refused with ErrVetoLifted
// Only a retire by a named person lifts a veto
func (s Set) approval(id string, version int, approver string, now time.Time) ([]Knowledge, error) {
	history, err := s.historyOf(id)
	if err != nil {
		return nil, err
	}
	from, err := history.latest(id, version)
	if err != nil {
		return nil, err
	}
	if from.Status == StatusCandidate && from.Compaction != "" {
		return nil, fmt.Errorf("%w: approve compaction %s with a passing replay", ErrCompactionInvalid, from.Compaction)
	}
	if retired, ok := s.compactedBase(from); ok {
		return nil, fmt.Errorf("%w: %s v%d was built on v%d, which compaction %s retired. Retire v%d and propose the change on the item that replaced v%d",
			ErrCandidateOutdated, id, version, retired.Version, retired.Compaction, version, retired.Version)
	}
	to, superseded, err := history.approve(id, version, approver, now)
	if err != nil {
		return nil, err
	}
	records := []Knowledge{to}
	if superseded != nil {
		if superseded.Veto != nil && !to.keepsVeto(*superseded.Veto) {
			return nil, fmt.Errorf("%w: %s v%d does not block what v%d blocks. Restate the veto or retire v%d first",
				ErrVetoLifted, id, version, superseded.Version, superseded.Version)
		}
		records = append(records, *superseded)
	}
	return records, nil
}

// The approved record and the approved record it supersedes when the id has one
// 1. a version older than the approved one is refused so an approval never rolls the id back
// 2. a candidate whose bases never reach the approved version is refused because it was built without that version
// Approving it would drop whatever the approved version added such as the facts a compaction merged
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
	if !s.builtOn(from, cur.Version) {
		return Knowledge{}, nil, fmt.Errorf(
			"%w: %s v%d was built from v%d and never from the approved v%d. Propose again from v%d or retire v%d",
			ErrCandidateOutdated, id, version, from.Base, cur.Version, cur.Version, version)
	}
	to.Supersedes = cur.Version
	superseded := cur.changed(StatusSuperseded, approver, now)
	return to, &superseded, nil
}

// Whether version is the base of k or a base of its base
// 1. a candidate built on another candidate knows every version that candidate was built on
// 2. a record without a base cannot tell and counts as built on any version
// 3. a base missing from the set ends the walk
func (s Set) builtOn(k Knowledge, version int) bool {
	if k.Base == 0 {
		return true
	}
	for base := k.Base; base > 0; {
		if base == version {
			return true
		}
		b, err := s.latest(k.ID, base)
		if err != nil {
			return false
		}
		base = b.Base
	}
	return false
}

// The latest record of the first version under k that a compaction retired
// 1. the walk follows the bases of k and a base missing from the set ends it
// 2. a version counts when its latest record is retired under a compaction whose candidates name that version
// A person who retires a compacted version or abandons a compaction candidate keeps its compaction id
// so only the names of the candidates tell the two apart
func (s Set) compactedBase(k Knowledge) (Knowledge, bool) {
	for base := k.Base; base > 0; {
		b, err := s.latest(k.ID, base)
		if err != nil {
			return Knowledge{}, false
		}
		if b.Status == StatusRetired && b.Compaction != "" && s.proposedUnder(b.Compaction).replaces(Set{b}) {
			return b, true
		}
		base = b.Base
	}
	return Knowledge{}, false
}

// The records of an import that the set does not hold yet in their order
// 1. every record must be valid and the first one that is not fails with its position
// 2. a record the set already holds is left out so importing one file twice changes nothing
// 3. a record older than the newest record of its id and version fails with ErrImportStale
// So a file never undoes a later retire or approval
func (s Set) importable(records []Knowledge) (Set, error) {
	out := Set{}
	for i, k := range records {
		if err := k.validate(); err != nil {
			return nil, fmt.Errorf("record %d: %w", i+1, err)
		}
		if slices.ContainsFunc(s, k.same) {
			continue
		}
		if newest, err := s.latest(k.ID, k.Version); err == nil && k.Time.Before(newest.Time) {
			return nil, fmt.Errorf("record %d: %w: %s v%d %s at %s and the recorded %s at %s",
				i+1, ErrImportStale, k.ID, k.Version, k.Status, k.Time.Format(time.RFC3339),
				newest.Status, newest.Time.Format(time.RFC3339))
		}
		out = append(out, k)
	}
	return out, nil
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

// The first listed record of id
func (s Set) Find(id string) (Knowledge, bool) {
	for _, k := range s {
		if k.ID == id {
			return k, true
		}
	}
	return Knowledge{}, false
}

func (s Set) latest(id string, version int) (Knowledge, error) {
	for _, k := range s {
		if k.ID == id && k.Version == version {
			return k, nil
		}
	}
	return Knowledge{}, fmt.Errorf("%w: %s version %d", ErrNotFound, id, version)
}

// The id of the version first and then every id its compactions replaced in first seen order
// 1. a compaction names only its direct parents so the walk follows each of them in turn
// 2. the earlier versions of an id are walked too
// A revision of a compacted item carries only its own evidence and still stands for what the compaction replaced
// 3. a version met twice is walked once so a draft that reuses an old id ends
// 4. a version missing from the set stands for its own id alone
func (s Set) Lineage(ref Ref) []string {
	ids := []string{}
	walked := map[Ref]bool{}
	next := []Ref{ref}
	for len(next) > 0 {
		r := next[0]
		next = next[1:]
		if walked[r] {
			continue
		}
		walked[r] = true
		if !slices.Contains(ids, r.ID) {
			ids = append(ids, r.ID)
		}
		if k, err := s.latest(r.ID, r.Version); err == nil {
			next = append(next, k.Evidence.Knowledge...)
		}
		next = append(next, s.history(r.ID).before(r.Version)...)
	}
	return ids
}

// The versions below version as refs
func (s Set) before(version int) []Ref {
	out := []Ref{}
	for _, k := range s {
		if k.Version < version {
			out = append(out, Ref{ID: k.ID, Version: k.Version})
		}
	}
	return out
}
