package file_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge/knowledgetest"
)

func TestContract(t *testing.T) {
	store, err := file.New(t.TempDir())
	require.NoError(t, err)
	knowledgetest.Run(t, store)
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
	blocked := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(blocked, "knowledge.jsonl"), 0o700))
	type want struct {
		perm os.FileMode
		err  error
	}
	tcs := []struct {
		name string
		args string
		want want
	}{
		{"first append creates an owner only file", t.TempDir(), want{perm: 0o600}},
		{"directory in place of the file fails to append", blocked, want{os.ModeDir | 0o700, file.ErrAppend}},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, err := file.New(tc.args)
			require.NoError(t, err)
			assert.ErrorIs(t, store.Append(ctx, knowledge.Knowledge{ID: "a"}), tc.want.err)
			info, err := os.Stat(filepath.Join(tc.args, "knowledge.jsonl"))
			require.NoError(t, err)
			assert.Equal(t, tc.want.perm, info.Mode()&(os.ModeDir|os.ModePerm))
		})
	}
}

func TestStoreList(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "knowledge.jsonl"), 0o700))
	store, err := file.New(dir)
	require.NoError(t, err)
	type want struct {
		records []knowledge.Knowledge
		err     error
	}
	tcs := []struct {
		name string
		want want
	}{
		{"directory in place of the file fails to read", want{err: file.ErrRead}},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := store.List(ctx)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.records, got)
		})
	}
}

func TestStoreAppendDecided(t *testing.T) {
	blocked := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(blocked, "knowledge.jsonl"), 0o700))
	type args struct {
		dir     string
		refusal error
	}
	type want struct {
		err       error
		notAppend bool
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a refusal comes back without ErrAppend", args{t.TempDir(), knowledge.ErrNotFound}, want{knowledge.ErrNotFound, true}},
		{"directory in place of the file fails with ErrAppend", args{blocked, nil}, want{file.ErrAppend, false}},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, err := file.New(tc.args.dir)
			require.NoError(t, err)
			err = store.AppendDecided(ctx, func(knowledge.Set) ([]knowledge.Knowledge, error) {
				return []knowledge.Knowledge{{ID: "a"}}, tc.args.refusal
			})
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.notAppend, !errors.Is(err, file.ErrAppend))
		})
	}
}

func TestStoreAppendDecidedConcurrent(t *testing.T) {
	tcs := []struct {
		name  string
		seeds []knowledge.Knowledge
	}{
		{"empty store", nil},
		{"seeded store", []knowledge.Knowledge{{ID: "old"}, {ID: "new"}}},
	}
	ctx := context.Background()
	// Enough stores on one file to collide on the lock while the test stays fast
	const writers = 8
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			seed, err := file.New(dir)
			require.NoError(t, err)
			for _, item := range tc.seeds {
				require.NoError(t, seed.Append(ctx, item))
			}
			start, results := make(chan struct{}), make(chan error, writers)
			for i := range writers {
				store, err := file.New(dir)
				require.NoError(t, err)
				go func() {
					<-start
					results <- store.AppendDecided(ctx, func(all knowledge.Set) ([]knowledge.Knowledge, error) {
						if _, ok := all.Find("winner"); ok {
							return nil, knowledge.ErrTransitionInvalid
						}
						return []knowledge.Knowledge{{ID: "winner", Version: i}}, nil
					})
				}()
			}
			close(start)
			counts := map[bool]int{}
			for range writers {
				err := <-results
				assert.True(t, err == nil || errors.Is(err, knowledge.ErrTransitionInvalid))
				counts[err == nil]++
			}
			assert.Equal(t, map[bool]int{true: 1, false: writers - 1}, counts, "only one writer finds no winner")
			got, err := seed.List(ctx)
			assert.NoError(t, err)
			assert.Len(t, got, len(tc.seeds)+1)
		})
	}
}
