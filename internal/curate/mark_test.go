package curate_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/curate"
)

// The hooks read the mark without its lock and draw nothing from a mark no pass wrote
func TestReadMark(t *testing.T) {
	type args struct {
		// The content of curate.json and none when empty
		content string
		// curate.json is a directory that cannot be read as a file
		unreadable bool
	}
	type want struct {
		mark curate.Mark
		err  error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"no mark reads as the zero mark", args{}, want{}},
		{
			"a mark keeps its knobs",
			args{content: `{"knobs":{"activated":true,"item_hold":0.1}}`},
			want{mark: curate.Mark{Knobs: curate.Knobs{Activated: true, ItemHold: 0.1}}},
		},
		{"a torn mark reads as the zero mark", args{content: `{"knobs":{"activ`}, want{}},
		{"a mark that cannot be read fails", args{unreadable: true}, want{err: curate.ErrMark}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, curate.MarkFile)
			if tc.args.content != "" {
				require.NoError(t, os.WriteFile(path, []byte(tc.args.content), 0o600))
			}
			if tc.args.unreadable {
				require.NoError(t, os.Mkdir(path, 0o755))
			}

			got, err := curate.ReadMark(dir)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.mark, got)
		})
	}
}

// One pass holds the mark while a second gets none and the mark it writes is what the hooks read next
func TestMarkerHold(t *testing.T) {
	left := curate.Mark{
		CycleID: "2026-09-21", PolicyVersion: "0.10.0+1a2b3c4d", Seed: "5eed", Knobs: curate.StartKnobs(),
		Due: []curate.Due{{Kind: curate.QuestionShadow, Decide: "d1", Change: curate.ChangeAdd, Ref: ref("k-a"), Run: "r-1", At: written}},
	}
	tcs := []struct {
		name string
		// The content of curate.json before the hold
		args string
		// The mark the hold reads
		want curate.Mark
	}{
		{"no mark holds the zero mark and writes the new one", "", curate.Mark{}},
		{"a torn mark holds the zero mark", `{"knobs":`, curate.Mark{}},
		{"a mark holds what the last pass left", `{"at":"2026-09-20T09:00:00Z","knobs":{"activated":true}}`, curate.Mark{At: written, Knobs: curate.Knobs{Activated: true}}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			if tc.args != "" {
				require.NoError(t, os.WriteFile(filepath.Join(dir, curate.MarkFile), []byte(tc.args), 0o600))
			}
			held, err := curate.NewMarker(dir).Hold()
			require.NoError(t, err)
			require.NotNil(t, held)
			before := held.Mark

			second, secondErr := curate.NewMarker(dir).Hold()
			writeErr := held.Write(left)
			releaseErr := held.Release()
			read, readErr := curate.ReadMark(dir)

			assert.NoError(t, secondErr)
			assert.Nil(t, second, "a second pass gets no mark while one holds it")
			assert.Equal(t, tc.want, before)
			assert.NoError(t, writeErr)
			assert.NoError(t, releaseErr)
			assert.NoError(t, readErr)
			assert.Equal(t, left, read)
			assert.Equal(t, left, held.Mark)
		})
	}
}

// A mark that cannot be opened fails to hold and a released one fails to write and to release
func TestMarkerFails(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, curate.MarkFile), 0o755))
	released, err := curate.NewMarker(t.TempDir()).Hold()
	require.NoError(t, err)
	require.NoError(t, released.Release())

	held, holdErr := curate.NewMarker(dir).Hold()
	writeErr, releaseErr := released.Write(curate.Mark{}), released.Release()

	assert.Nil(t, held)
	assert.ErrorIs(t, holdErr, curate.ErrMark)
	assert.ErrorIs(t, writeErr, curate.ErrMark)
	assert.ErrorIs(t, releaseErr, curate.ErrMark)
	assert.Equal(t, curate.Mark{}, released.Mark)
}

// The mark keeps the time of the last daily pass and a day after it the next one is due
func TestMarkDaily(t *testing.T) {
	at := time.Date(2026, 9, 22, 9, 0, 0, 0, time.UTC)
	tcs := []struct {
		name string
		args curate.Mark
		want bool
	}{
		{"no daily pass yet", curate.Mark{}, true},
		{"a pass an hour ago", curate.Mark{At: at.Add(-time.Hour)}, false},
		{"a pass a day ago", curate.Mark{At: at.Add(-24 * time.Hour)}, true},
		{"a pass in the future of a clock set back", curate.Mark{At: at.Add(time.Hour)}, true},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Daily(at))
		})
	}
}
