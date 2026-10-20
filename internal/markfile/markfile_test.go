package markfile_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/markfile"
	"github.com/jeon-jihyeon/nodloop/internal/record"
)

var at = time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)

// Hold and Read see the same mark in the file
func TestHoldAndRead(t *testing.T) {
	tcs := []struct {
		name string
		// The files of the directory before the call by name
		args map[string]string
		want record.Mark
	}{
		{"no file reads as the zero mark", nil, record.Mark{}},
		{
			"a mark reads its times",
			map[string]string{"mark.json": `{"at":"2026-10-07T09:00:00Z","cutoff":"2026-10-07T09:00:00Z"}`},
			record.Mark{At: at, Cutoff: at},
		},
		{"a mark that does not decode reads as the zero mark", map[string]string{"mark.json": "{"}, record.Mark{}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			for name, content := range tc.args {
				require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600))
			}
			path := filepath.Join(dir, "mark.json")

			read, readErr := markfile.Read[record.Mark](path)
			held, holdErr := markfile.Hold(path)
			require.NoError(t, holdErr)
			t.Cleanup(func() { _ = held.Release() })

			assert.NoError(t, readErr)
			assert.Equal(t, tc.want, read)
			assert.Equal(t, tc.want, held.Mark)
		})
	}
}

// A written mark replaces the whole file and a second hold while one is held gets none
func TestHeldWrite(t *testing.T) {
	tcs := []struct {
		name string
		// The mark written over one that held both times
		args record.Mark
		want string
	}{
		{"a mark without a cutoff drops the old cutoff", record.Mark{At: at.Add(time.Hour)}, `{"at":"2026-10-07T10:00:00Z"}` + "\n"},
		{
			"a mark with both times writes both",
			record.Mark{At: at.Add(time.Hour), Cutoff: at},
			`{"at":"2026-10-07T10:00:00Z","cutoff":"2026-10-07T09:00:00Z"}` + "\n",
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "mark.json")
			require.NoError(t, os.WriteFile(path, []byte(`{"at":"2026-10-07T09:00:00Z","cutoff":"2026-10-07T09:00:00Z"}`), 0o600))
			held, err := markfile.Hold(path)
			require.NoError(t, err)

			writeErr := held.Write(tc.args)
			second, err := markfile.Hold(path)
			require.NoError(t, err)
			require.NoError(t, held.Release())
			b, err := os.ReadFile(path)
			require.NoError(t, err)

			assert.NoError(t, writeErr)
			assert.Nil(t, second, "another process holds the file")
			assert.Equal(t, tc.want, string(b))
		})
	}
}

// A path that cannot be opened fails to hold and a directory also fails to read while a missing file reads as the zero mark
func TestMarkfileFails(t *testing.T) {
	type want struct {
		holdErr bool
		readErr bool
	}
	tcs := []struct {
		name string
		// The mark path relative to a temp directory that holds a directory named mark.json
		args string
		want want
	}{
		{"a directory in place of the mark opens for neither read nor write", "mark.json", want{true, true}},
		{"a mark under a missing directory fails to hold and reads as the zero mark", "missing/mark.json", want{true, false}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			require.NoError(t, os.Mkdir(filepath.Join(dir, "mark.json"), 0o755))
			path := filepath.Join(dir, tc.args)

			held, holdErr := markfile.Hold(path)
			read, readErr := markfile.Read[record.Mark](path)

			assert.Nil(t, held)
			assert.Equal(t, tc.want.holdErr, holdErr != nil, holdErr)
			assert.Equal(t, tc.want.readErr, readErr != nil, readErr)
			assert.Equal(t, record.Mark{}, read)
		})
	}
}

// A released file fails to write and to release with os.ErrClosed
func TestHeldReleased(t *testing.T) {
	type want struct {
		writeErr   error
		releaseErr error
	}
	tcs := []struct {
		name string
		// How often the file is released before the write
		args int
		want want
	}{
		{"a held file writes and releases", 0, want{nil, nil}},
		{"a released file fails to write and to release", 1, want{os.ErrClosed, os.ErrClosed}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			held, err := markfile.Hold(filepath.Join(t.TempDir(), "mark.json"))
			require.NoError(t, err)
			for range tc.args {
				require.NoError(t, held.Release())
			}

			writeErr, releaseErr := held.Write(record.Mark{At: at}), held.Release()

			assert.ErrorIs(t, writeErr, tc.want.writeErr)
			assert.ErrorIs(t, releaseErr, tc.want.releaseErr)
		})
	}
}
