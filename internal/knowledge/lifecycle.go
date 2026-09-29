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
// 2. the append fails with ErrRecordsChanged when the records changed since they were read
// So a reaffirm never reopens a version that a concurrent retire closed
func (l *Ledger) Reaffirm(ctx context.Context, id string, version int, approver string) (Knowledge, error) {
	if approver == "" {
		return Knowledge{}, fmt.Errorf("%w: reaffirm needs one", ErrApproverRequired)
	}
	all, err := l.All(ctx)
	if err != nil {
		return Knowledge{}, err
	}
	k, err := all.currentApproved(id, version)
	if err != nil {
		return Knowledge{}, err
	}
	k.Time = l.now().UTC()
	k.ReviewedAt, k.Approver = k.Time, approver
	if err := l.store.AppendIfUnchanged(ctx, k, all); err != nil {
		return Knowledge{}, err
	}
	return k, nil
}

// Proposes the next version of the current approved version without the change contexts where its reviews were refuted
// 1. a scoped item drops the contexts from its change contexts and an unscoped item takes them as exceptions
// 2. the refuted reviews join the outcome evidence
// 3. the candidate is approved like any new version so the approval supersedes the refuted one
// Fails with ErrNarrowInvalid when no context is given or none would be left
// That case is for a retire by a named person
func (l *Ledger) Narrow(
	ctx context.Context, id string, version int, contexts []evidence.Context, traceIDs []string, author string,
) (Knowledge, Set, error) {
	all, err := l.All(ctx)
	if err != nil {
		return Knowledge{}, nil, err
	}
	k, err := all.currentApproved(id, version)
	if err != nil {
		return Knowledge{}, nil, err
	}
	draft, err := k.narrowed(contexts)
	if err != nil {
		return Knowledge{}, nil, err
	}
	draft.Author = author
	draft.Evidence.OutcomeTraceIDs = append(slices.Clone(k.Evidence.OutcomeTraceIDs), traceIDs...)
	return l.Propose(ctx, draft)
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
func (k Knowledge) narrowed(contexts []evidence.Context) (Knowledge, error) {
	if len(contexts) == 0 {
		return Knowledge{}, fmt.Errorf("%w: no refuted change context", ErrNarrowInvalid)
	}
	if len(k.Scope.ChangeContexts) > 0 {
		k.Scope.ChangeContexts = slices.DeleteFunc(slices.Clone(k.Scope.ChangeContexts), func(c evidence.Context) bool {
			return slices.Contains(contexts, c)
		})
		if len(k.Scope.ChangeContexts) == 0 {
			return Knowledge{}, fmt.Errorf("%w: no change context would be left", ErrNarrowInvalid)
		}
		return k, nil
	}
	k.Exceptions = slices.Clone(k.Exceptions)
	for _, c := range contexts {
		if !slices.Contains(k.Exceptions, c) {
			k.Exceptions = append(k.Exceptions, c)
		}
	}
	if k.excepts(evidence.Contexts()) {
		return Knowledge{}, fmt.Errorf("%w: every change context would be an exception", ErrNarrowInvalid)
	}
	return k, nil
}
