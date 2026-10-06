package knowledge

import (
	"fmt"
	"maps"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/jeon-jihyeon/nodloop/internal/trace"
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

// The records written at t or later
func (s Set) Since(t time.Time) Set {
	out := Set{}
	for _, k := range s {
		if !k.Time.Before(t) {
			out = append(out, k)
		}
	}
	return out
}

// Whether a version of any status cites the trace as the feedback that taught it
func (s Set) Cites(traceID string) bool {
	return slices.ContainsFunc(s, func(k Knowledge) bool { return slices.Contains(k.Evidence.FeedbackTraceIDs, traceID) })
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

// The vetoes of the approved items ready to match a tool call
func (s Set) Guard() (veto.Vetoes, error) {
	specs := s.Vetoes()
	out := make(veto.Vetoes, 0, len(specs))
	for _, spec := range specs {
		v, err := spec.Veto()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// The approved items one run may carry together with the item
// A judgment with a veto acts through the guard and joins no run
func (s Set) folder(item Knowledge) Folder {
	if item.Run == nil || item.Veto != nil {
		return Folder{Carried: Set{}}
	}
	f := s.runFolder(item)
	if item.Status == StatusApproved {
		f.Compactable = len(s.runCarried(item.ID, *item.Run)) + 1
	}
	return f
}

// The run folder of the item when the set holds it past a cap and bigger than before
// 1. a folder past RunChars grows when its chars grow and one past RunItems when its items grow
// 2. a folder already past a cap that does not grow passes so a replacement never needs a retire first
func (s Set) outgrows(before Set, item Knowledge) (Folder, bool) {
	if item.Run == nil || item.Veto != nil {
		return Folder{}, false
	}
	f := s.runFolder(item)
	was := before.runCarried("", *item.Run)
	return f, (f.Chars > RunChars && f.Chars > was.runes()) || (f.Size() > RunItems && f.Size() > len(was))
}

// Runes of the texts a run receives
func (s Set) runes() int {
	n := 0
	for _, k := range s {
		n += utf8.RuneCountInString(k.Text())
	}
	return n
}

// Current items other than the item of the same kind that one run may carry with it
// Listed for a person and never merged
func (s Set) overlapsWith(k Knowledge) Set {
	out := Set{}
	for _, other := range s.Current() {
		if other.ID != k.ID && other.Kind == k.Kind && k.sharesRun(other) {
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
func (s Set) OverlapsOf(id string) (Set, error) {
	k := s.current(id)
	if k == nil {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return s.overlapsWith(*k), nil
}

// The draft as the next candidate version of its id
// 1. status and approval fields of the draft are dropped so a proposal never arrives approved
// 2. compaction fields are dropped so only a compaction proposal marks its candidates
// 3. the base is the current version of the id so approval can tell which versions the candidate was built on
func (s Set) propose(draft Knowledge, now time.Time) (Knowledge, error) {
	if draft.Basis == "" {
		draft.Basis = BasisStated
	}
	if draft.Run != nil {
		run, err := draft.Run.normalized()
		if err != nil {
			return Knowledge{}, err
		}
		draft.Run = &run
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
// 1. a candidate of a compaction is refused because only ApproveCompaction checks its coverage
// 2. a candidate built on a version that a compaction retired is refused because approving it would undo that compaction
// 3. a new version that drops or weakens the veto of the approved version is refused with ErrVetoLifted
// Only a retire by a named person lifts a veto
// 4. a new version that reaches runs the approved version never reached is refused with ErrScopeWidened
// Only a retire by a named person widens the scope the same way
// 5. an approval that pushes a run past RunChars or RunItems or grows one already past them is refused
// So a new version that replaces an item in a run already past a cap passes while that run does not grow
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
		return nil, fmt.Errorf("%w: approve compaction %s with a passing coverage check", ErrCompactionInvalid, from.Compaction)
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
		if err := to.replaces(*superseded); err != nil {
			return nil, err
		}
		records = append(records, *superseded)
	}
	if f, grew := slices.Concat(records, s).outgrows(s, to); grew {
		return nil, fmt.Errorf("%w: %s with %s", ErrFolderFull, f.load(), f)
	}
	return records, nil
}

// A new version that replaces the approved one keeps its veto and reaches no run the old one never reached
// Only a retire by a named person lifts a veto or widens the scope
func (k Knowledge) replaces(old Knowledge) error {
	if old.Veto != nil && !k.keepsVeto(*old.Veto) {
		return fmt.Errorf("%w: %s v%d does not block what v%d blocks. Restate the veto or retire v%d first",
			ErrVetoLifted, k.ID, k.Version, old.Version, old.Version)
	}
	if k.Run == nil || old.Run == nil || k.Run.widens(*old.Run) {
		return fmt.Errorf("%w: %s v%d reaches runs that v%d never reached. "+
			"Propose again with the scope of v%d or a narrower one, which is %s, propose the wider part under a new id, or retire v%d first",
			ErrScopeWidened, k.ID, k.Version, old.Version, old.Version, old.reachText(), old.Version)
	}
	return nil
}

// The approved record and the approved record it supersedes when the id has one
// 1. the approved record is checked again so a candidate stored before a check was added cannot slip through
// 2. a version older than the approved one is refused so an approval never rolls the id back
// 3. a candidate whose bases never reach the approved version is refused because it was built without that version
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
	if err := to.validate(); err != nil {
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

// The records of an import that the set does not hold yet
// 1. every record is validated so a file cannot slip in an item a proposal would refuse
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

// Whether ref takes over the outcome of a run of the producer with the labels that applied the versions in applied
// 1. the run applied a version a compaction merged into ref and never ref itself
// 2. ref still reaches the run by its producer and labels
// 3. neither ref nor a merged version cites the run as outcome evidence
// A narrowing cites the refuted runs it answers so they stop counting against the version
// A compaction restates the facts of the versions it merged so their open outcomes stay with the fact
func (s Set) Inherits(ref Ref, traceID string, applied []Ref, producer string, labels trace.Labels) bool {
	k, err := s.latest(ref.ID, ref.Version)
	if err != nil || slices.Contains(applied, ref) || k.Run == nil || !k.Run.Admits(producer, labels) {
		return false
	}
	merged := s.merged(ref)
	if !slices.ContainsFunc(applied, func(r Ref) bool { return slices.Contains(merged, r) }) {
		return false
	}
	for _, r := range append(merged, ref) {
		if cited, err := s.latest(r.ID, r.Version); err == nil && slices.Contains(cited.Evidence.OutcomeTraceIDs, traceID) {
			return false
		}
	}
	return true
}

// Versions a compaction merged into ref and in turn the versions those merged
// Only a record a compaction proposed replaces the versions it names
// A new item that cites another item as evidence replaces nothing
func (s Set) merged(ref Ref) []Ref {
	var out []Ref
	next := []Ref{ref}
	for len(next) > 0 {
		k, err := s.latest(next[0].ID, next[0].Version)
		next = next[1:]
		if err != nil || k.Compaction == "" {
			continue
		}
		for _, parent := range k.Evidence.Knowledge {
			if parent != ref && !slices.Contains(out, parent) {
				out = append(out, parent)
				next = append(next, parent)
			}
		}
	}
	return out
}
