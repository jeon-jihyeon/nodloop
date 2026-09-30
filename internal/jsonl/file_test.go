package jsonl_test

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/jsonl"
)

type row struct {
	ID string `json:"id"`
	N  int    `json:"n"`
}

func TestOpen(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "plain"), nil, 0o600))
	tcs := []struct {
		name string
		args string
		want error
	}{
		{"missing directory fails", "missing", os.ErrNotExist},
		{"plain file in place of the directory fails", "plain", jsonl.ErrNotDirectory},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := jsonl.Open[row](filepath.Join(root, tc.args), "rows.jsonl")
			assert.ErrorIs(t, err, tc.want)
			assert.Equal(t, jsonl.File[row]{}, got)
		})
	}
}

func TestFileAppend(t *testing.T) {
	type args struct {
		// Directory the empty file is created under before any append
		// rows.jsonl leaves a directory in place of the file
		under string
		perm  os.FileMode
		rows  []row
	}
	type want struct {
		rows []row
		err  error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"nothing appended reads as empty", args{perm: 0o600}, want{}},
		{
			"rows read back in append order",
			args{perm: 0o600, rows: []row{{"a", 0}, {"b", 1}, {"c", 2}}},
			want{rows: []row{{"a", 0}, {"b", 1}, {"c", 2}}},
		},
		{
			"directory in place of the file fails append and read",
			args{under: "rows.jsonl", perm: 0o600, rows: []row{{"a", 0}}},
			want{err: syscall.EISDIR},
		},
		{"write only file fails append and read", args{perm: 0o200, rows: []row{{"a", 0}}}, want{err: fs.ErrPermission}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(dir, tc.args.under), 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(dir, tc.args.under, "rows.jsonl"), nil, tc.args.perm))
			f, err := jsonl.Open[row](dir, "rows.jsonl")
			require.NoError(t, err)
			for _, r := range tc.args.rows {
				assert.ErrorIs(t, f.Append(r), tc.want.err)
			}
			got, err := f.All()
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.rows, got)
		})
	}
}

func TestFileAppendMendsTheTail(t *testing.T) {
	type args struct {
		// File content before the append
		content string
		row     row
	}
	tcs := []struct {
		name string
		args args
		want string
	}{
		{
			"terminated file gets the record on the next line",
			args{"{\"id\":\"a\",\"n\":0}\n", row{"b", 1}},
			"{\"id\":\"a\",\"n\":0}\n{\"id\":\"b\",\"n\":1}\n",
		},
		{
			"torn tail is cut before the record",
			args{"{\"id\":\"a\",\"n\":0}\n{\"id\":\"to", row{"b", 1}},
			"{\"id\":\"a\",\"n\":0}\n{\"id\":\"b\",\"n\":1}\n",
		},
		{
			"tail missing only its newline keeps its record",
			args{"{\"id\":\"a\",\"n\":0}", row{"b", 1}},
			"{\"id\":\"a\",\"n\":0}\n{\"id\":\"b\",\"n\":1}\n",
		},
		{"torn first line is cut", args{"{\"id", row{"b", 1}}, "{\"id\":\"b\",\"n\":1}\n"},
		{
			"one record after a byte order mark without a newline is kept",
			args{"\uFEFF{\"id\":\"a\",\"n\":0}", row{"b", 1}},
			"\uFEFF{\"id\":\"a\",\"n\":0}\n{\"id\":\"b\",\"n\":1}\n",
		},
		{
			"tail that is JSON but no record is cut like a torn one",
			args{"{\"id\":\"a\",\"n\":0}\n[1]", row{"b", 1}},
			"{\"id\":\"a\",\"n\":0}\n{\"id\":\"b\",\"n\":1}\n",
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "rows.jsonl")
			require.NoError(t, os.WriteFile(path, []byte(tc.args.content), 0o600))
			f, err := jsonl.Open[row](dir, "rows.jsonl")
			require.NoError(t, err)
			assert.NoError(t, f.Append(tc.args.row))
			got, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, tc.want, string(got))
		})
	}
}

func TestFileAppendConcurrentWritersKeepEveryRecord(t *testing.T) {
	f, err := jsonl.Open[row](t.TempDir(), "rows.jsonl")
	require.NoError(t, err)
	// Enough writers to collide on the flock while the test stays fast
	const writers = 16
	var want []row
	var wg sync.WaitGroup
	for i := range writers {
		want = append(want, row{ID: "w", N: i})
		wg.Go(func() { assert.NoError(t, f.Append(row{ID: "w", N: i})) })
	}
	wg.Wait()
	got, err := f.All()
	assert.NoError(t, err)
	assert.ElementsMatch(t, want, got)
}

func TestFileAppendIfUnchanged(t *testing.T) {
	type args struct {
		content  string
		expected []row
	}
	type want struct {
		rows []row
		err  error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"nil snapshot", args{"", nil}, want{[]row{{"b", 1}}, nil}},
		{"empty snapshot", args{"", []row{}}, want{[]row{{"b", 1}}, nil}},
		{"matching snapshot", args{"{\"id\":\"a\"}\n", []row{{"a", 0}}}, want{[]row{{"a", 0}, {"b", 1}}, nil}},
		{"changed snapshot", args{"{\"id\":\"a\"}\n", nil}, want{[]row{{"a", 0}}, jsonl.ErrChanged}},
		{"torn tail", args{"{\"id\":\"a\"}\n{", []row{{"a", 0}}}, want{[]row{{"a", 0}, {"b", 1}}, nil}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, "rows.jsonl"), []byte(tc.args.content), 0o600))
			f, err := jsonl.Open[row](dir, "rows.jsonl")
			require.NoError(t, err)
			assert.ErrorIs(t, f.AppendIfUnchanged(row{"b", 1}, tc.args.expected), tc.want.err)
			got, err := f.All()
			assert.NoError(t, err)
			assert.Equal(t, tc.want.rows, got)
		})
	}
}

func TestFileAppendFailsOnUnencodableValue(t *testing.T) {
	f, err := jsonl.Open[any](t.TempDir(), "rows.jsonl")
	require.NoError(t, err)
	var unsupported *json.UnsupportedTypeError
	assert.ErrorAs(t, f.Append(make(chan int)), &unsupported)
	got, err := f.All()
	assert.NoError(t, err)
	assert.Nil(t, got)
}

func TestFileAppendCreatesOwnerOnlyFile(t *testing.T) {
	dir := t.TempDir()
	f, err := jsonl.Open[row](dir, "rows.jsonl")
	require.NoError(t, err)
	require.NoError(t, f.Append(row{ID: "a"}))
	info, err := os.Stat(filepath.Join(dir, "rows.jsonl"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestFileAll(t *testing.T) {
	tcs := []struct {
		name string
		args string
		want []row
	}{
		{"empty file reads as empty", "", nil},
		{
			"blank and space only lines are skipped",
			"{\"id\":\"a\"}\n\n  \n{\"id\":\"b\",\"n\":1}\n",
			[]row{{"a", 0}, {"b", 1}},
		},
		{"torn last line is skipped", "{\"id\":\"a\"}\n{\"id\":\"b\",\"n", []row{{"a", 0}}},
		{"last line that is JSON but no record is skipped", "{\"id\":\"a\"}\n[1]", []row{{"a", 0}}},
		{"last line missing only its newline is read", "{\"id\":\"a\"}\n{\"id\":\"b\",\"n\":1}", []row{{"a", 0}, {"b", 1}}},
		{"a byte order mark before the first record is dropped", "\uFEFF{\"id\":\"a\"}\n{\"id\":\"b\"}\n", []row{{"a", 0}, {"b", 0}}},
		{"one record after a byte order mark without a newline is read", "\uFEFF{\"id\":\"a\"}", []row{{"a", 0}}},
		{
			"line longer than any scanner buffer is read",
			"{\"id\":\"" + strings.Repeat("x", 17<<20) + "\"}\n",
			[]row{{strings.Repeat("x", 17<<20), 0}},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, "rows.jsonl"), []byte(tc.args), 0o600))
			f, err := jsonl.Open[row](dir, "rows.jsonl")
			require.NoError(t, err)
			got, err := f.All()
			assert.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// The file was never written in both rows
// A directory renamed after Open must not read as an empty store
func TestFileAllWithoutFile(t *testing.T) {
	type want struct {
		rows []row
		err  error
	}
	tcs := []struct {
		name string
		// Directory renamed after Open
		// spare leaves the records directory in place
		args string
		want want
	}{
		{"missing file in an existing directory reads as empty", "spare", want{}},
		{"directory moved after open fails", "records", want{err: os.ErrNotExist}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			require.NoError(t, os.Mkdir(filepath.Join(root, "records"), 0o700))
			require.NoError(t, os.Mkdir(filepath.Join(root, "spare"), 0o700))
			f, err := jsonl.Open[row](filepath.Join(root, "records"), "rows.jsonl")
			require.NoError(t, err)
			require.NoError(t, os.Rename(filepath.Join(root, tc.args), filepath.Join(root, "moved")))
			got, err := f.All()
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.rows, got)
		})
	}
}

func TestFileAllNamesTheBrokenLine(t *testing.T) {
	type args struct {
		content string
		check   func(row) error
	}
	refuseB := func(r row) error {
		return map[string]error{"b": assert.AnError}[r.ID]
	}
	tcs := []struct {
		name string
		args args
		// A fragment of the message naming the file and line and the cause
		want string
	}{
		{"a line that is not JSON is named", args{"{\"id\":\"a\"}\n\nnot json\n", refuseB}, "rows.jsonl line 3: invalid"},
		{
			"a record the check refuses is named",
			args{"{\"id\":\"a\"}\n\n{\"id\":\"b\"}\n", refuseB},
			"rows.jsonl line 3: " + assert.AnError.Error(),
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, "rows.jsonl"), []byte(tc.args.content), 0o600))
			f, err := jsonl.Open[row](dir, "rows.jsonl")
			require.NoError(t, err)
			got, err := f.All(tc.args.check)
			assert.ErrorContains(t, err, tc.want)
			assert.Nil(t, got)
		})
	}
}

func TestFileNewest(t *testing.T) {
	f, err := jsonl.Open[row](t.TempDir(), "rows.jsonl")
	require.NoError(t, err)
	for _, r := range []row{{"a", 0}, {"b", 1}, {"c", 2}} {
		require.NoError(t, f.Append(r))
	}
	type args struct {
		keep  func(row) bool
		limit int
	}
	tcs := []struct {
		name string
		args args
		want []row
	}{
		{
			"zero limit returns every row newest first",
			args{func(row) bool { return true }, 0},
			[]row{{"c", 2}, {"b", 1}, {"a", 0}},
		},
		{"rows failing keep are skipped", args{func(r row) bool { return r.N%2 == 0 }, 0}, []row{{"c", 2}, {"a", 0}}},
		{"limit stops at the newest matches", args{func(row) bool { return true }, 2}, []row{{"c", 2}, {"b", 1}}},
		{"no match returns nothing", args{func(row) bool { return false }, 0}, nil},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := f.Newest(tc.args.keep, tc.args.limit)
			assert.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestFileNewestFailsOnUnreadableFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "rows.jsonl"), 0o700))
	f, err := jsonl.Open[row](dir, "rows.jsonl")
	require.NoError(t, err)
	got, err := f.Newest(func(row) bool { return true }, 0)
	assert.ErrorIs(t, err, syscall.EISDIR)
	assert.Nil(t, got)
}
