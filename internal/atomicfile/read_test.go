package atomicfile_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/atomicfile"
)

// The path a case reads in a folder that holds a file of 5 bytes, a link to it, a file its owner may not read and a folder
func readPath(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "file"), []byte("hello"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "locked"), []byte("hello"), 0o200))
	require.NoError(t, os.Symlink(filepath.Join(dir, "file"), filepath.Join(dir, "link")))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "folder"), 0o700))
	if name == os.DevNull {
		return name
	}
	return filepath.Join(dir, name)
}

func TestRead(t *testing.T) {
	type args struct {
		name  string
		limit int64
	}
	type want struct {
		content string
		err     error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a regular file within the limit is read whole", args{"file", 5}, want{"hello", nil}},
		{"a link to a regular file is followed", args{"link", 5}, want{"hello", nil}},
		{"a file past the limit is refused", args{"file", 4}, want{"", atomicfile.ErrTooLarge}},
		{"a folder is refused", args{"folder", 5}, want{"", atomicfile.ErrNotRegular}},
		{"a device is refused before it is opened", args{os.DevNull, 5}, want{"", atomicfile.ErrNotRegular}},
		{"a missing file fails as missing", args{"missing", 5}, want{"", fs.ErrNotExist}},
		{"a file its owner may not read fails to open", args{"locked", 5}, want{"", fs.ErrPermission}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := readPath(t, tc.args.name)

			got, err := atomicfile.Read(path, tc.args.limit)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.content, string(got))
		})
	}
}

func TestCheck(t *testing.T) {
	type args struct {
		name  string
		limit int64
	}
	tcs := []struct {
		name string
		args args
		want error
	}{
		{"a regular file within the limit passes", args{"file", 5}, nil},
		{"a link to a regular file is followed", args{"link", 5}, nil},
		{"a file past the limit is refused", args{"file", 4}, atomicfile.ErrTooLarge},
		{"a folder is refused", args{"folder", 5}, atomicfile.ErrNotRegular},
		{"a device is refused", args{os.DevNull, 5}, atomicfile.ErrNotRegular},
		{"a missing file fails as missing", args{"missing", 5}, fs.ErrNotExist},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := readPath(t, tc.args.name)

			err := atomicfile.Check(path, tc.args.limit)

			assert.ErrorIs(t, err, tc.want)
		})
	}
}
