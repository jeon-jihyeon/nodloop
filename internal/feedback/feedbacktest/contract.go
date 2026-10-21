// Package feedbacktest is the contract suite every feedback store must pass
package feedbacktest

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/record"
)

// Append only and newest first
// The suite appends feedback so the store must be writable
type Store interface {
	Append(ctx context.Context, f record.Feedback) error
	List(ctx context.Context, f record.FeedbackFilter) ([]record.Feedback, error)
	After(ctx context.Context, from string, limit int) ([]record.Feedback, string, error)
}

// Runs against an empty store
func Run(t *testing.T, store Store) {
	t.Helper()
	ctx := context.Background()
	empty, err := store.List(ctx, record.FeedbackFilter{})
	require.NoError(t, err)
	require.Empty(t, empty, "the suite needs an empty store")
	base := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	seed := []record.Feedback{
		{TraceID: "t1", Verdict: "reject", Reason: "wrong cause", Reviewer: "author"},
		{
			TraceID: "t1", Verdict: "edit", Reason: "reorder",
			Edited: json.RawMessage(`{"order":["db"]}`), Reviewer: "author", Proof: "key",
		},
		{TraceID: "t2", Verdict: "approve", Reviewer: "author"},
		{TraceID: "t3", Verdict: "reject", Reason: "no, use Edit", Reviewer: "session"},
	}
	for i := range seed {
		seed[i].Time = base.Add(time.Duration(i) * time.Second)
		require.NoError(t, store.Append(ctx, seed[i]))
	}
	tcs := []struct {
		name string
		args record.FeedbackFilter
		want []record.Feedback
	}{
		{
			"empty filter lists every field newest first",
			record.FeedbackFilter{},
			[]record.Feedback{seed[3], seed[2], seed[1], seed[0]},
		},
		{
			"trace id keeps that trace newest first",
			record.FeedbackFilter{TraceID: "t1"},
			[]record.Feedback{seed[1], seed[0]},
		},
		{
			"verdicts with a limit keep the newest matches",
			record.FeedbackFilter{Verdicts: []string{"edit", "reject"}, Limit: 2},
			[]record.Feedback{seed[3], seed[1]},
		},
		{"reviewer keeps that reviewer", record.FeedbackFilter{Reviewer: "session"}, []record.Feedback{seed[3]}},
		{"limit keeps the newest", record.FeedbackFilter{Limit: 1}, []record.Feedback{seed[3]}},
		{"since leaves out older verdicts", record.FeedbackFilter{TraceID: "t1", Since: seed[1].Time}, []record.Feedback{seed[1]}},
		{"no match lists nothing", record.FeedbackFilter{TraceID: "t9"}, nil},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := store.List(ctx, tc.args)
			assert.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
	t.Run("after reads in append order from the position the read before answered", func(t *testing.T) {
		t.Parallel()
		first, at, err := store.After(ctx, "", 3)
		require.NoError(t, err)
		rest, end, err := store.After(ctx, at, 0)
		require.NoError(t, err)
		none, again, err := store.After(ctx, end, 0)
		require.NoError(t, err)
		_, _, goneErr := store.After(ctx, "no-such-position", 0)

		assert.Equal(t, seed[:3], first)
		assert.Equal(t, seed[3:], rest)
		assert.Empty(t, none)
		assert.Equal(t, end, again, "a read past the last verdict answers its position")
		assert.ErrorIs(t, goneErr, record.ErrPositionGone)
	})
}
