package textkey

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A create that finds the name taken keeps the key there and leaves no temp file
func TestCreateKeepsAKeyThatExists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	require.NoError(t, os.WriteFile(path, []byte("taken"), 0o600))

	err := file(path).create()

	assert.ErrorIs(t, err, ErrCreate)
	assert.ErrorIs(t, err, fs.ErrExist)
	kept, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	entries, readErr := os.ReadDir(dir)
	require.NoError(t, readErr)
	assert.Equal(t, "taken", string(kept))
	assert.Len(t, entries, 1)
}
