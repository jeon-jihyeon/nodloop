package consolidate_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/consolidate"
)

var markedAt = time.Date(2026, 10, 12, 6, 0, 0, 0, time.UTC)

func TestMarkerClaim(t *testing.T) {
	type args struct {
		// consolidate.json before the claim and none when empty
		mark  string
		at    time.Time
		every time.Duration
	}
	type want struct {
		claimed bool
		mark    string
	}
	written := `{"at":"2026-10-12T06:00:00Z"}`
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"no mark claims", args{"", markedAt, consolidate.Interval}, want{true, written + "\n"}},
		{"a mark of the day before claims", args{`{"at":"2026-10-11T06:00:00Z"}`, markedAt, consolidate.Interval}, want{true, written + "\n"}},
		{"a mark of an hour ago claims nothing", args{written, markedAt.Add(time.Hour), consolidate.Interval}, want{false, written}},
		{"a mark from the future reads as elapsed", args{`{"at":"2026-10-20T06:00:00Z"}`, markedAt, consolidate.Interval}, want{true, written + "\n"}},
		{"a mark that does not decode reads as none", args{"{", markedAt, consolidate.Interval}, want{true, written + "\n"}},
		{"a manual run claims a fresh mark", args{written, markedAt.Add(time.Hour), 0}, want{true, `{"at":"2026-10-12T07:00:00Z"}` + "\n"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := markDir(t, tc.args.mark)

			claimed, err := consolidate.NewMarker(dir).Claim(tc.args.at, tc.args.every)

			b, readErr := os.ReadFile(filepath.Join(dir, consolidate.MarkFile))
			require.NoError(t, readErr)
			assert.NoError(t, err)
			assert.Equal(t, tc.want.claimed, claimed)
			assert.Equal(t, tc.want.mark, string(b))
		})
	}
}

func TestMarkerDue(t *testing.T) {
	tcs := []struct {
		name string
		args string
		want bool
	}{
		{"no mark is due", "", true},
		{"a mark of an hour ago is not due", `{"at":"2026-10-12T05:00:00Z"}`, false},
		{"a mark a day old is due", `{"at":"2026-10-11T06:00:00Z"}`, true},
		{"a mark from the future is due", `{"at":"2026-10-13T06:00:00Z"}`, true},
		{"a torn mark is due", `{"at":`, true},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := consolidate.NewMarker(markDir(t, tc.args)).Due(markedAt)

			assert.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// A mark that cannot be opened or read fails with ErrMark
func TestMarkerFails(t *testing.T) {
	type want struct {
		ok  bool
		err error
	}
	tcs := []struct {
		name string
		// The call on a marker whose mark is a directory
		args func(m consolidate.Marker) (bool, error)
		want want
	}{
		{
			"a claim fails to open the mark",
			func(m consolidate.Marker) (bool, error) { return m.Claim(markedAt, consolidate.Interval) },
			want{false, consolidate.ErrMark},
		},
		{
			"a due check fails to read the mark",
			func(m consolidate.Marker) (bool, error) { return m.Due(markedAt) },
			want{false, consolidate.ErrMark},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			// A directory in place of the mark opens for neither read nor write
			require.NoError(t, os.Mkdir(filepath.Join(dir, consolidate.MarkFile), 0o755))

			ok, err := tc.args(consolidate.NewMarker(dir))

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.ok, ok)
		})
	}
}

// A record directory with the mark written in it and none when the mark is empty
func markDir(t *testing.T, mark string) string {
	t.Helper()
	dir := t.TempDir()
	if mark != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, consolidate.MarkFile), []byte(mark), 0o600))
	}
	return dir
}
