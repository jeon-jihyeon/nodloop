package knowledge

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/trace"
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
// 3. the exports follow because they name the approver
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
	return k, l.exported(ctx, k)
}

// Proposes the next version of the approved item that excepts the values of the key where its runs were refuted
// 1. the refuted runs join the outcome evidence
// 2. fails with ErrNarrowInvalid for an item without a run scope or without values
// 3. fails with ErrNarrowExhausted when the scope requires that key and every value it allows would be excepted
// The version is checked under the store lock like Narrow
func (l *Ledger) Narrow(
	ctx context.Context, id string, version int, key string, values, traceIDs []string, author string,
) (Knowledge, Set, error) {
	return l.appendCandidate(ctx, func(all Set, now time.Time) (Knowledge, error) {
		k, err := all.currentApproved(id, version)
		if err != nil {
			return Knowledge{}, err
		}
		draft, err := k.narrowedRun(key, values)
		if err != nil {
			return Knowledge{}, err
		}
		draft.Author = author
		draft.Evidence.OutcomeTraceIDs = append(slices.Clone(k.Evidence.OutcomeTraceIDs), traceIDs...)
		return all.propose(draft, now)
	})
}

// The run item with the values of the key added to its exceptions
func (k Knowledge) narrowedRun(key string, values []string) (Knowledge, error) {
	if k.Run == nil {
		return Knowledge{}, fmt.Errorf("%w: %s v%d is not scoped to runs", ErrNarrowInvalid, k.ID, k.Version)
	}
	if key == "" || len(values) == 0 {
		return Knowledge{}, fmt.Errorf("%w: no refuted value of %q", ErrNarrowInvalid, key)
	}
	run := RunScope{Producer: k.Run.Producer, Labels: maps.Clone(k.Run.Labels), Except: maps.Clone(k.Run.Except)}
	if run.Except == nil {
		run.Except = trace.Labels{}
	}
	run.Except[key] = slices.Concat(run.Except[key], values)
	if allowed, ok := run.Labels[key]; ok && !slices.ContainsFunc(allowed, func(v string) bool { return !slices.Contains(run.Except[key], v) }) {
		return Knowledge{}, fmt.Errorf("%w: %s v%d would except every %s it allows", ErrNarrowExhausted, k.ID, k.Version, key)
	}
	k.Run = &run
	return k, nil
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
		return all.propose(k, now)
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
