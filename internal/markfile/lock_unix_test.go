//go:build unix

package markfile_test

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/markfile"
	"github.com/jeon-jihyeon/nodloop/internal/record"
)

// A process that finds the file locked gets no hold at once
func TestHoldSkipsAHeldLock(t *testing.T) {
	tcs := []struct {
		name string
		// The flock another process holds on the file
		args int
	}{
		{"an exclusive lock gives no hold", syscall.LOCK_EX},
		{"a shared lock gives no hold", syscall.LOCK_SH},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "mark.json")
			other, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
			require.NoError(t, err)
			t.Cleanup(func() { _ = other.Close() })
			require.NoError(t, syscall.Flock(int(other.Fd()), tc.args))
			type result struct {
				held *markfile.Held[record.Mark]
				err  error
			}
			done := make(chan result, 1)

			// The hold runs aside so a hold that waits for the lock fails the test instead of hanging it
			go func() {
				held, err := markfile.Hold[record.Mark](path)
				done <- result{held, err}
			}()
			var got result
			select {
			case got = <-done:
			case <-time.After(5 * time.Second):
				require.FailNow(t, "the hold waited for the lock")
			}

			assert.NoError(t, got.err)
			assert.Nil(t, got.held)
		})
	}
}
