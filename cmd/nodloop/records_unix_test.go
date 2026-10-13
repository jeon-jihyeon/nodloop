//go:build unix

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/retain"
)

// A prune while another process holds the marker removes nothing and says so
func TestRunRecordsPruneWhileAnotherRuns(t *testing.T) {
	getenv, records := pruneHome(t, "30")
	held, err := os.OpenFile(filepath.Join(records, retain.MarkerFile), os.O_CREATE|os.O_RDWR, 0o600)
	require.NoError(t, err)
	t.Cleanup(func() { _ = held.Close() })
	require.NoError(t, syscall.Flock(int(held.Fd()), syscall.LOCK_EX))
	var stdout, stderr bytes.Buffer

	code := runRecords([]string{"prune"}, getenv, func() time.Time { return pruneNow }, &stdout, &stderr)

	assert.Equal(t, 1, code)
	assert.Empty(t, stdout.String())
	assert.Equal(t, "nodloop records: another prune is running\n", stderr.String())
	assert.Equal(t, []string{"old-unjudged", "old-classify", "old-judged", "recent"}, traceIDs(t, records))
}
