// Package knowledgetest is the contract suite every knowledge store must pass
package knowledgetest

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/record"
)

// The store as the suite drives it
// Append seeds the records that the ledger itself only ever writes through AppendDecided
type Store interface {
	knowledge.Store
	Append(ctx context.Context, k record.Knowledge) error
	After(ctx context.Context, from string, limit int) ([]record.Knowledge, string, error)
}

// Runs against an empty store
func Run(t *testing.T, store Store) {
	t.Helper()
	ctx := context.Background()
	empty, err := store.List(ctx)
	require.NoError(t, err)
	require.Empty(t, empty, "the suite needs an empty store")
	base := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	candidate := record.Knowledge{
		ID: "a", Version: 1, Kind: "meaning", Content: "one",
		Evidence: record.Evidence{ParagraphIDs: []string{"p"}}, Basis: "stated",
		Status: "candidate", Author: "x", Time: base,
	}
	approved := candidate
	approved.Status, approved.Approver, approved.Time = "approved", "y", base.Add(time.Second)
	other := record.Knowledge{
		ID: "b", Version: 1, Kind: "judgment", Content: "two",
		Evidence: record.Evidence{FeedbackTraceIDs: []string{"t"}}, Basis: "verified",
		Status: "candidate", Author: "x", Time: base.Add(2 * time.Second),
	}
	for _, k := range []record.Knowledge{candidate, approved, other} {
		require.NoError(t, store.Append(ctx, k))
	}
	listed, err := store.List(ctx)
	require.NoError(t, err)
	reaffirmed := approved
	reaffirmed.Approver, reaffirmed.ReviewedAt, reaffirmed.Time = "z", base.Add(3*time.Second), base.Add(3*time.Second)
	retired := other
	retired.Status, retired.Approver, retired.Time = "retired", "z", base.Add(3*time.Second)
	var seen []record.Knowledge
	require.NoError(t, store.AppendDecided(ctx, func(all []record.Knowledge) ([]record.Knowledge, error) {
		seen = all
		return []record.Knowledge{reaffirmed, retired}, nil
	}), "the batch goes in")
	require.Equal(t, listed, seen, "decide gets the records as List gives them")
	require.ErrorIs(t, store.AppendDecided(ctx, func([]record.Knowledge) ([]record.Knowledge, error) {
		return nil, knowledge.ErrNotFound
	}), knowledge.ErrNotFound, "a refusal comes back as it is and nothing goes in")

	t.Run("list gives every record newest first", func(t *testing.T) {
		t.Parallel()
		got, err := store.List(ctx)
		assert.NoError(t, err)
		assert.Equal(t, []record.Knowledge{retired, reaffirmed, other, approved, candidate}, got)
	})
	t.Run("after reads in append order from the position the read before answered", func(t *testing.T) {
		t.Parallel()
		first, at, err := store.After(ctx, "", 2)
		require.NoError(t, err)
		rest, end, err := store.After(ctx, at, 0)
		require.NoError(t, err)
		none, again, err := store.After(ctx, end, 0)
		require.NoError(t, err)
		_, _, goneErr := store.After(ctx, "no-such-position", 0)

		assert.Equal(t, []record.Knowledge{candidate, approved}, first)
		assert.Equal(t, []record.Knowledge{other, reaffirmed, retired}, rest, "the records of one decision keep their order")
		assert.Empty(t, none)
		assert.Equal(t, end, again, "a read past the last record answers its position")
		assert.ErrorIs(t, goneErr, record.ErrPositionGone)
	})
}
