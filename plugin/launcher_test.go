package plugin_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tools the launcher runs before it reaches the PATH binary
// curl and go stay out so no run reaches the network or builds
var launcherTools = []string{"awk", "cat", "chmod", "cut", "dirname", "head", "ln", "mkdir", "mktemp", "mv", "rm", "sed", "tar", "tr", "uname"}

// A copy of the launcher whose plugin version no release carries
// The directory of each tool the launcher needs is linked into one tools directory
func launcher(t *testing.T) (script, tools string) {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "bin"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".claude-plugin"), 0o755))
	b, err := os.ReadFile(filepath.Join("bin", "nodloop"))
	require.NoError(t, err)
	script = filepath.Join(root, "bin", "nodloop")
	require.NoError(t, os.WriteFile(script, b, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".claude-plugin", "plugin.json"), []byte("{\n  \"version\": \"9.9.9\"\n}\n"), 0o600))
	tools = t.TempDir()
	for _, name := range launcherTools {
		path, err := exec.LookPath(name)
		require.NoError(t, err)
		require.NoError(t, os.Symlink(path, filepath.Join(tools, name)))
	}
	return script, tools
}

// A stand in nodloop that prints its name and arguments
func fakeBinary(t *testing.T, dir, name string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	path := filepath.Join(dir, "nodloop")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\necho "+name+" \"$@\"\n"), 0o755))
	return path
}

func TestLauncherStableLink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the launcher is a POSIX shell script")
	}
	type args struct {
		// PATH entries before the tools
		// `{stable}` is the directory of the stable link and `{path}` holds the PATH binary
		path string
		// The binary the stable link names before the run
		// `{old}` is another binary and empty means no link
		linked string
		// A cached binary of the plugin version
		cached bool
	}
	type want struct {
		stdout string
		// The binary the stable link names after the run
		// `{path}` the PATH binary and `{old}` the other binary and `{cached}` the cached one
		linked string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a PATH binary is linked from the stable path", args{"{path}", "", false}, want{"path version\n", "{path}"}},
		{"a PATH binary replaces the link to another binary", args{"{path}", "{old}", false}, want{"path version\n", "{path}"}},
		{"a stable link found on PATH keeps its target", args{"{stable}", "{old}", false}, want{"old version\n", "{old}"}},
		{"a cached binary takes the link back from a PATH binary", args{"{path}", "{path}", true}, want{"cached version\n", "{cached}"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			script, tools := launcher(t)
			home, dir := t.TempDir(), t.TempDir()
			stableDir := filepath.Join(home, ".nodloop", "bin")
			bins := map[string]string{
				"{path}":   fakeBinary(t, filepath.Join(dir, "path"), "path"),
				"{old}":    fakeBinary(t, filepath.Join(dir, "old"), "old"),
				"{stable}": stableDir,
			}
			if tc.args.cached {
				bins["{cached}"] = fakeBinary(t, filepath.Join(stableDir, "v9.9.9"), "cached")
			}
			require.NoError(t, os.MkdirAll(stableDir, 0o755))
			stable := filepath.Join(stableDir, "nodloop")
			if tc.args.linked != "" {
				require.NoError(t, os.Symlink(bins[tc.args.linked], stable))
			}
			pathDir := map[string]string{"{path}": filepath.Dir(bins["{path}"]), "{stable}": stableDir}[tc.args.path]
			cmd := exec.Command(script, "version")
			cmd.Env = []string{"HOME=" + home, "PATH=" + pathDir + string(os.PathListSeparator) + tools}
			var stderr strings.Builder
			cmd.Stderr = &stderr

			out, err := cmd.Output()

			require.NoError(t, err, stderr.String())
			assert.Equal(t, tc.want.stdout, string(out))
			target, err := os.Readlink(stable)
			require.NoError(t, err)
			assert.Equal(t, bins[tc.want.linked], target)
		})
	}
}
