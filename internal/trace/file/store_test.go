package file_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/trace"
	"github.com/jeon-jihyeon/nodloop/internal/trace/file"
	"github.com/jeon-jihyeon/nodloop/internal/trace/tracetest"
)

func TestContract(t *testing.T) {
	store, err := file.New(t.TempDir())
	require.NoError(t, err)
	tracetest.Run(t, store)
}

func TestReplaysContract(t *testing.T) {
	store, err := file.NewReplays(t.TempDir())
	require.NoError(t, err)
	tracetest.Run(t, store)
}

// A replay lands in replays.jsonl and the review store of the same directory never sees it
func TestNewReplays(t *testing.T) {
	dir := t.TempDir()
	replays, err := file.NewReplays(dir)
	require.NoError(t, err)
	traces, err := file.New(dir)
	require.NoError(t, err)
	ctx := context.Background()
	tr := trace.Trace{
		ID: "r1", Name: trace.NameRun, SessionID: "c-1", Tags: []string{"replay"}, Input: json.RawMessage(`{}`),
		Output: json.RawMessage(`{}`),
	}
	require.NoError(t, replays.Append(ctx, tr))

	got, err := replays.List(ctx, trace.Filter{})
	require.NoError(t, err)
	reviews, err := traces.List(ctx, trace.Filter{})
	require.NoError(t, err)

	assert.Equal(t, trace.Traces{tr}, got)
	assert.Empty(t, reviews)
	assert.FileExists(t, filepath.Join(dir, "replays.jsonl"))
	assert.NoFileExists(t, filepath.Join(dir, "traces.jsonl"))
}

func TestNew(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "plain"), nil, 0o600))
	tcs := []struct {
		name string
		args string
		want error
	}{
		{"missing directory fails to open", "missing", file.ErrOpen},
		{"plain file in place of the directory fails to open", "plain", file.ErrOpen},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := file.New(filepath.Join(root, tc.args))
			assert.ErrorIs(t, err, tc.want)
			assert.Nil(t, got)
		})
	}
}

func TestStoreAppend(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "traces.jsonl"), 0o700))
	store, err := file.New(dir)
	require.NoError(t, err)
	tcs := []struct {
		name string
		args trace.Trace
		want error
	}{
		{"directory in place of the file fails to append", trace.Trace{ID: "a"}, file.ErrAppend},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.ErrorIs(t, store.Append(ctx, tc.args), tc.want)
		})
	}
}

func TestStoreGet(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "traces.jsonl"), 0o700))
	store, err := file.New(dir)
	require.NoError(t, err)
	type want struct {
		trace trace.Trace
		err   error
	}
	tcs := []struct {
		name string
		args string
		want want
	}{
		{"directory in place of the file fails to read", "a", want{err: file.ErrRead}},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := store.Get(ctx, tc.args)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.trace, got)
		})
	}
}

func TestStoreList(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "traces.jsonl"), 0o700))
	store, err := file.New(dir)
	require.NoError(t, err)
	type want struct {
		traces trace.Traces
		err    error
	}
	tcs := []struct {
		name string
		args trace.Filter
		want want
	}{
		{"directory in place of the file fails to read", trace.Filter{}, want{err: file.ErrRead}},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := store.List(ctx, tc.args)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.traces, got)
		})
	}
}
