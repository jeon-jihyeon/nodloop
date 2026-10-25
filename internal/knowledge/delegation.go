package knowledge

import (
	"context"
	"fmt"
	"time"
)

// Approves a new candidate as nodloop under a delegated decide of curate within the scope of its grant
// Refused with ErrPersonDecides for a later version, a veto, a scope past the grant or an id decided before
func (l *Ledger) ApplyDelegated(
	ctx context.Context, id string, version int, decision string, scope RunScope,
) (Knowledge, error) {
	var applied Knowledge
	err := l.appendDecided(ctx, func(all Set) ([]Knowledge, error) {
		records, err := all.delegated(id, version, decision, scope, l.now().UTC())
		if err != nil {
			return nil, err
		}
		applied = records[0]
		return records, nil
	})
	if err != nil {
		return Knowledge{}, l.refused(ctx, err)
	}
	return applied, l.exported(ctx, applied)
}

// The records of a delegated approval
func (s Set) delegated(id string, version int, decision string, scope RunScope, now time.Time) ([]Knowledge, error) {
	if decision == "" {
		return nil, ErrDecisionRequired
	}
	if err := scope.check(); err != nil {
		return nil, err
	}
	k, err := s.Version(id, version)
	if err != nil {
		return nil, err
	}
	if version != 1 || k.Base != 0 || k.Veto != nil || k.Run == nil || k.Run.Timed() || !k.Run.Within(scope) {
		return nil, fmt.Errorf("%w: a delegation covers only a new candidate within its scope", ErrPersonDecides)
	}
	for _, old := range s {
		if old.ID == id && old.Status != StatusCandidate {
			return nil, fmt.Errorf("%w: a delegation cannot replace an earlier decision", ErrPersonDecides)
		}
	}
	records, err := s.approval(id, version, ApproverNodloop, now)
	if err != nil {
		return nil, err
	}
	for i := range records {
		records[i].Decision = decision
	}
	return records, nil
}
