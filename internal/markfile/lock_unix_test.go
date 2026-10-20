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

// A wait takes the file only once the process that holds it lets go and then reads the mark it left
func TestWaitTakesTheLockWhenFree(t *testing.T) {
	tcs := []struct {
		name string
		// Whether another process holds the file when the wait starts
		args bool
		want record.Mark
	}{
		{"a free file is taken at once", false, record.Mark{}},
		{"a held file is taken after its release with the mark written before it", true, record.Mark{At: at}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "mark.json")
			var other *markfile.Held[record.Mark]
			if tc.args {
				var err error
				other, err = markfile.Hold[record.Mark](path)
				require.NoError(t, err)
				require.NotNil(t, other)
			}
			type result struct {
				held *markfile.Held[record.Mark]
				err  error
			}
			done := make(chan result, 1)

			go func() {
				held, err := markfile.Wait[record.Mark](path)
				done <- result{held, err}
			}()
			if other != nil {
				select {
				case <-done:
					require.FailNow(t, "the wait took a held file")
				case <-time.After(100 * time.Millisecond):
				}
				require.NoError(t, other.Write(record.Mark{At: at}))
				require.NoError(t, other.Release())
			}
			var got result
			select {
			case got = <-done:
			case <-time.After(5 * time.Second):
				require.FailNow(t, "the wait never took the free file")
			}
			t.Cleanup(func() { _ = got.held.Release() })

			assert.NoError(t, got.err)
			assert.Equal(t, tc.want, got.held.Mark)
		})
	}
}
