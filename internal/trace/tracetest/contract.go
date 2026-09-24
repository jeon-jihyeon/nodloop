// Package tracetest is the contract suite every trace store must pass
package tracetest

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// Append only and newest first
// The suite appends traces so the store must be writable
type Store interface {
	Append(ctx context.Context, t trace.Trace) error
	Get(ctx context.Context, id string) (trace.Trace, error)
	List(ctx context.Context, f trace.Filter) (trace.Traces, error)
}

// Runs against an empty store
func Run(t *testing.T, store Store) {
	t.Helper()
	ctx := context.Background()
	empty, err := store.List(ctx, trace.Filter{})
	require.NoError(t, err)
	require.Empty(t, empty, "the suite needs an empty store")
	base := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	seed := []trace.Trace{
		{Name: trace.NameDiagnose, SessionID: "eval-1", Subject: "tq-001", Tags: []string{"feedback:off"}},
		{Name: trace.NameDiagnose, SessionID: "eval-1", Subject: "tq-001", Tags: []string{"feedback:on"}},
		{Name: trace.NameDiagnose, SessionID: "eval-1", Subject: "tq-002", Ref: "context-2", Tags: []string{"feedback:on"}},
		{Name: trace.NameContext, SessionID: "mcp-1", Subject: "tq-003"},
		{Name: trace.NameSelect, SessionID: "mcp-1", Subject: "tq-003", Ref: "context-3"},
	}
	for i := range seed {
		seed[i].ID = trace.NewID(base.Add(time.Duration(i) * time.Second))
		seed[i].Time = base.Add(time.Duration(i) * time.Second)
		seed[i].Input = json.RawMessage(fmt.Sprintf(`{"i":%d}`, i))
		seed[i].Usage = trace.Usage{InputTokens: 10 * (i + 1), CostUSD: 0.01}
		require.NoError(t, store.Append(ctx, seed[i]))
	}

	t.Run("get", func(t *testing.T) {
		t.Parallel()
		type want struct {
			trace trace.Trace
			err   error
		}
		tcs := []struct {
			name string
			args string
			want want
		}{
			{"known id returns every field", seed[1].ID, want{trace: seed[1]}},
			{"unknown id is not found", "no-such-id", want{err: trace.ErrNotFound}},
		}
		for _, tc := range tcs {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				got, err := store.Get(ctx, tc.args)
				assert.ErrorIs(t, err, tc.want.err)
				assert.Equal(t, tc.want.trace, got)
			})
		}
	})

	t.Run("list", func(t *testing.T) {
		t.Parallel()
		tcs := []struct {
			name string
			args trace.Filter
			want trace.Traces
		}{
			{"empty filter lists all newest first", trace.Filter{}, trace.Traces{seed[4], seed[3], seed[2], seed[1], seed[0]}},
			{"id keeps that trace", trace.Filter{ID: seed[0].ID}, trace.Traces{seed[0]}},
			{"name keeps that pipeline", trace.Filter{Name: trace.NameContext}, trace.Traces{seed[3]}},
			{"session keeps that session", trace.Filter{SessionID: "eval-1"}, trace.Traces{seed[2], seed[1], seed[0]}},
			{"subject keeps that subject", trace.Filter{Subject: "tq-002"}, trace.Traces{seed[2]}},
			{"ref keeps the traces of that context", trace.Filter{Ref: "context-3"}, trace.Traces{seed[4]}},
			{
				"tags keep traces carrying them",
				trace.Filter{Name: trace.NameDiagnose, Tags: []string{"feedback:on"}},
				trace.Traces{seed[2], seed[1]},
			},
			{"limit keeps the newest", trace.Filter{SessionID: "eval-1", Limit: 2}, trace.Traces{seed[2], seed[1]}},
			{"no match lists nothing", trace.Filter{Ref: "context-9"}, nil},
		}
		for _, tc := range tcs {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				got, err := store.List(ctx, tc.args)
				assert.NoError(t, err)
				assert.Equal(t, tc.want, got)
			})
		}
	})
}
