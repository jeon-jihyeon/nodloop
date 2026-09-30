package atomicfile_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/atomicfile"
)

// A file kept in a dotfiles folder and reached from home through links
func TestReplace(t *testing.T) {
	type args struct {
		// The target in the dotfiles folder before the run
		// Empty means none
		before string
		// Links in home from the path handed to Replace toward the target
		// None hands the target itself
		links []string
		// Each link names the next one by a path relative to its folder
		relative bool
		// Mode of the dotfiles folder during the run
		mode os.FileMode
	}
	type want struct {
		err error
		// The target after the run
		// Empty means none
		after string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a plain file is replaced", args{"old", nil, false, 0o755}, want{nil, "new"}},
		{"a missing file is created", args{"", nil, false, 0o755}, want{nil, "new"}},
		{"a link stays and its target is replaced", args{"old", []string{"config.json"}, false, 0o755}, want{nil, "new"}},
		{"a relative link stays and its target is replaced", args{"old", []string{"config.json"}, true, 0o755}, want{nil, "new"}},
		{"a chain of links stays and the last target is replaced", args{"old", []string{"config.json", "hop"}, false, 0o755}, want{nil, "new"}},
		{"a dangling link fails and creates nothing", args{"", []string{"config.json"}, false, 0o755}, want{atomicfile.ErrLinkDangling, ""}},
		{"a folder that takes no new file fails and keeps the bytes", args{"old", []string{"config.json"}, false, 0o500}, want{os.ErrPermission, "old"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.args.mode != 0o755 && (runtime.GOOS == "windows" || os.Geteuid() == 0) {
				t.Skip("folder permissions do not stop the write here")
			}
			home, dotfiles := t.TempDir(), t.TempDir()
			target := filepath.Join(dotfiles, "config.json")
			if tc.args.before != "" {
				require.NoError(t, os.WriteFile(target, []byte(tc.args.before), 0o644))
			}
			path, next := target, target
			for i := len(tc.args.links) - 1; i >= 0; i-- {
				path = filepath.Join(home, tc.args.links[i])
				dest := next
				if tc.args.relative {
					rel, err := filepath.Rel(home, next)
					require.NoError(t, err)
					dest = rel
				}
				require.NoError(t, os.Symlink(dest, path))
				next = path
			}
			require.NoError(t, os.Chmod(dotfiles, tc.args.mode))
			t.Cleanup(func() { _ = os.Chmod(dotfiles, 0o755) })

			err := atomicfile.Replace(path, []byte("new"))

			assert.ErrorIs(t, err, tc.want.err)
			got, _ := os.ReadFile(target)
			assert.Equal(t, tc.want.after, string(got))
			for _, name := range tc.args.links {
				info, err := os.Lstat(filepath.Join(home, name))
				require.NoError(t, err)
				assert.Equal(t, os.ModeSymlink, info.Mode().Type(), "the link stays a link")
			}
			for _, dir := range []string{home, dotfiles} {
				temps, err := filepath.Glob(filepath.Join(dir, ".config.json-*"))
				require.NoError(t, err)
				assert.Empty(t, temps)
			}
			if tc.want.err == nil {
				info, err := os.Stat(target)
				require.NoError(t, err)
				assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
			}
		})
	}
}
