package knowledge

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/evidence"
)

// One version of an item
type Ref struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
}

// Whether the record is of this version
func (r Ref) names(k Knowledge) bool {
	return k.ID == r.ID && k.Version == r.Version
}

// The approved items a compaction of one anchor covers
type Compactable struct {
	// The anchor first and then its folder items that an event can replay
	Items Set
	// Folder items whose evidence cites only procedure paragraphs
	// No event can replay them so a compaction leaves them out
	Excluded Set
}

// A replacement of the approved items of one folder by fewer items that never share a folder with an item of their kind
// It adds no record type and maps onto candidates and approvals and retirements that carry its id
type Compaction struct {
	ID string
	// The new versions in the order they were proposed
	Items Set
	// The old versions they replace
	Replaced Set
}

// Every event the old items came from reviewed again with the new items in their place
type Replay struct {
	Compaction string
	Events     []ReplayEvent
}

// Passes when there is an event and every event reached its expected status
// A failed or missing review has no status and fails
func (r Replay) Passed() bool {
	return len(r.Events) > 0 && !slices.ContainsFunc(r.Events, ReplayEvent.failed)
}

type ReplayEvent struct {
	EventID  string          `json:"event_id"`
	Expected evidence.Status `json:"expected"`
	// Empty when the event was never replayed or its review failed
	Got     evidence.Status `json:"got"`
	TraceID string          `json:"trace_id,omitempty"`
}

func (e ReplayEvent) failed() bool {
	return e.Got == "" || e.Got != e.Expected
}

// The approved knowledge as it reads once a compaction is approved
// Held in memory so a replay reviews with the new items and nothing is appended
type Preview struct {
	set Set
}

func (p *Preview) All(_ context.Context) (Set, error) {
	return p.set, nil
}

// Fails unless the version is approved in the preview
func (p *Preview) Approved(_ context.Context, id string, version int) (Knowledge, error) {
	return p.set.checkApproved(id, version)
}

// Whether an event can replay the item
// An item whose evidence cites only procedure paragraphs has no review to replay and is never compacted
func (e Evidence) Replayable() bool {
	return len(e.FeedbackTraceIDs) > 0 || len(e.OutcomeTraceIDs) > 0
}

// The anchor and its folder items split by whether an event can replay them
// 1. fails with ErrNotFound unless the anchor has an approved version
// 2. fails with ErrParagraphOnly when the anchor cites only procedure paragraphs
func (s Set) Compactable(anchor string) (Compactable, error) {
	k := s.current(anchor)
	if k == nil || k.Status != StatusApproved {
		return Compactable{}, fmt.Errorf("%w: %s has no approved version", ErrNotFound, anchor)
	}
	if !k.Evidence.Replayable() {
		return Compactable{}, fmt.Errorf("%w: %s", ErrParagraphOnly, anchor)
	}
	out := Compactable{Items: Set{*k}, Excluded: Set{}}
	for _, other := range s.folder(*k).Items {
		if other.Evidence.Replayable() {
			out.Items = append(out.Items, other)
		} else {
			out.Excluded = append(out.Excluded, other)
		}
	}
	return out, nil
}

// The candidates of a compaction of old built from the drafts
// Checks in order
// 1. at least two old items and one draft
// 2. every name is the current approved version of an old item
// 3. every old item is named
// 4. no two drafts of one kind share a folder
// 5. an old veto is kept by a new veto of an item that names it and blocks the old example
// 6. a draft id is one old id so it becomes the next version of that id or a new id and no id repeats
// 7. evidence is the union of the named old items and basis is verified only when every named item is
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

// No two items of the same kind share a folder
func (s Set) checkExclusive() error {
	for i, a := range s {
		for _, b := range s[i+1:] {
			if a.Kind == b.Kind && a.sharesFolder(b) {
				return fmt.Errorf("%w: %s and %s are both %s items of one folder", ErrCompactionOverlap, a.ID, b.ID, a.Kind)
			}
		}
	}
	return nil
}

// Every old veto is blocked by the veto of a new judgment that names its item
// Retiring the old item would otherwise lift a guard block unseen
func (s Set) checkVetoes(items Set) error {
	for _, old := range s {
		if old.Veto == nil {
			continue
		}
		kept := slices.ContainsFunc(items, func(k Knowledge) bool {
			return slices.Contains(k.Evidence.Knowledge, Ref{old.ID, old.Version}) && k.keepsVeto(*old.Veto)
		})
		if !kept {
			return fmt.Errorf("%w: the veto of %s v%d", ErrCompactionVeto, old.ID, old.Version)
		}
	}
	return nil
}

func (v Veto) preserves(id, reason string, old Veto) bool {
	compiled, err := v.spec(id, reason).Veto()
	if err != nil {
		return false
	}
	for tool := range strings.SplitSeq(old.Tool, "|") {
		if tool = strings.TrimSpace(tool); tool != "" && !compiled.Matches(tool, old.Example) {
			return false
		}
	}
	return true
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

// The set with the compaction approved in memory
func (s Set) preview(c Compaction, now time.Time) (Set, error) {
	_, working, err := s.approveCompaction(c, "preview", now)
	return working, err
}
