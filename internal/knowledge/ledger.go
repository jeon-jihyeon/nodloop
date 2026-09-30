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
	// Appends in one write the records decide returns for every record newest first
	// 1. decide runs once while no other writer can append so it decides on the records as they are then
	// 2. a refusal of decide comes back as it is and nothing is written
	AppendDecided(ctx context.Context, decide func(all Set) ([]Knowledge, error)) error
	// Every record newest first
	List(ctx context.Context) ([]Knowledge, error)
}

// Where the vetoes of the approved items take effect
// Handed the whole approved set every time so it never drifts from the records and an empty set clears it
type VetoSink interface {
	Write(specs []veto.Spec) error
}

// Every write appends new records and none edits an earlier one
// 1. a proposal is always a candidate
// 2. approve and retire need an approver and follow the allowed status changes
// 3. the clock is read here in UTC and every record of one call carries that time
// 4. approve and retire and import end by handing the approved vetoes to the sink
// 5. approve refuses an item whose folder may outgrow ReviewChars or ReviewItems and import never checks it
// 6. every write decides under the store lock on the records as they are then
// So two writers at once never both pass a check that only one of them may pass and neither refuses the other
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
	if draft.ID == "" {
		draft.ID = l.newID(itemPrefix)
	}
	return l.appendCandidate(ctx, func(all Set, now time.Time) (Knowledge, error) {
		return all.propose(draft, now)
	})
}

// Appends the candidate decided on the records under the store lock with the items it overlaps then
func (l *Ledger) appendCandidate(ctx context.Context, decide func(all Set, now time.Time) (Knowledge, error)) (Knowledge, Set, error) {
	var k Knowledge
	var overlaps Set
	err := l.store.AppendDecided(ctx, func(all Set) ([]Knowledge, error) {
		var err error
		if k, err = decide(all, l.now().UTC()); err != nil {
			return nil, err
		}
		overlaps = all.Overlaps(k.ID, k.Kind, k.Scope)
		return []Knowledge{k}, nil
	})
	if err != nil {
		return Knowledge{}, nil, err
	}
	return k, overlaps, nil
}

// The approved record and the superseded one land in one write
func (l *Ledger) Approve(ctx context.Context, id string, version int, approver string) (Knowledge, error) {
	var to Knowledge
	err := l.store.AppendDecided(ctx, func(all Set) ([]Knowledge, error) {
		records, err := all.approval(id, version, approver, l.now().UTC())
		if err != nil {
			return nil, err
		}
		to = records[0]
		if f := all.folder(to); f.Full() {
			return nil, fmt.Errorf("%w: %s with %s", ErrFolderFull, f.load(), f)
		}
		return records, nil
	})
	if err != nil {
		return Knowledge{}, err
	}
	return to, l.exportVetoes(ctx, to)
}

func (l *Ledger) Retire(ctx context.Context, id string, version int, approver string) (Knowledge, error) {
	var to Knowledge
	err := l.store.AppendDecided(ctx, func(all Set) ([]Knowledge, error) {
		history, err := all.historyOf(id)
		if err != nil {
			return nil, err
		}
		if to, err = history.retire(id, version, approver, l.now().UTC()); err != nil {
			return nil, err
		}
		return []Knowledge{to}, nil
	})
	if err != nil {
		return Knowledge{}, err
	}
	return to, l.exportVetoes(ctx, to)
}

// Appends in one write the records of a file that the ledger does not hold yet and returns them
// 1. every record is checked before any lands so a bad file lands nothing
// 2. a record the ledger already holds is skipped so importing one file again changes nothing
// 3. a record older than the recorded history of its version fails with ErrImportStale
// The records land as they are without the status change checks because a data set brings its seed this way
func (l *Ledger) Import(ctx context.Context, records []Knowledge) (Set, error) {
	var fresh Set
	err := l.store.AppendDecided(ctx, func(all Set) ([]Knowledge, error) {
		var err error
		fresh, err = all.importable(records)
		return fresh, err
	})
	if err != nil {
		return nil, err
	}
	if err := l.ExportVetoes(ctx); err != nil {
		return fresh, fmt.Errorf("%w: %d records imported: %w", ErrVetoExport, len(fresh), err)
	}
	return fresh, nil
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
	drafts = slices.Clone(drafts)
	for i := range drafts {
		if drafts[i].ID == "" {
			drafts[i].ID = l.newID(itemPrefix)
		}
	}
	c := Compaction{ID: l.newID(compactionPrefix)}
	err := l.store.AppendDecided(ctx, func(all Set) ([]Knowledge, error) {
		old, err := all.Compactable(anchor)
		if err != nil {
			return nil, err
		}
		if pending := all.PendingCompaction(old.Items); pending != "" {
			return nil, fmt.Errorf("%w: %s", ErrCompactionPending, pending)
		}
		if c.Items, err = all.compact(c.ID, old.Items, drafts, l.now().UTC()); err != nil {
			return nil, err
		}
		c.Replaced = old.Items
		return c.Items, nil
	})
	if err != nil {
		return Compaction{}, err
	}
	return c, nil
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
// 2. the approved and superseded and retired records land in one write
// 3. a second call appends only the records still missing such as those of a legacy call cut between two appends
// 4. vetoes are exported once after the records
func (l *Ledger) ApproveCompaction(ctx context.Context, id, approver string, replay Replay) (Compaction, error) {
	if replay.Compaction != id || !replay.Passed() {
		return Compaction{}, fmt.Errorf("%w: %s", ErrReplayNotPassed, id)
	}
	err := l.store.AppendDecided(ctx, func(all Set) ([]Knowledge, error) {
		c, err := all.compaction(id)
		if err != nil {
			return nil, err
		}
		if approver == "" {
			return nil, fmt.Errorf("%w: compaction %s needs one", ErrApproverRequired, id)
		}
		records, after, err := all.approveCompaction(c, approver, l.now().UTC())
		if err != nil {
			return nil, err
		}
		for _, k := range c.Items {
			if f := after.folder(k); f.Full() {
				return nil, fmt.Errorf("%w: %s %s with %s", ErrFolderFull, k.ID, f.load(), f)
			}
		}
		return records, nil
	})
	if err != nil {
		return Compaction{}, err
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
