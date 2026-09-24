package file_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/feedback/feedbacktest"
	"github.com/jeon-jihyeon/nodloop/internal/feedback/file"
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
