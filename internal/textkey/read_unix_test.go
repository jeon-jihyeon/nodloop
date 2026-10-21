//go:build unix

package textkey_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/textkey"
)

func TestLoadFIFO(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, syscall.Mkfifo(filepath.Join(dir, textkey.FileName), 0o600))
	done := make(chan error, 1)
	go func() {
		_, err := textkey.Load(dir)
		done <- err
	}()
	select {
	case err := <-done:
		assert.ErrorIs(t, err, textkey.ErrRead)
	case <-time.After(5 * time.Second):
		t.Fatal("loading a FIFO waited for a writer")
	}
}

func TestLoadReplacementKeepsModeAndBytesTogether(t *testing.T) {
	tcs := []struct {
		name string
		fifo bool
		want error
	}{
		{"a FIFO replacement is refused", true, textkey.ErrRead},
		{"a shared replacement is refused", false, textkey.ErrShared},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, textkey.FileName)
			require.NoError(t, os.WriteFile(path, bytes.Repeat([]byte{7}, 32), 0o600))
			key, err := textkey.Load(dir)
			require.NoError(t, err)
			want := key.Sum("refund window")
			done := make(chan error, 1)
			go func() {
				for i := range 200 {
					next := filepath.Join(dir, "next")
					var err error
					switch {
					case i%2 == 0:
						err = os.WriteFile(next, bytes.Repeat([]byte{7}, 32), 0o600)
					case tc.fifo:
						err = syscall.Mkfifo(next, 0o600)
					default:
						err = os.WriteFile(next, bytes.Repeat([]byte{8}, 32), 0o644)
					}
					if err == nil {
						err = os.Rename(next, path)
					}
					if err != nil {
						done <- err
						return
					}
				}
				done <- nil
			}()
			for range 200 {
				key, err := textkey.Load(dir)
				if err != nil {
					require.True(t, errors.Is(err, tc.want), err)
					continue
				}
				assert.Equal(t, want, key.Sum("refund window"), "a read never combines one file's mode with another's key")
			}
			require.NoError(t, <-done)
		})
	}
}
