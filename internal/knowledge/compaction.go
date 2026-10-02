package knowledge

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// One version of an item
type Ref struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
}

// Whether the item names the version among the old items it replaces
func (k Knowledge) Names(old Knowledge) bool {
	return slices.Contains(k.Evidence.Knowledge, Ref{ID: old.ID, Version: old.Version})
}

// Whether the record is of this version
func (r Ref) names(k Knowledge) bool {
	return k.ID == r.ID && k.Version == r.Version
}

// The approved items a compaction of one anchor covers
type Compactable struct {
	// The anchor first and then the approved items one run may carry with it
	Items Set
}

// A replacement of the approved items of one folder so each run carries fewer items that never share a folder with an item of their kind
// Two judgments that each carry a veto may share a folder because vetoes are never merged
// It adds no record type and maps onto candidates and approvals and retirements that carry its id
type Compaction struct {
	ID string
	// The new versions in the order they were proposed
	Items Set
	// The old versions they replace
	Replaced Set
}

// Which new items state each old item of a compaction and what they lose
// A run cannot be replayed so a reader of both sides checks that nothing is lost
type Coverage struct {
	Compaction string         `json:"compaction"`
	Items      []CoverageItem `json:"items"`
}

type CoverageItem struct {
	Old Ref `json:"old"`
	// The new items that state the old item
	CoveredBy []Ref `json:"covered_by"`
	// Facts of the old item no new item states
	Lost []string `json:"lost,omitempty"`
}

// Passes when it is of this compaction, every old item is covered by its new items and no fact is lost
// Fails with ErrCoverageNotPassed naming the first reason
func (cv Coverage) Passes(c Compaction) error {
	if cv.Compaction != c.ID {
		return fmt.Errorf("%w: the coverage is of %q and not of %s", ErrCoverageNotPassed, cv.Compaction, c.ID)
	}
	for _, old := range c.Replaced {
		ref := Ref{ID: old.ID, Version: old.Version}
		i := slices.IndexFunc(cv.Items, func(it CoverageItem) bool { return it.Old == ref })
		switch {
		case i < 0 || len(cv.Items[i].CoveredBy) == 0:
			return fmt.Errorf("%w: no new item states %s v%d", ErrCoverageNotPassed, old.ID, old.Version)
		case len(cv.Items[i].Lost) > 0:
			return fmt.Errorf("%w: %s v%d loses %s", ErrCoverageNotPassed, old.ID, old.Version, strings.Join(cv.Items[i].Lost, "; "))
		}
		for _, by := range cv.Items[i].CoveredBy {
			if !slices.ContainsFunc(c.Items, by.names) {
				return fmt.Errorf("%w: %s v%d is not a new item of %s", ErrCoverageNotPassed, by.ID, by.Version, c.ID)
			}
		}
	}
	return nil
}

// The anchor and the approved items one run may carry with it
// Fails with ErrNotFound unless the anchor has an approved version with a run scope
func (s Set) Compactable(anchor string) (Compactable, error) {
	k := s.current(anchor)
	if k == nil || k.Status != StatusApproved || k.Run == nil {
		return Compactable{}, fmt.Errorf("%w: %s has no approved version scoped to runs", ErrNotFound, anchor)
	}
	return Compactable{Items: append(Set{*k}, s.runCarried(k.ID, *k.Run)...)}, nil
}

// Whether one run carries more than FolderItems of the items
// Every item of a run folder may reach one run with the anchor so all of them count
func (c Compactable) Crowded() bool {
	return len(c.Items) > FolderItems
}

// The candidates of a compaction of old built from the drafts
// Checks in order
// 1. at least two old items and one draft
// 2. every name is the current approved version of an old item
// 3. every old item is named
// 4. no draft reaches a run that an old item it names does not reach
// 5. no two drafts of one kind overlap in a folder unless both carry a veto
// 6. an old veto is kept by a new veto of an item that names it and keeps its tools and conditions and example
// 7. a draft id is one old id so it becomes the next version of that id or a new id and no id repeats
// 8. evidence is the union of the named old items and basis is verified only when every named item is
func (s Set) compact(id string, old Set, drafts []Knowledge, now time.Time) (Set, error) {
	if len(old) < 2 || len(drafts) == 0 {
		return nil, fmt.Errorf("%w: %d old items and %d drafts", ErrCompactionInvalid, len(old), len(drafts))
	}
	if err := old.checkNamed(drafts); err != nil {
		return nil, err
	}
	out := Set{}
	for i, draft := range drafts {
		named, err := old.named(draft.Evidence.Knowledge)
		if err != nil {
			return nil, err
		}
		if err := named.checkWidening(draft); err != nil {
			return nil, fmt.Errorf("draft %d: %w", i+1, err)
		}
		draft = named.draft(draft)
		if slices.ContainsFunc(out, draft.hasID) {
			return nil, fmt.Errorf("%w: draft %d repeats id %s", ErrCompactionInvalid, i+1, draft.ID)
		}
		if len(s.history(draft.ID)) > 0 && !slices.ContainsFunc(old, draft.hasID) {
			return nil, fmt.Errorf("%w: draft %d takes the id %s of an item outside the compaction", ErrCompactionInvalid, i+1, draft.ID)
		}
		k, err := s.propose(draft, now)
		if err != nil {
			return nil, fmt.Errorf("draft %d: %w", i+1, err)
		}
		k.Compaction, k.CompactionSize = id, len(drafts)
		out = append(out, k)
	}
	if err := out.checkExclusive(); err != nil {
		return nil, err
	}
	return out, old.checkVetoes(out)
}

func (k Knowledge) hasID(other Knowledge) bool {
	return k.ID == other.ID
}

// Every name is an old item and every old item is named
func (s Set) checkNamed(drafts []Knowledge) error {
	named := map[Ref]bool{}
	for i, d := range drafts {
		if len(d.Evidence.Knowledge) == 0 {
			return fmt.Errorf("%w: draft %d names no old item", ErrCompactionInvalid, i+1)
		}
		for _, ref := range d.Evidence.Knowledge {
			named[ref] = true
		}
	}
	for _, k := range s {
		if !named[Ref{k.ID, k.Version}] {
			return fmt.Errorf("%w: %s v%d is named by no draft", ErrCompactionInvalid, k.ID, k.Version)
		}
	}
	return nil
}

// The old items the refs name
// Fails on a ref that is not an old item such as an older version or an item of another folder
func (s Set) named(refs []Ref) (Set, error) {
	out := Set{}
	for _, ref := range refs {
		i := slices.IndexFunc(s, ref.names)
		if i < 0 {
			return nil, fmt.Errorf("%w: %s v%d is not a current approved item of the folder", ErrCompactionInvalid, ref.ID, ref.Version)
		}
		out = append(out, s[i])
	}
	return out, nil
}

// The draft with its evidence and basis built from the old items it names
// The draft never supplies trace ids so every new item traces back to the corrections behind the old ones
func (s Set) draft(d Knowledge) Knowledge {
	built := Evidence{Knowledge: d.Evidence.Knowledge}
	d.Basis = BasisVerified
	for _, k := range s {
		built = built.union(k.Evidence)
		if k.Basis != BasisVerified {
			d.Basis = BasisStated
		}
	}
	d.Evidence = built.union(Evidence{ParagraphIDs: d.Evidence.ParagraphIDs})
	return d
}

// The trace and paragraph ids of both in first seen order
// The knowledge refs stay those of e
func (e Evidence) union(other Evidence) Evidence {
	add := func(out, values []string) []string {
		for _, v := range values {
			if !slices.Contains(out, v) {
				out = append(out, v)
			}
		}
		return out
	}
	e.FeedbackTraceIDs = add(e.FeedbackTraceIDs, other.FeedbackTraceIDs)
	e.OutcomeTraceIDs = add(e.OutcomeTraceIDs, other.OutcomeTraceIDs)
	e.ParagraphIDs = add(e.ParagraphIDs, other.ParagraphIDs)
	return e
}

// Fails with ErrCompactionInvalid when the draft reaches runs that an item it names never reached
// The draft carries the facts of every item it names so a wider draft would carry a fact where it never held
func (s Set) checkWidening(d Knowledge) error {
	for _, k := range s {
		if d.Run == nil || k.Run == nil || d.Run.widens(*k.Run) {
			return fmt.Errorf("%w: it carries the facts of %s to runs that %s never reached, whose scope is %s. "+
				"Give each draft only the producer, labels and exceptions every item it names reaches, "+
				"and repeat a general fact in each draft that needs it",
				ErrCompactionInvalid, k.ID, k.ID, k.reachText())
		}
	}
	return nil
}

// No two items of the same kind reach one run unless both carry a veto
// Two vetoes never merge without lifting one so each old veto keeps its own judgment
func (s Set) checkExclusive() error {
	for i, a := range s {
		for _, b := range s[i+1:] {
			if a.Kind == b.Kind && a.sharesRun(b) && (a.Veto == nil || b.Veto == nil) {
				return fmt.Errorf("%w: %s and %s are both %s items one run may carry and no label splits them",
					ErrCompactionOverlap, a.ID, b.ID, a.Kind)
			}
		}
	}
	return nil
}

// Whether one run may carry both items
func (k Knowledge) sharesRun(other Knowledge) bool {
	return k.Run != nil && other.Run != nil && k.Run.overlaps(*other.Run)
}

// Every old veto is kept by the veto of a new judgment that names its item
// Retiring the old item would otherwise lift a guard block unseen
func (s Set) checkVetoes(items Set) error {
	for _, old := range s {
		if old.Veto == nil {
			continue
		}
		lifted := "no judgment that names it carries a veto"
		for _, k := range items {
			if k.Veto == nil || !slices.Contains(k.Evidence.Knowledge, Ref{old.ID, old.Version}) {
				continue
			}
			if lifted = k.liftedVeto(*old.Veto); lifted == "" {
				break
			}
		}
		if lifted != "" {
			return fmt.Errorf("%w: the veto of %s v%d: %s", ErrCompactionVeto, old.ID, old.Version, lifted)
		}
	}
	return nil
}

// The first change by which the veto of the item blocks less than old and empty when it blocks at least what old blocks
// 1. every old tool stays and a tool may be added
// 2. every condition keeps the field and match of one old condition since conditions join by AND
// 3. its unless is empty or the unless of that old condition
// 4. the old example stays blocked
// Regexp inclusion is undecidable so a condition is kept as written and may only be dropped
// A veto that merges two old vetoes changes a match and is refused
func (k Knowledge) liftedVeto(old Veto) string {
	tools := k.Veto.tools()
	for _, t := range old.tools() {
		if !slices.Contains(tools, t) {
			return fmt.Sprintf("tool %s is dropped", t)
		}
	}
	for _, c := range k.Veto.When {
		if slices.ContainsFunc(old.When, c.narrows) {
			continue
		}
		condition := fmt.Sprintf("%s %q", c.Field, c.Match)
		if c.Unless != "" {
			condition += fmt.Sprintf(" unless %q", c.Unless)
		}
		return fmt.Sprintf("condition %s is not an old condition as written", condition)
	}
	if !k.keepsVeto(old) {
		return "the old example is no longer blocked"
	}
	return ""
}

// Whether the condition blocks at least what old blocks
func (c VetoCondition) narrows(old VetoCondition) bool {
	return c.Field == old.Field && c.Match == old.Match && (c.Unless == "" || c.Unless == old.Unless)
}

func (v Veto) preserves(id, reason string, old Veto) bool {
	compiled, err := v.spec(id, reason).Veto()
	if err != nil {
		return false
	}
	for _, tool := range old.tools() {
		if !compiled.Matches(tool, old.Example) {
			return false
		}
	}
	return true
}

// The tool names of the list without blanks
func (v Veto) tools() []string {
	var out []string
	for t := range strings.SplitSeq(v.Tool, "|") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// The records of a compaction: the versions proposed under its id and the old versions they name
func (s Set) compaction(id string) (Compaction, error) {
	c := Compaction{ID: id, Items: Set{}, Replaced: Set{}}
	candidates := s.proposedUnder(id)
	if len(candidates) == 0 {
		return Compaction{}, fmt.Errorf("%w: compaction %s", ErrNotFound, id)
	}
	if !candidates.complete() {
		return Compaction{}, fmt.Errorf("%w: %s has %d candidates against its declared size", ErrCompactionIncomplete, id, len(candidates))
	}
	for _, candidate := range candidates {
		k, err := s.latest(candidate.ID, candidate.Version)
		if err != nil {
			return Compaction{}, err
		}
		c.Items = append(c.Items, k)
		for _, ref := range k.Evidence.Knowledge {
			if slices.ContainsFunc(c.Replaced, ref.names) {
				continue
			}
			old, err := s.latest(ref.ID, ref.Version)
			if err != nil {
				return Compaction{}, err
			}
			c.Replaced = append(c.Replaced, old)
		}
	}
	return c, nil
}

// The id of a pending compaction whose refs name one of the items
// Empty when none
// 1. a compaction is pending while all its candidates were appended and none was retired and one is still a candidate
// 2. approving one of two compactions of the same items would leave the other outdated
// 3. a proposal cut between two appends can never be approved so it is not pending and a new proposal replaces it
func (s Set) PendingCompaction(items Set) string {
	var seen []string
	for _, k := range s {
		if k.Compaction == "" || k.Status != StatusCandidate || slices.Contains(seen, k.Compaction) {
			continue
		}
		seen = append(seen, k.Compaction)
		if s.pending(k.Compaction) && s.proposedUnder(k.Compaction).replaces(items) {
			return k.Compaction
		}
	}
	return ""
}

func (s Set) pending(id string) bool {
	candidates := s.proposedUnder(id)
	if !candidates.complete() {
		return false
	}
	open := false
	for _, candidate := range candidates {
		latest, err := s.latest(candidate.ID, candidate.Version)
		if err != nil || latest.Status == StatusRetired {
			return false
		}
		open = open || latest.Status == StatusCandidate
	}
	return open
}

// Whether the evidence refs of the candidates name one of the items
func (s Set) replaces(items Set) bool {
	for _, k := range s {
		for _, ref := range k.Evidence.Knowledge {
			if slices.ContainsFunc(items, ref.names) {
				return true
			}
		}
	}
	return false
}

// Whether every candidate proposed under one compaction id was appended
// Each candidate declares how many drafts the proposal had
func (s Set) complete() bool {
	for _, k := range s {
		if k.CompactionSize != len(s) {
			return false
		}
	}
	return len(s) > 0
}

// The candidates proposed under the compaction id in the order they were appended
func (s Set) proposedUnder(id string) Set {
	out := Set{}
	for _, k := range slices.Backward(s) {
		if k.Compaction == id && k.Status == StatusCandidate {
			out = append(out, k)
		}
	}
	return out
}

// The records that approve a compaction in the order they are appended and the set with them on top
// 1. each new item is approved and an old approved version of its id is superseded
// 2. every other old item is retired
// 3. a record already appended by an earlier call is not repeated so a second call completes a partial one
// 4. an old item changed by anything but this compaction makes the compaction outdated
func (s Set) approveCompaction(c Compaction, approver string, now time.Time) ([]Knowledge, Set, error) {
	working := slices.Clone(s)
	var records []Knowledge
	add := func(k Knowledge) {
		k.Compaction = c.ID
		records = append(records, k)
		working = append(Set{k}, working...)
	}
	for _, item := range c.Items {
		latest, err := working.latest(item.ID, item.Version)
		if err != nil {
			return nil, nil, err
		}
		if latest.Status == StatusApproved {
			continue
		}
		to, superseded, err := working.approve(item.ID, item.Version, approver, now)
		if err != nil {
			return nil, nil, err
		}
		add(to)
		if superseded != nil {
			add(*superseded)
		}
	}
	for _, old := range c.Replaced {
		latest, err := working.latest(old.ID, old.Version)
		if err != nil {
			return nil, nil, err
		}
		switch {
		case latest.Status == StatusApproved && latest.Compaction != c.ID:
			add(latest.changed(StatusRetired, approver, now))
		case latest.Compaction != c.ID || latest.Status == StatusCandidate:
			return nil, nil, fmt.Errorf("%w: %s v%d is %s", ErrCompactionOutdated, old.ID, old.Version, latest.Status)
		}
	}
	return records, working, nil
}

// The records that approve the compaction of id
// 1. an approver is required once the compaction is found
// 2. an approval that pushes a run past a cap or grows one already past it is refused like one of Approve
func (s Set) compactionApproval(id, approver string, check Coverage, now time.Time) ([]Knowledge, error) {
	c, err := s.compaction(id)
	if err != nil {
		return nil, err
	}
	if err := check.Passes(c); err != nil {
		return nil, err
	}
	if approver == "" {
		return nil, fmt.Errorf("%w: compaction %s needs one", ErrApproverRequired, id)
	}
	records, after, err := s.approveCompaction(c, approver, now)
	if err != nil {
		return nil, err
	}
	for _, k := range c.Items {
		if f, grew := after.outgrows(s, k); grew {
			return nil, fmt.Errorf("%w: %s %s with %s", ErrFolderFull, k.ID, f.load(), f)
		}
	}
	return records, nil
}
