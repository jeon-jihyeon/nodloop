package plugin_test

import (
	"bytes"
	"encoding/json"
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
// Writes an executable through cp so no fork of a parallel test holds a write descriptor of it
// Linux refuses to run a file that some process holds open for writing
func writeExecutable(t *testing.T, path string, text []byte) {
	t.Helper()
	src := path + ".src"
	require.NoError(t, os.WriteFile(src, text, 0o600))
	require.NoError(t, exec.Command("cp", src, path).Run())
	require.NoError(t, os.Chmod(path, 0o755))
}

func launcher(t *testing.T) (script, tools string) {
	t.Helper()
	return launcherWith(t, map[string]string{".claude-plugin": "9.9.9"})
}

// A copy of the launcher beside a plugin.json of the version in each manifest directory
// The directory of each tool the launcher needs is linked into one tools directory
func launcherWith(t *testing.T, manifests map[string]string) (script, tools string) {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "bin"), 0o755))
	for dir, version := range manifests {
		require.NoError(t, os.MkdirAll(filepath.Join(root, dir), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, dir, "plugin.json"), []byte("{\n  \"version\": \""+version+"\"\n}\n"), 0o600))
	}
	b, err := os.ReadFile(filepath.Join("bin", "nodloop"))
	require.NoError(t, err)
	script = filepath.Join(root, "bin", "nodloop")
	writeExecutable(t, script, b)
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
	writeExecutable(t, path, []byte("#!/bin/sh\necho \"$NODLOOP_PLUGIN_VERSION\"\n"))
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

// Claude Code and Codex start the launcher from the same directory and each finds the version of a manifest
func TestLauncherManifest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the launcher is a POSIX shell script")
	}
	type want struct {
		// The version the cached binary learns
		stdout  string
		stderr  string
		refused bool
	}
	tcs := []struct {
		name string
		// The version in plugin.json of each manifest directory
		args map[string]string
		want want
	}{
		{"the Claude Code manifest alone runs its version", map[string]string{".claude-plugin": "9.9.9"}, want{stdout: "9.9.9\n"}},
		{"the Codex manifest alone runs its version", map[string]string{".codex-plugin": "9.9.8"}, want{stdout: "9.9.8\n"}},
		{"the Claude Code manifest wins over the Codex one", map[string]string{".claude-plugin": "9.9.9", ".codex-plugin": "9.9.8"}, want{stdout: "9.9.9\n"}},
		{"no manifest is refused", map[string]string{}, want{stderr: "nodloop: no version in", refused: true}},
		{"a manifest without a version is refused", map[string]string{".codex-plugin": ""}, want{stderr: ".codex-plugin/plugin.json", refused: true}},
		{
			"a version that leaves the bin directory is refused", map[string]string{".claude-plugin": "x/../../../planted"},
			want{stderr: "is not a release version", refused: true},
		},
		{"a version with a dot segment is refused", map[string]string{".claude-plugin": "9.9..9"}, want{stderr: "is not a release version", refused: true}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			script, tools := launcherWith(t, tc.args)
			home := t.TempDir()
			for _, version := range tc.args {
				envBinary(t, filepath.Join(home, ".nodloop", "bin", "v"+version))
			}
			cmd := exec.Command(script, "version")
			cmd.Env = []string{"HOME=" + home, "PATH=" + tools}
			var stderr strings.Builder
			cmd.Stderr = &stderr

			out, err := cmd.Output()

			assert.Equal(t, tc.want.refused, err != nil, stderr.String())
			assert.Equal(t, tc.want.stdout, string(out))
			assert.Contains(t, stderr.String(), tc.want.stderr)
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
	writeExecutable(t, path, []byte(script))
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

// The plugin registers the conversation hooks through the launcher so they run the binary of the plugin version
// 1. every hook runs synchronously with no async flag so a stop records its run before the next prompt reads it
// The async flags and the timeout decode so a hook that sets one fails the comparison
// 2. a subagent start adds the items since no prompt hook runs inside a subagent
// 3. Codex reads the same events with its host named and no instructions hook since it never runs one
// Codex moves none of the context nodloop adds into a file since contextRunes already bounds it
// 4. a rules file Claude Code loads is recorded with no matcher so every load reason reaches the anchor
func TestHooksJSON(t *testing.T) {
	type hook struct {
		Type         string `json:"type"`
		Command      string `json:"command"`
		Async        bool   `json:"async"`
		AsyncRewake  bool   `json:"asyncRewake"`
		Timeout      int    `json:"timeout"`
		ContextLimit *int   `json:"additionalContextLimit"`
	}
	// The hooks of one matcher group of an event
	type group struct {
		Hooks []hook `json:"hooks"`
	}
	launcher := `"${CLAUDE_PLUGIN_ROOT}/bin/nodloop" hook `
	spill := 0
	tcs := []struct {
		name string
		// The hook file under hooks
		args string
		want map[string][]group
	}{
		{"the Claude Code hooks run start, prompt, stop, subagent and instructions", "hooks.json", map[string][]group{
			"SessionStart":       {{[]hook{{Type: "command", Command: launcher + "start"}}}},
			"UserPromptSubmit":   {{[]hook{{Type: "command", Command: launcher + "prompt"}}}},
			"Stop":               {{[]hook{{Type: "command", Command: launcher + "stop"}}}},
			"SubagentStart":      {{[]hook{{Type: "command", Command: launcher + "subagent"}}}},
			"InstructionsLoaded": {{[]hook{{Type: "command", Command: launcher + "instructions"}}}},
		}},
		{"the Codex hooks name the host", "codex.json", map[string][]group{
			"SessionStart":     {{[]hook{{Type: "command", Command: launcher + "start --host codex", ContextLimit: &spill}}}},
			"UserPromptSubmit": {{[]hook{{Type: "command", Command: launcher + "prompt --host codex", ContextLimit: &spill}}}},
			"Stop":             {{[]hook{{Type: "command", Command: launcher + "stop --host codex"}}}},
			"SubagentStart":    {{[]hook{{Type: "command", Command: launcher + "subagent --host codex", ContextLimit: &spill}}}},
		}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b, err := os.ReadFile(filepath.Join("hooks", tc.args))
			require.NoError(t, err)
			var got struct {
				Hooks map[string][]group `json:"hooks"`
			}

			require.NoError(t, json.Unmarshal(b, &got))

			assert.Equal(t, tc.want, got.Hooks)
		})
	}
}

// Both manifests carry one version so the launcher runs the same binary under either host
// The Codex manifest names its own hook file so Codex runs every hook with its host named
// Codex expands no variable in an MCP command so its server runs the launcher by a path below the plugin root
func TestManifests(t *testing.T) {
	type manifest struct {
		Name       string          `json:"name"`
		Version    string          `json:"version"`
		Skills     string          `json:"skills"`
		Hooks      string          `json:"hooks"`
		MCPServers json.RawMessage `json:"mcpServers"`
	}
	b, err := os.ReadFile(filepath.Join(".claude-plugin", "plugin.json"))
	require.NoError(t, err)
	var claude manifest
	require.NoError(t, json.Unmarshal(b, &claude))
	require.NotEmpty(t, claude.Version)
	type want struct {
		skills string
		hooks  string
		// The mcpServers key in compact JSON and empty when the host reads its default file
		servers string
		// Files below the plugin root the host loads
		files []string
	}
	tcs := []struct {
		name string
		// The manifest directory
		args string
		want want
	}{
		{
			"the Claude Code manifest leaves the skills and the hooks and the servers to their default places",
			".claude-plugin",
			want{files: []string{"skills", "hooks/hooks.json", ".mcp.json", "bin/nodloop"}},
		},
		{
			"the Codex manifest names its own hook file and runs the launcher below the plugin root",
			".codex-plugin",
			want{
				skills: "./skills/", hooks: "./hooks/codex.json",
				servers: `{"nodloop":{"command":"./bin/nodloop","args":["mcp","--agent"],"cwd":"."}}`,
				files:   []string{"./skills/", "./hooks/codex.json", "bin/nodloop"},
			},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b, err := os.ReadFile(filepath.Join(tc.args, "plugin.json"))
			require.NoError(t, err)
			var got manifest
			require.NoError(t, json.Unmarshal(b, &got))

			assert.Equal(t, claude.Version, got.Version)
			assert.Equal(t, claude.Name, got.Name)
			assert.Equal(t, tc.want.skills, got.Skills)
			assert.Equal(t, tc.want.hooks, got.Hooks)
			assert.Equal(t, tc.want.servers, compactJSON(t, got.MCPServers))
			assert.Empty(t, missingFiles(tc.want.files))
		})
	}
}

// Claude Code starts the agent server of the plugin, which lists no tool that decides for a person
func TestMCPConfig(t *testing.T) {
	b, err := os.ReadFile(".mcp.json")
	require.NoError(t, err)

	got := compactJSON(t, b)

	assert.Equal(t, `{"mcpServers":{"nodloop":{"command":"${CLAUDE_PLUGIN_ROOT}/bin/nodloop","args":["mcp","--agent"]}}}`, got)
}

// The JSON in compact form and empty for none
func compactJSON(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	if len(raw) == 0 {
		return ""
	}
	var out bytes.Buffer
	require.NoError(t, json.Compact(&out, raw))
	return out.String()
}

// The paths that do not exist
func missingFiles(paths []string) []string {
	var out []string
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			out = append(out, path)
		}
	}
	return out
}
