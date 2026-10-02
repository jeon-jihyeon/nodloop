package knowledge

import (
	"context"
	"errors"
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
	// Rewrites the rules file with what render returns for every record newest first
	// render runs while no other writer can append so the later of two writers reads the newer records
	ReplaceRules(ctx context.Context, render func(all Set) string) error
}

// Where the vetoes of the approved items take effect
// Handed the whole approved set every time so it never drifts from the records and an empty set clears it
// The sink calls read while no other process replaces the same vetoes so the last replace reads the newest records
type VetoSink interface {
	Replace(read func() ([]veto.Spec, error)) error
}

// Every write appends new records and none edits an earlier one
// 1. a proposal is always a candidate
// 2. approve and retire need an approver and follow the allowed status changes
// 3. the clock is read here in UTC and every record of one call carries that time
// 4. approve and retire and import and reaffirm end by exporting the approved vetoes and rules
// A refused approve or retire exports them too so its retry repairs an export a killed call left behind
// 5. approve refuses an approval that pushes a run past RunChars or RunItems or grows one already past them
// and import never checks it
// 6. every write decides under the store lock on the records as they are then
// So two writers at once never both pass a check that only one of them may pass and neither refuses the other
// A failed export returns ErrExport after the records are appended so the caller knows the status changed
type Ledger struct {
	store  Store
	vetoes VetoSink
	now    func() time.Time
	newID  func(prefix string) string
}

func NewLedger(
	store Store, vetoes VetoSink, now func() time.Time, newID func(prefix string) string,
) *Ledger {
	return &Ledger{store: store, vetoes: vetoes, now: now, newID: newID}
}

// The anchor and the approved items one run may carry with it
func (l *Ledger) Compactable(ctx context.Context, anchor string) (Compactable, error) {
	all, err := l.All(ctx)
	if err != nil {
		return Compactable{}, err
	}
	return all.Compactable(anchor)
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
		overlaps = all.overlapsWith(k)
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
		return records, nil
	})
	if err != nil {
		return Knowledge{}, l.refused(ctx, err)
	}
	return to, l.exported(ctx, to)
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
		return Knowledge{}, l.refused(ctx, err)
	}
	return to, l.exported(ctx, to)
}

// The refusal of an approve or a retire after the approved knowledge is exported again
// A call killed between its append and its export leaves the exports behind the records and its retry is refused
// so the refused retry brings them back in step
// A failed export joins the refusal without ErrExport because no status changed
func (l *Ledger) refused(ctx context.Context, err error) error {
	return errors.Join(err, l.Export(ctx))
}

// Appends in one write the records of a file that the ledger does not hold yet and returns them
// 1. every record is checked before any lands so a bad file lands nothing
// 2. a record the ledger already holds is skipped so importing one file again changes nothing
// 3. a record older than the recorded history of its version fails with ErrImportStale
// The records land as they are without the status change checks because they were decided where they were written
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
	if err := l.Export(ctx); err != nil {
		return fresh, fmt.Errorf("%w: %d records imported: %w", ErrExport, len(fresh), err)
	}
	return fresh, nil
}

// Hands the approved vetoes to the sink and rewrites the rules file again
// The way back after a failed export because nothing else changes a status
// One failure never stops the other export
func (l *Ledger) Export(ctx context.Context) error {
	vetoes := l.vetoes.Replace(func() ([]veto.Spec, error) {
		all, err := l.All(ctx)
		if err != nil {
			return nil, err
		}
		return all.Vetoes(), nil
	})
	return errors.Join(vetoes, l.store.ReplaceRules(ctx, Set.Rules))
}

// The status change already happened so the error names it
func (l *Ledger) exported(ctx context.Context, changed Knowledge) error {
	if err := l.Export(ctx); err != nil {
		return fmt.Errorf("%w: %s v%d is %s: %w", ErrExport, changed.ID, changed.Version, changed.Status, err)
	}
	return nil
}

// Appends one candidate per draft under a new compaction id
// 1. the old items are the anchor and its folder items
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

// Approves the new items of a compaction and retires the old ones on behalf of a named person
// 1. refused unless the coverage check names this compaction and passed
// 2. the approved and superseded and retired records land in one write
// 3. a second call appends only the records still missing such as those of a legacy call cut between two appends
// 4. vetoes are exported once after the records
func (l *Ledger) ApproveCompaction(ctx context.Context, id, approver string, check Coverage) (Compaction, error) {
	err := l.store.AppendDecided(ctx, func(all Set) ([]Knowledge, error) {
		return all.compactionApproval(id, approver, check, l.now().UTC())
	})
	if err != nil {
		return Compaction{}, err
	}
	approved, err := l.Compaction(ctx, id)
	if err != nil {
		return Compaction{}, err
	}
	if err := l.Export(ctx); err != nil {
		return approved, fmt.Errorf("%w: compaction %s is approved: %w", ErrExport, id, err)
	}
	return approved, nil
}
