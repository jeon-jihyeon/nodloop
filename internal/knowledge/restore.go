package knowledge

import (
	"context"
	"fmt"
	"slices"
	"time"
)

// The ledger whose writes name the trace of a person's decision in decision
// 1. the answer or the intervention is recorded after the write under that id since it names the records the write made
// 2. a record that names a decision of its own keeps it so a write of nodloop never takes the person's
func (l *Ledger) Under(decision string) *Ledger {
	under := *l
	under.decision = decision
	return &under
}

// The decision of the ledger on every record that names none
func (l *Ledger) stamp(records []Knowledge) []Knowledge {
	if l.decision == "" {
		return records
	}
	for i := range records {
		if records[i].Decision == "" {
			records[i].Decision = l.decision
		}
	}
	return records
}

// Restates a retired version as the next version of its id and approves it under the person's name
// So a lesson a person or nodloop retired comes back with its text and scope
// 1. only a retired version comes back and every check of an approval holds
// 2. the next version keeps the evidence and the extract of the retired one
func (l *Ledger) Restore(ctx context.Context, id string, version int, approver string) (Knowledge, error) {
	if err := CheckApprover(approver); err != nil {
		return Knowledge{}, err
	}
	var to Knowledge
	err := l.appendDecided(ctx, func(all Set) ([]Knowledge, error) {
		k, err := all.Version(id, version)
		if err != nil {
			return nil, err
		}
		if k.Status != StatusRetired {
			return nil, fmt.Errorf("%w: %s v%d is %s and only a retired version comes back", ErrTransitionInvalid, id, version, k.Status)
		}
		records, err := all.restored(k, approver, l.now().UTC())
		if err != nil {
			return nil, err
		}
		to = records[len(records)-1]
		return records, nil
	})
	if err != nil {
		return Knowledge{}, l.refused(ctx, err)
	}
	return to, l.exported(ctx, to)
}

// The records that restate the version as the next candidate of its id and approve it in the order they are appended
// The approved record lands last so it is the newest record of the next version
func (s Set) restored(k Knowledge, approver string, now time.Time) ([]Knowledge, error) {
	next, err := s.propose(k, now)
	if err != nil {
		return nil, err
	}
	approval, err := slices.Concat(Set{next}, s).approval(next.ID, next.Version, approver, now)
	if err != nil {
		return nil, err
	}
	// The approval answers the approved record first and the record it supersedes after it
	return slices.Concat([]Knowledge{next}, approval[1:], approval[:1]), nil
}

// Returns every id the decision wrote to the state it had before that write under the person's name and answers the records
// 1. the records that name the decision are its write
// An id another write changed since is refused with ErrReverseStale since the person decides that change apart
// 2. an approved version the write made is retired and one it took away comes back as its next version
// 3. so does the candidate that waits for the id
// So the items a run receives and the candidates that wait read as before while both writes stay in the records
func (l *Ledger) Reverse(ctx context.Context, decision, approver string) (Set, error) {
	if err := CheckApprover(approver); err != nil {
		return nil, err
	}
	var wrote Set
	err := l.appendDecided(ctx, func(all Set) ([]Knowledge, error) {
		var err error
		wrote, err = all.reversal(decision, approver, l.now().UTC())
		return wrote, err
	})
	if err != nil {
		return nil, l.refused(ctx, err)
	}
	if err := l.Export(ctx); err != nil {
		return wrote, fmt.Errorf("%w: %d records of a reverse: %w", ErrExport, len(wrote), err)
	}
	return wrote, nil
}

// The records of a reverse of the decision in the order they are appended
// Every retire comes before every restore so a folder never carries the old and the new items at once
func (s Set) reversal(decision, approver string, now time.Time) (Set, error) {
	ids := s.idsUnder(decision)
	if len(ids) == 0 {
		return nil, fmt.Errorf("%w: no record names the decision %q", ErrNotFound, decision)
	}
	plans := make([]returned, 0, len(ids))
	for _, id := range ids {
		history := s.history(id)
		if history[0].Decision != decision {
			return nil, fmt.Errorf("%w: %s changed since %s", ErrReverseStale, id, decision)
		}
		plans = append(plans, history.returned(id, decision))
	}
	r := reversing{work: s, approver: approver, now: now}
	for _, p := range plans {
		r.retire(p)
	}
	for _, p := range plans {
		if err := r.restore(p); err != nil {
			return nil, err
		}
	}
	return r.out, nil
}

// Whether the records of the decision took back the write of the stamp
// 1. they are the records a reverse of the stamp under the approver writes over the records without them
// Matching references and statuses in append order
// 2. a decision that wrote no record on an id of the stamp took nothing back
// So a retry of a reverse whose event failed to append goes on after what it took back
// and another write of the same ids never passes for it
func (s Set) TakenBack(stamp, decision, approver string) bool {
	before := slices.DeleteFunc(slices.Clone(s), func(k Knowledge) bool { return k.Decision == decision })
	plan, err := before.reversal(stamp, approver, time.Time{})
	if decision == "" || err != nil {
		return false
	}
	ids := before.idsUnder(stamp)
	took := slices.DeleteFunc(slices.Clone(s), func(k Knowledge) bool { return k.Decision != decision || !slices.Contains(ids, k.ID) })
	slices.Reverse(took)
	return len(took) > 0 && slices.EqualFunc(took, plan, func(a, b Knowledge) bool {
		return a.ID == b.ID && a.Version == b.Version && a.Status == b.Status
	})
}

// The ids of the records that name the decision and none for no decision
func (s Set) idsUnder(decision string) []string {
	var ids []string
	for _, k := range s {
		if decision != "" && k.Decision == decision && !slices.Contains(ids, k.ID) {
			ids = append(ids, k.ID)
		}
	}
	return ids
}

// The approved version and the waiting candidate one id has now and had before a write
type returned struct {
	approvedNow, approvedBefore *Knowledge
	waitingNow, waitingBefore   *Knowledge
}

// The states of the id in its history with and without the records of the decision
func (s Set) returned(id, decision string) returned {
	before := slices.DeleteFunc(slices.Clone(s), func(k Knowledge) bool { return k.Decision == decision })
	return returned{
		approvedNow: s.approvedOf(id), approvedBefore: before.approvedOf(id), waitingNow: s.waitsFor(id), waitingBefore: before.waitsFor(id),
	}
}

func (s Set) approvedOf(id string) *Knowledge {
	cur := s.current(id)
	if cur == nil || cur.Status != StatusApproved {
		return nil
	}
	return cur
}

// The newest candidate of the id as Waiting lists it and nil for none or a candidate of a compaction
func (s Set) waitsFor(id string) *Knowledge {
	k, ok := s.newestCandidates()[id]
	if !ok || k.Compaction != "" {
		return nil
	}
	return &k
}

// Whether two states name different versions
func differs(a, b *Knowledge) bool {
	return (a == nil) != (b == nil) || a != nil && a.Version != b.Version
}

// One reverse in progress: the records so far over the ledger newest first and in the order they are appended
type reversing struct {
	work     Set
	out      Set
	approver string
	now      time.Time
}

// Adds the records in the order they are appended
func (r *reversing) add(records ...Knowledge) {
	newest := slices.Clone(records)
	slices.Reverse(newest)
	r.work, r.out = slices.Concat(Set(newest), r.work), append(r.out, records...)
}

// Retires the approved version and the candidate the write left in place of the ones before
// Both are the newest records of their versions and an approved version and a candidate may always be retired
// so no transition check can refuse the retire
func (r *reversing) retire(p returned) {
	for _, pair := range [][2]*Knowledge{{p.approvedNow, p.approvedBefore}, {p.waitingNow, p.waitingBefore}} {
		now, before := pair[0], pair[1]
		if now == nil || !differs(now, before) {
			continue
		}
		k := now.changed(StatusRetired, r.approver, r.now)
		k.Reason = ReasonPerson
		r.add(k)
	}
}

// Restates the approved version and the candidate the write took away as the next versions of the id
func (r *reversing) restore(p returned) error {
	if before := p.approvedBefore; before != nil && differs(p.approvedNow, before) {
		records, err := r.work.restored(*before, r.approver, r.now)
		if err != nil {
			return err
		}
		r.add(records...)
	}
	if before := p.waitingBefore; before != nil && differs(p.waitingNow, before) {
		next, err := r.work.propose(*before, r.now)
		if err != nil {
			return err
		}
		r.add(next)
	}
	return nil
}
