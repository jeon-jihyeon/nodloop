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

	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// Append only and newest first
// The suite appends traces so the store must be writable
// Heads and Prune serve retention and Prune is the one removal
type Store interface {
	Append(ctx context.Context, t record.Trace) error
	Get(ctx context.Context, id string) (record.Trace, error)
	List(ctx context.Context, f record.TraceFilter) ([]record.Trace, error)
	After(ctx context.Context, f record.TraceFilter, from string) ([]record.Trace, string, error)
	Heads(ctx context.Context, names ...string) ([]record.Trace, error)
	Prune(ctx context.Context, decide func(context.Context) ([]string, error)) (int, error)
}

// Runs against an empty store
func Run(t *testing.T, store Store) {
	t.Helper()
	ctx := context.Background()
	empty, err := store.List(ctx, record.TraceFilter{})
	require.NoError(t, err)
	require.Empty(t, empty, "the suite needs an empty store")
	base := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	prune(ctx, t, store, base.Add(-time.Hour))
	seed := []record.Trace{
		{Name: "run", SessionID: "session-1", Subject: "task-1", Tags: []string{"feedback:off"}},
		{Name: "run", SessionID: "session-1", Subject: "task-1", Tags: []string{"feedback:on"}, Error: "timeout"},
		{Name: "run", SessionID: "session-1", Subject: "task-2", Ref: "ref-2", Tags: []string{"feedback:on"}},
		{Name: "check", SessionID: "session-2", Subject: "task-3"},
		{Name: "run", SessionID: "session-2", Subject: "task-3", Ref: "ref-3", Output: json.RawMessage(`"out"`)},
	}
	for i := range seed {
		seed[i].ID = trace.NewID(base.Add(time.Duration(i) * time.Second))
		seed[i].Time = base.Add(time.Duration(i) * time.Second)
		seed[i].Input = json.RawMessage(fmt.Sprintf(`{"i":%d}`, i))
		seed[i].Usage = record.Usage{InputTokens: 10 * (i + 1), CostUSD: 0.01}
		require.NoError(t, store.Append(ctx, seed[i]))
	}

	t.Run("get", func(t *testing.T) {
		t.Parallel()
		type want struct {
			trace record.Trace
			err   error
		}
		tcs := []struct {
			name string
			args string
			want want
		}{
			{"known id returns every field", seed[1].ID, want{trace: seed[1]}},
			{"unknown id is not found", "no-such-id", want{err: record.ErrTraceNotFound}},
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
			args record.TraceFilter
			want []record.Trace
		}{
			{"empty filter lists all newest first", record.TraceFilter{}, []record.Trace{seed[4], seed[3], seed[2], seed[1], seed[0]}},
			{"id keeps that trace", record.TraceFilter{ID: seed[0].ID}, []record.Trace{seed[0]}},
			{"name keeps that pipeline", record.TraceFilter{Name: "check"}, []record.Trace{seed[3]}},
			{"session keeps that session", record.TraceFilter{SessionID: "session-1"}, []record.Trace{seed[2], seed[1], seed[0]}},
			{"subject keeps that subject", record.TraceFilter{Subject: "task-2"}, []record.Trace{seed[2]}},
			{"ref keeps the traces of that ref", record.TraceFilter{Ref: "ref-3"}, []record.Trace{seed[4]}},
			{
				"tags keep traces carrying them",
				record.TraceFilter{Name: "run", Tags: []string{"feedback:on"}},
				[]record.Trace{seed[2], seed[1]},
			},
			{"limit keeps the newest", record.TraceFilter{SessionID: "session-1", Limit: 2}, []record.Trace{seed[2], seed[1]}},
			{"since leaves out older traces", record.TraceFilter{SessionID: "session-1", Since: seed[1].Time}, []record.Trace{seed[2], seed[1]}},
			{"names keep the traces of each", record.TraceFilter{Names: []string{"check", "run"}, Ref: "ref-3"}, []record.Trace{seed[4]}},
			{"names and a limit keep the newest of them", record.TraceFilter{Names: []string{"check"}, Limit: 1}, []record.Trace{seed[3]}},
			{"without output leaves the output out", record.TraceFilter{Ref: "ref-3", WithoutOutput: true}, []record.Trace{withoutOutput(seed[4])}},
			{"no match lists nothing", record.TraceFilter{Ref: "ref-9"}, nil},
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
			name                            string
			time                            time.Time
		}
		of := func(ts []record.Trace) []head {
			var out []head
			for _, tr := range ts {
				out = append(out, head{tr.ID, tr.Ref, string(tr.Input), tr.Error, string(tr.Output), tr.Name, tr.Time})
			}
			return out
		}
		bare := withoutOutput(seed[4])
		tcs := []struct {
			name string
			args []string
			want []record.Trace
		}{
			{"one name lists its traces newest first without output", []string{"run"}, []record.Trace{bare, seed[2], seed[1], seed[0]}},
			{"two names list both", []string{"run", "check"}, []record.Trace{bare, seed[3], seed[2], seed[1], seed[0]}},
			{"a name without traces lists nothing", []string{"classify"}, nil},
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

	t.Run("after", func(t *testing.T) {
		t.Parallel()
		after(ctx, t, store, seed)
	})
}

func withoutOutput(t record.Trace) record.Trace {
	t.Output = nil
	return t
}

// A read in append order goes on from the position the read before answered
// 1. the filter keeps what it keeps and the limit ends a read early
// 2. a read past the last trace answers nothing and the position it was given
// 3. a position no read answered fails with record.ErrPositionGone
func after(ctx context.Context, t *testing.T, store Store, seed []record.Trace) {
	t.Helper()
	runs := record.TraceFilter{Names: []string{"run"}, Limit: 2}
	first, at, err := store.After(ctx, runs, "")
	require.NoError(t, err)
	rest, end, err := store.After(ctx, record.TraceFilter{Names: []string{"run"}}, at)
	require.NoError(t, err)
	none, again, err := store.After(ctx, record.TraceFilter{}, end)
	require.NoError(t, err)
	all, _, err := store.After(ctx, record.TraceFilter{}, "")
	require.NoError(t, err)
	_, _, goneErr := store.After(ctx, record.TraceFilter{}, "no-such-position")

	assert.Equal(t, []record.Trace{seed[0], seed[1]}, first)
	assert.Equal(t, []record.Trace{seed[2], seed[4]}, rest, "the check between them is left out")
	assert.Empty(t, none)
	assert.Equal(t, end, again)
	assert.Equal(t, seed, all, "the empty position reads every trace oldest first")
	assert.ErrorIs(t, goneErr, record.ErrPositionGone)
}

// Prune removes the traces of the ids it is given and nothing else and leaves the store empty again
// It runs before the seed since the parallel cases read the seed after Run returns
func prune(ctx context.Context, t *testing.T, store Store, at time.Time) {
	t.Helper()
	traces := make([]record.Trace, 3)
	for i := range traces {
		stamp := at.Add(time.Duration(i) * time.Second)
		traces[i] = record.Trace{ID: trace.NewID(stamp), Name: "run", Time: stamp, Input: json.RawMessage(`{}`)}
		require.NoError(t, store.Append(ctx, traces[i]))
	}
	type want struct {
		removed int
		left    []record.Trace
	}
	tcs := []struct {
		name string
		args []string
		want want
	}{
		{"no id removes nothing", nil, want{0, []record.Trace{traces[2], traces[1], traces[0]}}},
		{"named ids go and unknown ones are skipped", []string{traces[0].ID, traces[2].ID, "no-such-id"}, want{2, []record.Trace{traces[1]}}},
		{"an id removed before removes nothing", []string{traces[0].ID}, want{0, []record.Trace{traces[1]}}},
		{"the last trace goes", []string{traces[1].ID}, want{1, nil}},
	}
	// Each case removes from what the one before left so they run in order
	for _, tc := range tcs {
		removed, err := store.Prune(ctx, func(context.Context) ([]string, error) { return tc.args, nil })
		require.NoError(t, err, tc.name)
		left, err := store.List(ctx, record.TraceFilter{})
		require.NoError(t, err, tc.name)
		assert.Equal(t, tc.want.removed, removed, tc.name)
		assert.Equal(t, tc.want.left, left, tc.name)
	}
}
