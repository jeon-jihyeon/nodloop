package file_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/feedback/feedbacktest"
	"github.com/jeon-jihyeon/nodloop/internal/feedback/file"
	"github.com/jeon-jihyeon/nodloop/internal/jsonl"
)

func TestContract(t *testing.T) {
	store, err := file.New(t.TempDir())
	require.NoError(t, err)
	feedbacktest.Run(t, store)
}

func TestNew(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "plain"), nil, 0o600))
	tcs := []struct {
		name string
		args string
		want error
	}{
		{"missing directory fails", "missing", file.ErrOpen},
		{"plain file in place of the directory fails", "plain", file.ErrOpen},
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
	tcs := []struct {
		name string
		// Created as a directory inside the store directory before the append
		args string
		want error
	}{
		{"append writes the record", "", nil},
		{"directory in place of the file fails", "feedback.jsonl", file.ErrAppend},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(dir, tc.args), 0o700))
			store, err := file.New(dir)
			require.NoError(t, err)
			assert.ErrorIs(t, store.Append(ctx, feedback.Feedback{TraceID: "a"}), tc.want)
		})
	}
}

func TestStoreList(t *testing.T) {
	tcs := []struct {
		name string
		// Created as a directory inside the store directory before the list
		args string
		want error
	}{
		{"missing file lists nothing", "", nil},
		{"directory in place of the file fails", "feedback.jsonl", file.ErrRead},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(dir, tc.args), 0o700))
			store, err := file.New(dir)
			require.NoError(t, err)
			got, err := store.List(ctx, feedback.Filter{})
			assert.ErrorIs(t, err, tc.want)
			assert.Nil(t, got)
		})
	}
}

// A line that is not JSON leaves the other records readable in both stores of the directory
// Its error wraps ErrRead and ErrCorrupt and the jsonl cause until Repair moves it aside
func TestStoresCorruptLine(t *testing.T) {
	ctx := context.Background()
	type checked interface {
		Check() (int, error)
		Repair() (int, error)
		Name() string
	}
	type args struct {
		// Appends one record with the given trace id
		add  func(dir, traceID string) error
		list func(dir string) (any, error)
		open func(dir string) (checked, error)
	}
	type want struct {
		name   string
		listed any
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"feedback",
			args{
				func(dir, id string) error {
					s, err := file.New(dir)
					return errors.Join(err, s.Append(ctx, feedback.Feedback{TraceID: id}))
				},
				func(dir string) (any, error) {
					s, _ := file.New(dir)
					return s.List(ctx, feedback.Filter{})
				},
				func(dir string) (checked, error) { return file.New(dir) },
			},
			want{"feedback.jsonl", []feedback.Feedback{{TraceID: "c"}, {TraceID: "a"}}},
		},
		{
			"outcomes",
			args{
				func(dir, id string) error {
					s, err := file.NewOutcomeStore(dir)
					return errors.Join(err, s.Append(ctx, feedback.Outcome{TraceID: id}))
				},
				func(dir string) (any, error) {
					s, _ := file.NewOutcomeStore(dir)
					return s.List(ctx, "")
				},
				func(dir string) (checked, error) { return file.NewOutcomeStore(dir) },
			},
			want{"outcomes.jsonl", []feedback.Outcome{{TraceID: "c"}, {TraceID: "a"}}},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			require.NoError(t, tc.args.add(dir, "a"))
			require.NoError(t, appendRaw(filepath.Join(dir, tc.want.name), "not json\n"))
			require.NoError(t, tc.args.add(dir, "c"))
			store, err := tc.args.open(dir)
			require.NoError(t, err)

			listed, listErr := tc.args.list(dir)
			n, checkErr := store.Check()

			for _, err := range []error{listErr, checkErr} {
				assert.ErrorIs(t, err, file.ErrRead)
				assert.ErrorIs(t, err, file.ErrCorrupt)
				assert.ErrorIs(t, err, jsonl.ErrCorrupt)
			}
			assert.Equal(t, tc.want.listed, listed)
			assert.Equal(t, 2, n)
			moved, err := store.Repair()
			require.NoError(t, err)
			assert.Equal(t, 1, moved)
			_, err = store.Check()
			assert.NoError(t, err)
			assert.Equal(t, tc.want.name, store.Name())
		})
	}
}

func appendRaw(path, line string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.WriteString(line)
	return errors.Join(err, f.Close())
}
