package knowledge

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/evidence"
)

// Days an approved version stays fresh after its approval or its last reaffirm
// The three month freshness reminder that documentation owners get at Google
const ReviewDays = 90

// When a person last stood behind this version
// The reaffirm time else the approval time else the time of the record
func (k Knowledge) LastReviewed() time.Time {
	if !k.ReviewedAt.IsZero() {
		return k.ReviewedAt
	}
	if !k.ApprovedAt.IsZero() {
		return k.ApprovedAt
	}
	return k.Time
}

// Whether an approved version went ReviewDays or more without a person standing behind it
// Age is a reason to look and never a reason to retire
func (k Knowledge) Stale(now time.Time) bool {
	return k.Status == StatusApproved && !now.Before(k.LastReviewed().AddDate(0, 0, ReviewDays))
}

// Records that a named person rechecked the current approved version
// 1. the approved record is appended again with the reaffirm time and the approver and nothing a review sees changes
// 2. the version is checked under the store lock
// So a reaffirm never reopens a version that a concurrent retire closed
func (l *Ledger) Reaffirm(ctx context.Context, id string, version int, approver string) (Knowledge, error) {
	if approver == "" {
		return Knowledge{}, fmt.Errorf("%w: reaffirm needs one", ErrApproverRequired)
	}
	var k Knowledge
	err := l.store.AppendDecided(ctx, func(all Set) ([]Knowledge, error) {
		var err error
		if k, err = all.currentApproved(id, version); err != nil {
			return nil, err
		}
		k.Time = l.now().UTC()
		k.ReviewedAt, k.Approver = k.Time, approver
		return []Knowledge{k}, nil
	})
	if err != nil {
		return Knowledge{}, err
	}
	return k, nil
}

// Proposes the next version of the current approved version without the change contexts where its reviews were refuted
// 1. a scoped item drops the contexts from its change contexts and an unscoped item takes them as exceptions
// 2. the refuted reviews join the outcome evidence
// 3. the candidate is approved like any new version so the approval supersedes the refuted one
// 1. fails with ErrNarrowInvalid when no context is given
// 2. fails with ErrNarrowExhausted when no change context of the version would be left
// The second case is for keeping the version or for a retire by a named person
// The version is checked under the store lock so a concurrent retire never leaves a narrowed candidate of a retired version
func (l *Ledger) Narrow(
	ctx context.Context, id string, version int, contexts []evidence.Context, traceIDs []string, author string,
) (Knowledge, Set, error) {
	return l.appendCandidate(ctx, func(all Set, now time.Time) (Knowledge, error) {
		k, err := all.currentApproved(id, version)
		if err != nil {
			return Knowledge{}, err
		}
		draft, err := k.narrowed(contexts, l.contexts)
		if err != nil {
			return Knowledge{}, err
		}
		draft.Author = author
		draft.Evidence.OutcomeTraceIDs = append(slices.Clone(k.Evidence.OutcomeTraceIDs), traceIDs...)
		return all.propose(draft, now, l.contexts)
	})
}

// Proposes the next version of the current approved version with basis verified on the confirmed reviews
// 1. content and scope and exceptions and veto stay so the approval replaces the version without widening or lifting
// 2. the confirmed reviews join the outcome evidence
// 3. the candidate is approved like any new version by a named person
// 1. fails with ErrPromoteInvalid when no confirmed review is given
// 2. fails with ErrPromoteVerified when the version is verified already
// The version is checked under the store lock like Narrow
func (l *Ledger) Promote(ctx context.Context, id string, version int, traceIDs []string, author string) (Knowledge, Set, error) {
	return l.appendCandidate(ctx, func(all Set, now time.Time) (Knowledge, error) {
		k, err := all.currentApproved(id, version)
		if err != nil {
			return Knowledge{}, err
		}
		if len(traceIDs) == 0 {
			return Knowledge{}, fmt.Errorf("%w: %s v%d has no confirmed review", ErrPromoteInvalid, id, version)
		}
		if k.Basis == BasisVerified {
			return Knowledge{}, fmt.Errorf("%w: %s v%d", ErrPromoteVerified, id, version)
		}
		k.Basis, k.Author = BasisVerified, author
		k.Evidence.OutcomeTraceIDs = slices.Concat(k.Evidence.OutcomeTraceIDs, traceIDs)
		return all.propose(k, now, l.contexts)
	})
}

// Fails unless the version is the approved one of its id
// A superseded or retired version cannot be reaffirmed or narrowed
func (s Set) currentApproved(id string, version int) (Knowledge, error) {
	history, err := s.historyOf(id)
	if err != nil {
		return Knowledge{}, err
	}
	cur := history.current(id)
	if cur == nil || cur.Status != StatusApproved || cur.Version != version {
		return Knowledge{}, fmt.Errorf("%w: %s version %d is not the approved version", ErrVersionUnapproved, id, version)
	}
	return *cur, nil
}

// The item without the contexts
func (k Knowledge) narrowed(contexts []evidence.Context, declared evidence.Contexts) (Knowledge, error) {
	if len(contexts) == 0 {
		return Knowledge{}, fmt.Errorf("%w: no refuted change context", ErrNarrowInvalid)
	}
	if len(k.Scope.ChangeContexts) > 0 {
		k.Scope.ChangeContexts = slices.DeleteFunc(slices.Clone(k.Scope.ChangeContexts), func(c evidence.Context) bool {
			return slices.Contains(contexts, c)
		})
		if len(k.Scope.ChangeContexts) == 0 || k.Excluded(declared) {
			return Knowledge{}, fmt.Errorf("%w: %s v%d has none left without %v", ErrNarrowExhausted, k.ID, k.Version, contexts)
		}
		return k, nil
	}
	k.Exceptions = slices.Clone(k.Exceptions)
	for _, c := range contexts {
		if !slices.Contains(k.Exceptions, c) {
			k.Exceptions = append(k.Exceptions, c)
		}
	}
	if k.Excluded(declared) {
		return Knowledge{}, fmt.Errorf("%w: %s v%d would except every change context", ErrNarrowExhausted, k.ID, k.Version)
	}
	return k, nil
}
