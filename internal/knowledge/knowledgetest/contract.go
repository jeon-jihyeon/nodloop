// Package knowledgetest is the contract suite every knowledge store must pass
package knowledgetest

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
)

// The store as the suite drives it
// Append seeds the records that the ledger itself only ever writes through AppendDecided
type Store interface {
	knowledge.Store
	Append(ctx context.Context, k knowledge.Knowledge) error
}

// Runs against an empty store
func Run(t *testing.T, store Store) {
	t.Helper()
	ctx := context.Background()
	empty, err := store.List(ctx)
	require.NoError(t, err)
	require.Empty(t, empty, "the suite needs an empty store")
	base := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	candidate := knowledge.Knowledge{
		ID: "a", Version: 1, Kind: knowledge.KindMeaning, Content: "one",
		Evidence: knowledge.Evidence{ParagraphIDs: []string{"p"}}, Basis: knowledge.BasisStated,
		Status: knowledge.StatusCandidate, Author: "x", Time: base,
	}
	approved := candidate
	approved.Status, approved.Approver, approved.Time = knowledge.StatusApproved, "y", base.Add(time.Second)
	other := knowledge.Knowledge{
		ID: "b", Version: 1, Kind: knowledge.KindJudgment, Content: "two",
		Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{"t"}}, Basis: knowledge.BasisVerified,
		Status: knowledge.StatusCandidate, Author: "x", Time: base.Add(2 * time.Second),
	}
	for _, k := range []knowledge.Knowledge{candidate, approved, other} {
		require.NoError(t, store.Append(ctx, k))
	}
	listed, err := store.List(ctx)
	require.NoError(t, err)
	reaffirmed := approved
	reaffirmed.Approver, reaffirmed.ReviewedAt, reaffirmed.Time = "z", base.Add(3*time.Second), base.Add(3*time.Second)
	retired := other
	retired.Status, retired.Approver, retired.Time = knowledge.StatusRetired, "z", base.Add(3*time.Second)
	var seen knowledge.Set
	require.NoError(t, store.AppendDecided(ctx, func(all knowledge.Set) ([]knowledge.Knowledge, error) {
		seen = all
		return []knowledge.Knowledge{reaffirmed, retired}, nil
	}), "the batch goes in")
	require.Equal(t, knowledge.Set(listed), seen, "decide gets the records as List gives them")
	require.ErrorIs(t, store.AppendDecided(ctx, func(knowledge.Set) ([]knowledge.Knowledge, error) {
		return nil, knowledge.ErrNotFound
	}), knowledge.ErrNotFound, "a refusal comes back as it is and nothing goes in")

	t.Run("list gives every record newest first", func(t *testing.T) {
		t.Parallel()
		got, err := store.List(ctx)
		assert.NoError(t, err)
		assert.Equal(t, []knowledge.Knowledge{retired, reaffirmed, other, approved, candidate}, got)
	})
}
