package knowledge

import (
	"context"
	"fmt"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/veto"
)

// Append only
// A status change is a new record with the same id and version
type Store interface {
	Append(ctx context.Context, k Knowledge) error
	// Every record newest first
	List(ctx context.Context) ([]Knowledge, error)
}

// Where the vetoes of the approved items take effect
// Handed the whole approved set every time so it never drifts from the records and an empty set clears it
type VetoSink interface {
	Write(specs []veto.Spec) error
}

// Every write appends a new record and none edits an earlier one
// 1. a proposal is always a candidate
// 2. approve and retire need an approver and follow the allowed status changes
// 3. the clock is read here in UTC and every record of one call carries that time
// 4. approve and retire and import end by handing the approved vetoes to the sink
// 5. approve refuses an item whose folder may outgrow the review budget and import never checks it
// A failed hand off returns ErrVetoExport after the records are appended so the caller knows the status changed
type Ledger struct {
	store  Store
	vetoes VetoSink
	// Characters of approved knowledge one review carries
	budget int
	now    func() time.Time
	newID  func() string
}

func NewLedger(store Store, vetoes VetoSink, budget int, now func() time.Time, newID func() string) *Ledger {
	return &Ledger{store: store, vetoes: vetoes, budget: budget, now: now, newID: newID}
}

func (l *Ledger) All(ctx context.Context) (Set, error) {
	return l.store.List(ctx)
}

func (l *Ledger) History(ctx context.Context, id string) (Set, error) {
	all, err := l.All(ctx)
	if err != nil {
		return nil, err
	}
	return all.historyOf(id)
}

// Current items of the same kind whose scope intersects with the current record of id
func (l *Ledger) Overlaps(ctx context.Context, id string) (Set, error) {
	all, err := l.All(ctx)
	if err != nil {
		return nil, err
	}
	return all.overlapsOf(id)
}

// The folder one version of id would join once approved
// A new version is measured against the approved items other than its own id
func (l *Ledger) Folder(ctx context.Context, id string, version int) (Folder, error) {
	all, err := l.All(ctx)
	if err != nil {
		return Folder{}, err
	}
	k, err := all.latest(id, version)
	if err != nil {
		return Folder{}, err
	}
	return all.folder(k, l.budget), nil
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
	all, err := l.All(ctx)
	if err != nil {
		return Knowledge{}, err
	}
	history, err := all.historyOf(id)
	if err != nil {
		return Knowledge{}, err
	}
	to, superseded, err := history.approve(id, version, approver, l.now().UTC())
	if err != nil {
		return Knowledge{}, err
	}
	if f := all.folder(to, l.budget); f.Full() {
		return Knowledge{}, fmt.Errorf("%w: %d of %d chars with %s", ErrFolderFull, f.Chars, f.Budget, f)
	}
	records := []Knowledge{to}
	if superseded != nil {
		records = append(records, *superseded)
	}
	for _, k := range records {
		if err = l.store.Append(ctx, k); err != nil {
			return Knowledge{}, err
		}
	}
	return to, l.exportVetoes(ctx, to)
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
	return to, l.exportVetoes(ctx, to)
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
	if err := l.ExportVetoes(ctx); err != nil {
		return fmt.Errorf("%w: %d records imported: %w", ErrVetoExport, len(records), err)
	}
	return nil
}

// Hands the approved vetoes to the sink again
// The way back after a failed hand off because nothing else changes a status
func (l *Ledger) ExportVetoes(ctx context.Context) error {
	all, err := l.All(ctx)
	if err != nil {
		return err
	}
	return l.vetoes.Write(all.Vetoes())
}

// The status change already happened so the error names it
func (l *Ledger) exportVetoes(ctx context.Context, changed Knowledge) error {
	if err := l.ExportVetoes(ctx); err != nil {
		return fmt.Errorf("%w: %s v%d is %s: %w", ErrVetoExport, changed.ID, changed.Version, changed.Status, err)
	}
	return nil
}
