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

// A stand in nodloop that prints the plugin version the launcher passed
func envBinary(t *testing.T, dir string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	path := filepath.Join(dir, "nodloop")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\necho \"$NODLOOP_PLUGIN_VERSION\"\n"), 0o755))
	return path
}

// The cached binary and the PATH binary both learn the version the plugin expects
func TestLauncherPluginVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the launcher is a POSIX shell script")
	}
	tcs := []struct {
		name string
		// Whether the plugin version is cached so the launcher runs it instead of the PATH binary
		args bool
	}{
		{"the cached binary", true},
		{"the PATH binary", false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			script, tools := launcher(t)
			home := t.TempDir()
			if tc.args {
				envBinary(t, filepath.Join(home, ".nodloop", "bin", "v9.9.9"))
			}
			path := filepath.Dir(envBinary(t, filepath.Join(t.TempDir(), "path")))
			cmd := exec.Command(script, "version")
			cmd.Env = []string{"HOME=" + home, "PATH=" + path + string(os.PathListSeparator) + tools}
			var stderr strings.Builder
			cmd.Stderr = &stderr

			out, err := cmd.Output()

			require.NoError(t, err, stderr.String())
			assert.Equal(t, "9.9.9\n", string(out))
		})
	}
}

// A stand in nodloop of the plugin version that prints its name and arguments
func fakeBinary(t *testing.T, dir, name string) string {
	t.Helper()
	return versionedBinary(t, dir, name, "9.9.9")
}

// A stand in nodloop that answers version with the given one and prints its name and arguments for any other command
func versionedBinary(t *testing.T, dir, name, version string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	path := filepath.Join(dir, "nodloop")
	script := "#!/bin/sh\nif [ \"$1\" = version ]; then echo " + version + "; exit 0; fi\necho " + name + " \"$@\"\n"
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	return path
}

// A PATH binary of another version runs only when the user allows it, so an old build never serves the plugin unnoticed
func TestLauncherPathVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the launcher is a POSIX shell script")
	}
	type args struct {
		version string
		allow   bool
	}
	type want struct {
		// Empty means the launcher refused
		stdout string
		stderr string
		linked bool
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"the plugin version runs", args{version: "9.9.9"}, want{stdout: "path check\n", linked: true}},
		{"the plugin version with a leading v runs", args{version: "v9.9.9"}, want{stdout: "path check\n", linked: true}},
		{"another version is refused", args{version: "dev"}, want{stderr: "is version dev but the plugin needs 9.9.9"}},
		{
			"another version runs when allowed",
			args{version: "dev", allow: true},
			want{stdout: "path check\n", stderr: "running a PATH build of version dev", linked: true},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			script, tools := launcher(t)
			home := t.TempDir()
			bin := versionedBinary(t, filepath.Join(t.TempDir(), "path"), "path", tc.args.version)
			cmd := exec.Command(script, "check")
			cmd.Env = []string{"HOME=" + home, "PATH=" + filepath.Dir(bin) + string(os.PathListSeparator) + tools}
			if tc.args.allow {
				cmd.Env = append(cmd.Env, "NODLOOP_ALLOW_PATH=1")
			}
			var stderr strings.Builder
			cmd.Stderr = &stderr

			out, err := cmd.Output()

			assert.Equal(t, tc.want.stdout == "", err != nil, stderr.String())
			assert.Equal(t, tc.want.stdout, string(out))
			assert.Contains(t, stderr.String(), tc.want.stderr)
			_, linkErr := os.Lstat(filepath.Join(home, ".nodloop", "bin", "nodloop"))
			assert.Equal(t, tc.want.linked, linkErr == nil)
		})
	}
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
		{"a PATH binary is linked from the stable path", args{"{path}", "", false}, want{"path check\n", "{path}"}},
		{"a PATH binary replaces the link to another binary", args{"{path}", "{old}", false}, want{"path check\n", "{path}"}},
		{"a stable link found on PATH keeps its target", args{"{stable}", "{old}", false}, want{"old check\n", "{old}"}},
		{"a cached binary takes the link back from a PATH binary", args{"{path}", "{path}", true}, want{"cached check\n", "{cached}"}},
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
			cmd := exec.Command(script, "check")
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
