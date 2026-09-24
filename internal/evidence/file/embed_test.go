package file_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/evidence/file"
)

func TestWriteDemoCopiesTheDataSet(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "events.csv"), []byte("stale"), 0o600))
	require.NoError(t, file.WriteDemo(dir))
	// Every file under the root keyed by its relative path
	// A file the embed directive misses shows up as a difference
	snapshot := func(root string) map[string]string {
		files := map[string]string{}
		err := fs.WalkDir(os.DirFS(root), ".", func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			b, err := fs.ReadFile(os.DirFS(root), path)
			files[path] = string(b)
			return err
		})
		require.NoError(t, err)
		return files
	}
	assert.Equal(t, snapshot("testdata"), snapshot(dir))
}

func TestWriteDemoFailsOnAFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "occupied")
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	assert.ErrorIs(t, file.WriteDemo(path), syscall.ENOTDIR)
}
