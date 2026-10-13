//go:build unix

package jsonl_test

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/jsonl"
)

// A process of a release that locks the data file itself appends while a rewrite waits
// The rewrite takes the data file lock too so it reads the file after that append and the record stays
func TestFileRemoveWaitsForTheDataFileLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rows.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(`{"id":"a","n":1}`+"\n"+`{"id":"b","n":2}`+"\n"), 0o600))
	f, err := jsonl.Open[row](dir, "rows.jsonl")
	require.NoError(t, err)
	old, err := os.OpenFile(path, os.O_APPEND|os.O_RDWR, 0o600)
	require.NoError(t, err)
	t.Cleanup(func() { _ = old.Close() })
	require.NoError(t, syscall.Flock(int(old.Fd()), syscall.LOCK_EX))
	done := make(chan error, 1)

	go func() {
		_, err := f.Remove(func() (func(row) bool, error) { return func(r row) bool { return r.ID == "a" }, nil })
		done <- err
	}()
	select {
	case err := <-done:
		done <- err
	case <-time.After(200 * time.Millisecond):
	}
	_, err = old.WriteString(`{"id":"c","n":3}` + "\n")
	require.NoError(t, err)
	require.NoError(t, syscall.Flock(int(old.Fd()), syscall.LOCK_UN))

	require.NoError(t, <-done)
	got, err := f.All()
	require.NoError(t, err)
	assert.Equal(t, []row{{"b", 2}, {"c", 3}}, got)
}
