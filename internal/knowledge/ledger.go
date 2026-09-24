package knowledge

import (
	"context"
	"fmt"
	"time"
)

// Append only
// A status change is a new record with the same id and version
type Store interface {
	Append(ctx context.Context, k Knowledge) error
	// Every record newest first
	List(ctx context.Context) ([]Knowledge, error)
}

// Every write appends a new record and none edits an earlier one
// 1. a proposal is always a candidate
// 2. approve and retire need an approver and follow the allowed status changes
// 3. the clock is read here in UTC and every record of one call carries that time
type Ledger struct {
	store Store
	now   func() time.Time
	newID func() string
}

func NewLedger(store Store, now func() time.Time, newID func() string) *Ledger {
	return &Ledger{store: store, now: now, newID: newID}
}

func (l *Ledger) All(ctx context.Context) (Set, error) {
	return l.store.List(ctx)
}

// Fails with ErrNotFound when the id has no record
func (l *Ledger) History(ctx context.Context, id string) (Set, error) {
	all, err := l.All(ctx)
	if err != nil {
		return nil, err
	}
	history := all.history(id)
	if len(history) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return history, nil
}

// Current items of the same kind whose scope intersects with the current record of id
func (l *Ledger) Overlaps(ctx context.Context, id string) (Set, error) {
	all, err := l.All(ctx)
	if err != nil {
		return nil, err
	}
	return all.overlapsOf(id)
}

// Latest record of one version
// Fails unless that version is approved
func (l *Ledger) Approved(ctx context.Context, id string, version int) (Knowledge, error) {
	history, err := l.History(ctx, id)
	if err != nil {
		return Knowledge{}, err
	}
	return history.checkApproved(id, version)
}

// Appends a candidate with the next version of its id and returns it with the items it overlaps
// A draft without an id gets a generated one
func (l *Ledger) Propose(ctx context.Context, draft Knowledge) (Knowledge, Set, error) {
	all, err := l.All(ctx)
	if err != nil {
		return Knowledge{}, nil, err
	}
	if draft.ID == "" {
		draft.ID = l.newID()
	}
	k, err := all.propose(draft, l.now().UTC())
	if err != nil {
		return Knowledge{}, nil, err
	}
	if err = l.store.Append(ctx, k); err != nil {
		return Knowledge{}, nil, err
	}
	return k, all.Overlaps(k.ID, k.Kind, k.Scope), nil
}

// The approved record is appended before the superseded one
// A failure between the two leaves two approved versions and Current still picks the newer
func (l *Ledger) Approve(ctx context.Context, id string, version int, approver string) (Knowledge, error) {
	history, err := l.History(ctx, id)
	if err != nil {
		return Knowledge{}, err
	}
	to, superseded, err := history.approve(id, version, approver, l.now().UTC())
	if err != nil {
		return Knowledge{}, err
	}
	if err = l.store.Append(ctx, to); err != nil {
		return Knowledge{}, err
	}
	if superseded == nil {
		return to, nil
	}
	if err = l.store.Append(ctx, *superseded); err != nil {
		return Knowledge{}, err
	}
	return to, nil
}

func (l *Ledger) Retire(ctx context.Context, id string, version int, approver string) (Knowledge, error) {
	history, err := l.History(ctx, id)
	if err != nil {
		return Knowledge{}, err
	}
	to, err := history.retire(id, version, approver, l.now().UTC())
	if err != nil {
		return Knowledge{}, err
	}
	if err = l.store.Append(ctx, to); err != nil {
		return Knowledge{}, err
	}
	return to, nil
}

// Appends each record as it is once it is valid
func (l *Ledger) Import(ctx context.Context, records []Knowledge) error {
	for i, k := range records {
		if err := k.validate(); err != nil {
			return fmt.Errorf("record %d: %w", i+1, err)
		}
		if err := l.store.Append(ctx, k); err != nil {
			return err
		}
	}
	return nil
}
