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
// Heads and Prune serve retention and Prune is the one removal
type Store interface {
	Append(ctx context.Context, t trace.Trace) error
	Get(ctx context.Context, id string) (trace.Trace, error)
	List(ctx context.Context, f trace.Filter) (trace.Traces, error)
	Heads(ctx context.Context, names ...trace.Name) (trace.Traces, error)
	Prune(ctx context.Context, decide func(context.Context) ([]string, error)) (int, error)
}

// Runs against an empty store
func Run(t *testing.T, store Store) {
	t.Helper()
	ctx := context.Background()
	empty, err := store.List(ctx, trace.Filter{})
	require.NoError(t, err)
	require.Empty(t, empty, "the suite needs an empty store")
	base := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	prune(ctx, t, store, base.Add(-time.Hour))
	seed := []trace.Trace{
		{Name: trace.NameRun, SessionID: "session-1", Subject: "task-1", Tags: []string{"feedback:off"}},
		{Name: trace.NameRun, SessionID: "session-1", Subject: "task-1", Tags: []string{"feedback:on"}, Error: "timeout"},
		{Name: trace.NameRun, SessionID: "session-1", Subject: "task-2", Ref: "ref-2", Tags: []string{"feedback:on"}},
		{Name: trace.NameCheck, SessionID: "session-2", Subject: "task-3"},
		{Name: trace.NameRun, SessionID: "session-2", Subject: "task-3", Ref: "ref-3", Output: json.RawMessage(`"out"`)},
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
			{"name keeps that pipeline", trace.Filter{Name: trace.NameCheck}, trace.Traces{seed[3]}},
			{"session keeps that session", trace.Filter{SessionID: "session-1"}, trace.Traces{seed[2], seed[1], seed[0]}},
			{"subject keeps that subject", trace.Filter{Subject: "task-2"}, trace.Traces{seed[2]}},
			{"ref keeps the traces of that ref", trace.Filter{Ref: "ref-3"}, trace.Traces{seed[4]}},
			{
				"tags keep traces carrying them",
				trace.Filter{Name: trace.NameRun, Tags: []string{"feedback:on"}},
				trace.Traces{seed[2], seed[1]},
			},
			{"limit keeps the newest", trace.Filter{SessionID: "session-1", Limit: 2}, trace.Traces{seed[2], seed[1]}},
			{"since leaves out older traces", trace.Filter{SessionID: "session-1", Since: seed[1].Time}, trace.Traces{seed[2], seed[1]}},
			{"no match lists nothing", trace.Filter{Ref: "ref-9"}, nil},
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

	t.Run("heads", func(t *testing.T) {
		t.Parallel()
		// The fields every store keeps in a head and the output it leaves out
		type head struct {
			id, ref, input, failure, output string
			name                            trace.Name
			time                            time.Time
		}
		of := func(ts trace.Traces) []head {
			var out []head
			for _, tr := range ts {
				out = append(out, head{tr.ID, tr.Ref, string(tr.Input), tr.Error, string(tr.Output), tr.Name, tr.Time})
			}
			return out
		}
		bare := seed[4]
		bare.Output = nil
		tcs := []struct {
			name string
			args []trace.Name
			want trace.Traces
		}{
			{"one name lists its traces newest first without output", []trace.Name{trace.NameRun}, trace.Traces{bare, seed[2], seed[1], seed[0]}},
			{"two names list both", []trace.Name{trace.NameRun, trace.NameCheck}, trace.Traces{bare, seed[3], seed[2], seed[1], seed[0]}},
			{"a name without traces lists nothing", []trace.Name{trace.NameClassify}, nil},
		}
		for _, tc := range tcs {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				got, err := store.Heads(ctx, tc.args...)
				assert.NoError(t, err)
				assert.Equal(t, of(tc.want), of(got))
			})
		}
	})
}

// Prune removes the traces of the ids it is given and nothing else and leaves the store empty again
// It runs before the seed since the parallel cases read the seed after Run returns
func prune(ctx context.Context, t *testing.T, store Store, at time.Time) {
	t.Helper()
	traces := make([]trace.Trace, 3)
	for i := range traces {
		stamp := at.Add(time.Duration(i) * time.Second)
		traces[i] = trace.Trace{ID: trace.NewID(stamp), Name: trace.NameRun, Time: stamp, Input: json.RawMessage(`{}`)}
		require.NoError(t, store.Append(ctx, traces[i]))
	}
	type want struct {
		removed int
		left    trace.Traces
	}
	tcs := []struct {
		name string
		args []string
		want want
	}{
		{"no id removes nothing", nil, want{0, trace.Traces{traces[2], traces[1], traces[0]}}},
		{"named ids go and unknown ones are skipped", []string{traces[0].ID, traces[2].ID, "no-such-id"}, want{2, trace.Traces{traces[1]}}},
		{"an id removed before removes nothing", []string{traces[0].ID}, want{0, trace.Traces{traces[1]}}},
		{"the last trace goes", []string{traces[1].ID}, want{1, nil}},
	}
	// Each case removes from what the one before left so they run in order
	for _, tc := range tcs {
		removed, err := store.Prune(ctx, func(context.Context) ([]string, error) { return tc.args, nil })
		require.NoError(t, err, tc.name)
		left, err := store.List(ctx, trace.Filter{})
		require.NoError(t, err, tc.name)
		assert.Equal(t, tc.want.removed, removed, tc.name)
		assert.Equal(t, tc.want.left, left, tc.name)
	}
}
