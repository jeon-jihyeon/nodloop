package knowledge

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/veto"
)

// Prefixes of the generated ids
const (
	itemPrefix       = "k-"
	compactionPrefix = "c-"
)

// Append only
// A status change is a new record with the same id and version
type Store interface {
	Append(ctx context.Context, k Knowledge) error
	// Appends only while the store holds exactly the expected records
	// The expected records come newest first as List gives them
	// Fails with ErrRecordsChanged otherwise
	AppendIfUnchanged(ctx context.Context, k Knowledge, expected []Knowledge) error
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
// 5. approve refuses an item whose folder may outgrow ReviewChars and import never checks it
// A failed hand off returns ErrVetoExport after the records are appended so the caller knows the status changed
type Ledger struct {
	store  Store
	vetoes VetoSink
	now    func() time.Time
	newID  func(prefix string) string
}

func NewLedger(store Store, vetoes VetoSink, now func() time.Time, newID func(prefix string) string) *Ledger {
	return &Ledger{store: store, vetoes: vetoes, now: now, newID: newID}
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

// Current items of the same kind whose scope overlaps the scope of the current record of id
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
	return all.folder(k), nil
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

// The version of id that is approved now
// Fails with ErrVersionUnapproved when no version of id is approved
func (l *Ledger) ApprovedVersion(ctx context.Context, id string) (int, error) {
	history, err := l.History(ctx, id)
	if err != nil {
		return 0, err
	}
	approved := history.Approved()
	if len(approved) == 0 {
		return 0, fmt.Errorf("%w: %s has no approved version", ErrVersionUnapproved, id)
	}
	return approved[0].Version, nil
}

// Appends a candidate with the next version of its id and returns it with the items it overlaps
// A draft without an id gets a generated one
func (l *Ledger) Propose(ctx context.Context, draft Knowledge) (Knowledge, Set, error) {
	all, err := l.All(ctx)
	if err != nil {
		return Knowledge{}, nil, err
	}
	if draft.ID == "" {
		draft.ID = l.newID(itemPrefix)
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
// A candidate of a compaction is refused because only ApproveCompaction checks its replay
func (l *Ledger) Approve(ctx context.Context, id string, version int, approver string) (Knowledge, error) {
	all, err := l.All(ctx)
	if err != nil {
		return Knowledge{}, err
	}
	history, err := all.historyOf(id)
	if err != nil {
		return Knowledge{}, err
	}
	from, err := history.latest(id, version)
	if err != nil {
		return Knowledge{}, err
	}
	if from.Compaction != "" {
		return Knowledge{}, fmt.Errorf("%w: approve compaction %s with a passing replay", ErrCompactionInvalid, from.Compaction)
	}
	to, superseded, err := history.approve(id, version, approver, l.now().UTC())
	if err != nil {
		return Knowledge{}, err
	}
	if f := all.folder(to); f.Full() {
		return Knowledge{}, fmt.Errorf("%w: %d of %d chars with %s", ErrFolderFull, f.Chars, ReviewChars, f)
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

// Appends one candidate per draft under a new compaction id
// 1. the old items are the anchor and its folder items that an event can replay
// 2. each draft names the old versions it replaces in its evidence knowledge refs
// 3. a draft without an id gets a generated one
func (l *Ledger) ProposeCompaction(ctx context.Context, anchor string, drafts []Knowledge) (Compaction, error) {
	all, err := l.All(ctx)
	if err != nil {
		return Compaction{}, err
	}
	old, err := all.Compactable(anchor)
	if err != nil {
		return Compaction{}, err
	}
	if pending := all.PendingCompaction(old.Items); pending != "" {
		return Compaction{}, fmt.Errorf("%w: %s", ErrCompactionPending, pending)
	}
	drafts = slices.Clone(drafts)
	for i := range drafts {
		if drafts[i].ID == "" {
			drafts[i].ID = l.newID(itemPrefix)
		}
	}
	id := l.newID(compactionPrefix)
	items, err := all.compact(id, old.Items, drafts, l.now().UTC())
	if err != nil {
		return Compaction{}, err
	}
	for _, k := range items {
		if err := l.store.Append(ctx, k); err != nil {
			return Compaction{}, err
		}
	}
	return Compaction{ID: id, Items: items, Replaced: old.Items}, nil
}

func (l *Ledger) Compaction(ctx context.Context, id string) (Compaction, error) {
	all, err := l.All(ctx)
	if err != nil {
		return Compaction{}, err
	}
	return all.compaction(id)
}

// The knowledge as it would read once the compaction is approved
// Nothing is appended
func (l *Ledger) Preview(ctx context.Context, id string) (*Preview, error) {
	all, err := l.All(ctx)
	if err != nil {
		return nil, err
	}
	c, err := all.compaction(id)
	if err != nil {
		return nil, err
	}
	set, err := all.preview(c, l.now().UTC())
	if err != nil {
		return nil, err
	}
	return &Preview{set: set}, nil
}

// Approves the new items of a compaction and retires the old ones on behalf of a named person
// 1. refused unless the replay names this compaction and passed
// 2. the approved records come before the superseded and retired ones so a failure between them never leaves a gap
// 3. a second call appends only the records still missing
// 4. vetoes are exported once after the records
func (l *Ledger) ApproveCompaction(ctx context.Context, id, approver string, replay Replay) (Compaction, error) {
	if replay.Compaction != id || !replay.Passed() {
		return Compaction{}, fmt.Errorf("%w: %s", ErrReplayNotPassed, id)
	}
	all, err := l.All(ctx)
	if err != nil {
		return Compaction{}, err
	}
	c, err := all.compaction(id)
	if err != nil {
		return Compaction{}, err
	}
	if approver == "" {
		return Compaction{}, fmt.Errorf("%w: compaction %s needs one", ErrApproverRequired, id)
	}
	records, after, err := all.approveCompaction(c, approver, l.now().UTC())
	if err != nil {
		return Compaction{}, err
	}
	for _, k := range c.Items {
		if f := after.folder(k); f.Full() {
			return Compaction{}, fmt.Errorf("%w: %s %d of %d chars with %s", ErrFolderFull, k.ID, f.Chars, ReviewChars, f)
		}
	}
	for _, k := range records {
		if err := l.store.Append(ctx, k); err != nil {
			return Compaction{}, err
		}
	}
	approved, err := l.Compaction(ctx, id)
	if err != nil {
		return Compaction{}, err
	}
	if err := l.ExportVetoes(ctx); err != nil {
		return approved, fmt.Errorf("%w: compaction %s is approved: %w", ErrVetoExport, id, err)
	}
	return approved, nil
}
