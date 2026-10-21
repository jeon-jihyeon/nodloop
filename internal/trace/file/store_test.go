package file_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/jsonl"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/trace/file"
	"github.com/jeon-jihyeon/nodloop/internal/trace/tracetest"
)

func TestContract(t *testing.T) {
	store, err := file.New(t.TempDir())
	require.NoError(t, err)
	tracetest.Run(t, store)
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
		args record.Trace
		want error
	}{
		{"directory in place of the file fails to append", record.Trace{ID: "a"}, file.ErrAppend},
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
		trace record.Trace
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
		traces []record.Trace
		err    error
	}
	tcs := []struct {
		name string
		args record.TraceFilter
		want want
	}{
		{"directory in place of the file fails to read", record.TraceFilter{}, want{err: file.ErrRead}},
		{"directory in place of the file fails a read without output", record.TraceFilter{WithoutOutput: true}, want{err: file.ErrRead}},
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
	t.Run("directory in place of the file fails a read in append order", func(t *testing.T) {
		t.Parallel()
		got, at, err := store.After(ctx, record.TraceFilter{}, "")
		assert.ErrorIs(t, err, file.ErrRead)
		assert.Empty(t, got)
		assert.Empty(t, at)
	})
}

// A line that is not JSON leaves the other traces readable
// Its error wraps ErrRead and ErrCorrupt and the jsonl cause until Repair moves it aside
func TestStoreCorruptLine(t *testing.T) {
	dir := t.TempDir()
	store, err := file.New(dir)
	require.NoError(t, err)
	ctx := context.Background()
	a := record.Trace{ID: "a", Name: "run", Input: json.RawMessage(`{}`), Output: json.RawMessage(`"x"`)}
	c := record.Trace{ID: "c", Name: "run", Input: json.RawMessage(`{}`), Output: json.RawMessage(`"y"`)}
	require.NoError(t, store.Append(ctx, a))
	require.NoError(t, appendRaw(filepath.Join(dir, "traces.jsonl"), "not json\n"))
	require.NoError(t, store.Append(ctx, c))

	listed, listErr := store.List(ctx, record.TraceFilter{})
	bare, bareErr := store.List(ctx, record.TraceFilter{WithoutOutput: true})
	appended, _, afterErr := store.After(ctx, record.TraceFilter{}, "")
	got, getErr := store.Get(ctx, "a")
	n, checkErr := store.Check()

	for _, err := range []error{listErr, bareErr, afterErr, getErr, checkErr} {
		assert.ErrorIs(t, err, file.ErrRead)
		assert.ErrorIs(t, err, file.ErrCorrupt)
		assert.ErrorIs(t, err, jsonl.ErrCorrupt)
	}
	assert.Equal(t, []record.Trace{c, a}, listed)
	assert.Equal(t, []record.Trace{{ID: "c", Name: "run", Input: c.Input}, {ID: "a", Name: "run", Input: a.Input}}, bare)
	assert.Equal(t, []record.Trace{a, c}, appended)
	assert.Equal(t, a, got, "get returns the trace found with the error as list does")
	assert.Equal(t, 2, n)
	moved, err := store.Repair()
	require.NoError(t, err)
	assert.Equal(t, 1, moved)
	n, err = store.Check()
	assert.NoError(t, err)
	assert.Equal(t, 2, n)
	assert.Equal(t, "traces.jsonl", store.Name())
}

func appendRaw(path, line string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.WriteString(line)
	return errors.Join(err, f.Close())
}
