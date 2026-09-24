// Package feedbacktest is the contract suite every feedback store must pass
package feedbacktest

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
)

// Append only and newest first
// The suite appends feedback so the store must be writable
type Store interface {
	Append(ctx context.Context, f feedback.Feedback) error
	List(ctx context.Context, f feedback.Filter) ([]feedback.Feedback, error)
}

// Runs against an empty store
func Run(t *testing.T, store Store) {
	t.Helper()
	ctx := context.Background()
	empty, err := store.List(ctx, feedback.Filter{})
	require.NoError(t, err)
	require.Empty(t, empty, "the suite needs an empty store")
	base := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	seed := []feedback.Feedback{
		{TraceID: "t1", Verdict: feedback.VerdictReject, Reason: "wrong cause", Reviewer: "author"},
		{
			TraceID: "t1", Verdict: feedback.VerdictEdit, Reason: "reorder",
			Edited: json.RawMessage(`{"order":["db"]}`), Reviewer: "author",
		},
		{TraceID: "t2", Verdict: feedback.VerdictApprove, Reviewer: "author"},
		{TraceID: "t3", Verdict: feedback.VerdictReject, Reason: "no, use Edit", Reviewer: "session"},
	}
	for i := range seed {
		seed[i].Time = base.Add(time.Duration(i) * time.Second)
		require.NoError(t, store.Append(ctx, seed[i]))
	}
	tcs := []struct {
		name string
		args feedback.Filter
		want []feedback.Feedback
	}{
		{
			"empty filter lists every field newest first",
			feedback.Filter{},
			[]feedback.Feedback{seed[3], seed[2], seed[1], seed[0]},
		},
		{
			"trace id keeps that trace newest first",
			feedback.Filter{TraceID: "t1"},
			[]feedback.Feedback{seed[1], seed[0]},
		},
		{
			"verdicts with a limit keep the newest matches",
			feedback.Filter{Verdicts: []feedback.Verdict{feedback.VerdictEdit, feedback.VerdictReject}, Limit: 2},
			[]feedback.Feedback{seed[3], seed[1]},
		},
		{"reviewer keeps that reviewer", feedback.Filter{Reviewer: "session"}, []feedback.Feedback{seed[3]}},
		{"limit keeps the newest", feedback.Filter{Limit: 1}, []feedback.Feedback{seed[3]}},
		{"no match lists nothing", feedback.Filter{TraceID: "t9"}, nil},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := store.List(ctx, tc.args)
			assert.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
