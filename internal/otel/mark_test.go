package otel_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/otel"
)

func TestMarkerHold(t *testing.T) {
	at := time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)
	tcs := []struct {
		name string
		// The files of the record directory before the hold by name
		args map[string]string
		want otel.Mark
	}{
		{"no mark reads as no export", nil, otel.Mark{}},
		{"a mark of the time alone reads its time", map[string]string{otel.MarkFile: `{"at":"2026-10-07T09:00:00Z"}`}, otel.Mark{At: at}},
		{"a mark keeps terminal receipt fingerprints",
			map[string]string{otel.MarkFile: `{"at":"2026-10-07T09:00:00Z","completed":["fingerprint"]}`},
			otel.Mark{At: at, Completed: []string{"fingerprint"}}},
		{"a mark reads the cursor of each store", map[string]string{otel.MarkFile: `{"at":"2026-10-07T09:00:00Z","after":{"traces":"t1"}}`},
			otel.Mark{At: at, After: map[string]string{"traces": "t1"}}},
		{"a mark that does not decode reads as no export", map[string]string{otel.MarkFile: "{"}, otel.Mark{}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			for name, content := range tc.args {
				require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600))
			}

			held, err := otel.NewMarker(dir).Hold()

			require.NoError(t, err)
			t.Cleanup(func() { _ = held.Release() })
			assert.Equal(t, tc.want, held.Mark)
		})
	}
}

// A moved mark is written in UTC and is what the next hold reads and a second hold while one is held gets none
func TestMarkerMove(t *testing.T) {
	type want struct {
		at   time.Time
		file string
	}
	tcs := []struct {
		name string
		// Where the held mark moves to
		args otel.Mark
		want want
	}{
		{
			"a time in another zone is written in UTC",
			otel.Mark{At: time.Date(2026, 10, 7, 9, 0, 0, 0, time.FixedZone("x", 3600))},
			want{time.Date(2026, 10, 7, 8, 0, 0, 0, time.UTC), `{"at":"2026-10-07T08:00:00Z"}` + "\n"},
		},
		{
			"a UTC time is written as it is with the cursor of each store",
			otel.Mark{At: time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC), After: map[string]string{"traces": "t1", "verdicts": "v1"}},
			want{time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC), `{"at":"2026-10-07T09:00:00Z","after":{"traces":"t1","verdicts":"v1"}}` + "\n"},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			marker := otel.NewMarker(dir)
			held, err := marker.Hold()
			require.NoError(t, err)

			require.NoError(t, held.Move(tc.args))
			second, err := marker.Hold()
			require.NoError(t, err)
			require.NoError(t, held.Release())
			again, err := marker.Hold()
			require.NoError(t, err)
			t.Cleanup(func() { _ = again.Release() })
			b, err := os.ReadFile(filepath.Join(dir, otel.MarkFile))
			require.NoError(t, err)

			assert.Nil(t, second, "another export holds the mark")
			assert.True(t, tc.args.At.Equal(held.At), "a moved mark keeps the time it moved to")
			assert.Equal(t, tc.args.After, again.After)
			assert.True(t, tc.want.at.Equal(again.At), again.At)
			assert.Equal(t, tc.want.file, string(b))
		})
	}
}

// A mark that cannot be opened fails with ErrMark
func TestMarkerHoldFails(t *testing.T) {
	tcs := []struct {
		name string
		// The record directory relative to a temp directory and the mark inside it made a directory
		args string
	}{
		{"a directory in place of the mark opens for neither read nor write", "."},
		{"a record directory that does not exist has no mark to create", "missing"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			require.NoError(t, os.Mkdir(filepath.Join(root, otel.MarkFile), 0o755))

			held, err := otel.NewMarker(filepath.Join(root, tc.args)).Hold()

			assert.ErrorIs(t, err, otel.ErrMark)
			assert.Nil(t, held)
		})
	}
}

// A held mark moves and releases once and a released one fails both with ErrMark and keeps the time it read
func TestHeldReleased(t *testing.T) {
	at := time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)
	type want struct {
		moveErr    error
		releaseErr error
		at         time.Time
	}
	tcs := []struct {
		name string
		// How often the mark is released before the move
		args int
		want want
	}{
		{"a held mark moves and releases", 0, want{nil, nil, at}},
		{"a released mark fails to move and to release", 1, want{otel.ErrMark, otel.ErrMark, time.Time{}}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			held, err := otel.NewMarker(t.TempDir()).Hold()
			require.NoError(t, err)
			for range tc.args {
				require.NoError(t, held.Release())
			}

			moveErr, releaseErr := held.Move(otel.Mark{At: at}), held.Release()

			assert.ErrorIs(t, moveErr, tc.want.moveErr)
			assert.ErrorIs(t, releaseErr, tc.want.releaseErr)
			assert.True(t, tc.want.at.Equal(held.At), held.At)
		})
	}
}

func TestMarkerKeepsReceiptAfterFailedMove(t *testing.T) {
	for _, failure := range []string{"marshal", "write"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			if failure == "write" && (runtime.GOOS == "windows" || os.Getuid() == 0) {
				t.Skip("directory permission failures need an unprivileged Unix process")
			}
			dir := t.TempDir()
			marker := otel.NewMarker(dir)
			held, err := marker.Hold()
			require.NoError(t, err)
			before := otel.Mark{At: at, Completed: []string{"receipt"}}
			require.NoError(t, held.Move(before))
			next := otel.Mark{At: at.Add(time.Hour)}
			if failure == "marshal" {
				next.At = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
			} else {
				require.NoError(t, os.Chmod(dir, 0o500))
				t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
			}
			assert.ErrorIs(t, held.Move(next), otel.ErrMark)
			require.NoError(t, held.Release())
			again, err := marker.Hold()
			require.NoError(t, err)
			t.Cleanup(func() { _ = again.Release() })
			assert.Equal(t, before, again.Mark)
		})
	}
}
