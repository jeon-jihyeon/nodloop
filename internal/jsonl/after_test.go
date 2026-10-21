package jsonl_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/jsonl"
)

func ids(rows []row) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}

func every(row) bool { return true }

// A read goes on from the position the read before answered whatever a rewrite or a slow writer did in between
func TestFileAfter(t *testing.T) {
	type args struct {
		// The file before the first read
		content string
		// Of the first read and zero for every record
		limit int
		// The ids a rewrite removes after the first read
		drop []string
		// The bytes written after the first read
		appended string
	}
	type want struct {
		first, second []string
		// Of the first and of the second read
		firstErr, err error
	}
	a, b, c := `{"id":"a"}`+"\n", `{"id":"b"}`+"\n", `{"id":"c"}`+"\n"
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a read from the start takes every line and the next only what was appended", args{a + b, 0, nil, c},
			want{first: []string{"a", "b"}, second: []string{"c"}}},
		{"a limit ends the read after its records and the next goes on", args{a + b + c, 2, nil, ""},
			want{first: []string{"a", "b"}, second: []string{"c"}}},
		{"nothing appended reads nothing", args{a + b, 0, nil, ""}, want{first: []string{"a", "b"}}},
		{"a rewrite that removed an earlier line resumes after the line of the position", args{a + b + c, 2, []string{"a"}, `{"id":"d"}` + "\n"},
			want{first: []string{"a", "b"}, second: []string{"c", "d"}}},
		{"a rewrite that removed the line of the position fails", args{a + b, 0, []string{"b"}, ""},
			want{first: []string{"a", "b"}, err: jsonl.ErrPositionGone}},
		{"a torn tail waits for the rest of its write", args{a + `{"id":"b"`, 0, nil, `,"n":1}` + "\n"},
			want{first: []string{"a"}, second: []string{"b"}}},
		{"a tail that lost only its newline is read once", args{a + `{"id":"b"}`, 0, nil, "\n" + c},
			want{first: []string{"a", "b"}, second: []string{"c"}}},
		{"a corrupt line is left out with the error and passed", args{a + "not json\n" + b, 0, nil, c},
			want{first: []string{"a", "b"}, second: []string{"c"}, firstErr: jsonl.ErrCorrupt}},
		{"a byte order mark before the first line is left out", args{"\uFEFF" + a + b, 1, []string{"b"}, c},
			want{first: []string{"a"}, second: []string{"c"}}},
		{"blank lines hold nothing", args{a + "\n  \n" + b, 0, nil, "\n\n"}, want{first: []string{"a", "b"}}},
		{"an empty file reads nothing and the next read starts at the first line", args{"", 0, nil, a}, want{second: []string{"a"}}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "rows.jsonl")
			require.NoError(t, os.WriteFile(path, []byte(tc.args.content), 0o600))
			f, err := jsonl.Open[row](dir, "rows.jsonl")
			require.NoError(t, err)

			first, at, firstErr := f.After("", every, tc.args.limit)
			_, err = f.Remove(func() (func(row) bool, error) {
				return func(r row) bool { return slices.Contains(tc.args.drop, r.ID) }, nil
			})
			require.NoError(t, err)
			out, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
			require.NoError(t, err)
			_, err = out.WriteString(tc.args.appended)
			require.NoError(t, err)
			require.NoError(t, out.Close())
			second, _, err := f.After(at, every, 0)

			assert.Equal(t, tc.want.first, ids(first))
			assert.ErrorIs(t, firstErr, tc.want.firstErr)
			assert.Equal(t, tc.want.second, ids(second))
			assert.ErrorIs(t, err, tc.want.err)
		})
	}
}

// A read keeps only what keep holds for and still moves past every line it read
// So a reader of one kind never reads the lines of the others again
func TestFileAfterKeep(t *testing.T) {
	dir := t.TempDir()
	content := `{"id":"a","n":1}` + "\n" + `{"id":"b"}` + "\n" + `{"id":"c","n":1}` + "\n" + `{"id":"d"}` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "rows.jsonl"), []byte(content), 0o600))
	f, err := jsonl.Open[row](dir, "rows.jsonl")
	require.NoError(t, err)
	ones := func(r row) bool { return r.N == 1 }

	first, at, err := f.After("", ones, 1)
	require.NoError(t, err)
	second, end, err := f.After(at, ones, 0)
	require.NoError(t, err)
	third, again, err := f.After(end, every, 0)
	require.NoError(t, err)

	assert.Equal(t, []string{"a"}, ids(first))
	assert.Equal(t, []string{"c"}, ids(second))
	assert.Empty(t, third, "the read of the ones read past d")
	assert.Equal(t, end, again, "a read of nothing answers its position")
}

// A position no read answered and one of a file removed since fail with ErrPositionGone
// The start of a file not written yet reads nothing
func TestFileAfterRefuses(t *testing.T) {
	tcs := []struct {
		name string
		args string
		want error
	}{
		{"the start of a file not written yet reads nothing", "", nil},
		{"a position of a file removed since", "12:af", jsonl.ErrPositionGone},
		{"a position without its hash", "12", jsonl.ErrPositionGone},
		{"a position of no offset", "0:af", jsonl.ErrPositionGone},
		{"a position of no number", "x:af", jsonl.ErrPositionGone},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f, err := jsonl.Open[row](t.TempDir(), "rows.jsonl")
			require.NoError(t, err)

			got, at, err := f.After(tc.args, every, 0)

			assert.ErrorIs(t, err, tc.want)
			assert.Empty(t, got)
			assert.Equal(t, tc.args, at, "a failed read answers the position it was given")
		})
	}
}

// A file that cannot be read fails a read from the start and one from a position alike
func TestFileAfterFailsOnUnreadableFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rows.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(`{"id":"a"}`+"\n"), 0o600))
	f, err := jsonl.Open[row](dir, "rows.jsonl")
	require.NoError(t, err)
	_, at, err := f.After("", every, 0)
	require.NoError(t, err)
	require.NoError(t, os.Chmod(path, 0o200))
	_, _, denied := f.After("", every, 0)
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.Mkdir(path, 0o700))

	_, _, startErr := f.After("", every, 0)
	_, _, onErr := f.After(at, every, 0)

	assert.ErrorIs(t, denied, fs.ErrPermission)
	assert.ErrorIs(t, startErr, syscall.EISDIR)
	assert.ErrorIs(t, onErr, syscall.EISDIR)
}

func TestFileAfterRemovedDirectory(t *testing.T) {
	tcs := []struct {
		name string
		args bool
	}{
		{"from the start", false},
		{"after a recorded position", true},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := filepath.Join(t.TempDir(), "records")
			require.NoError(t, os.Mkdir(dir, 0o700))
			f, err := jsonl.Open[row](dir, "rows.jsonl")
			require.NoError(t, err)
			require.NoError(t, f.Append(row{ID: "example"}))
			_, position, err := f.After("", every, 0)
			require.NoError(t, err)
			from := ""
			if tc.args {
				from = position
			}
			require.NoError(t, os.RemoveAll(dir))

			rows, next, err := f.After(from, every, 0)

			assert.ErrorIs(t, err, fs.ErrNotExist)
			assert.NotErrorIs(t, err, jsonl.ErrPositionGone)
			assert.Empty(t, rows)
			assert.Equal(t, from, next)
		})
	}
}
