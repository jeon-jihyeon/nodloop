package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/veto"
	vetofile "github.com/jeon-jihyeon/nodloop/internal/veto/file"
)

func TestRunGuard(t *testing.T) {
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	valid, err := os.ReadFile("testdata/valid.yaml")
	require.NoError(t, err)
	sed, err := os.ReadFile("testdata/bash_sed.json")
	require.NoError(t, err)
	plain, err := os.ReadFile("testdata/bash_plain.json")
	require.NoError(t, err)
	const (
		installed = `{"hooks":{"PreToolUse":[{"matcher":"*",` +
			`"hooks":[{"type":"command","command":"{exe} guard","timeout":5}]}]}}`
		written = "{\n  \"hooks\": {\n    \"PreToolUse\": [\n      {\n        \"hooks\": [\n          {\n" +
			"            \"command\": \"{hooked} guard\",\n            \"timeout\": 5,\n            \"type\": \"command\"\n" +
			"          }\n        ],\n        \"matcher\": \"*\"\n      }\n    ]\n  }\n}\n"
		stale = `{"hooks":{"PreToolUse":[{"matcher":"Bash",` +
			`"hooks":[{"type":"command","command":"{home}/.nodloop/bin/v0.4.1/nodloop guard","timeout":5}]}]}}`
	)
	type args struct {
		args  []string
		stdin []byte
		// Empty for an unknown home
		home string
		// Content of settings.json before the run
		settings string
		// The running binary with `{home}` for the temp home
		// Empty means `{exe}` which is a file under the temp home
		exe string
		// Whether the stable link under the home points at the running binary
		stable bool
		// Content of the user veto file
		// Empty writes the valid fixture
		vetoes string
		// Other veto files keyed by a path under `{home}` or `{shared}`
		// `{shared}` is a directory outside home
		files map[string]string
		// Directories created under `{home}` or `{shared}`
		dirs []string
	}
	type want struct {
		code   int
		stdout string
		// Regexp matched against stderr
		stderr string
		// Text settings.json holds after the run
		settings string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"hook passes a plain call", args{stdin: plain, home: "{home}", settings: "{}"}, want{0, "", `^$`, "{}"}},
		{
			"hook blocks through the user vetoes",
			args{stdin: sed, home: "{home}", settings: "{}"},
			want{2, "", `^nodloop guard: Bash call blocked by veto no-sed-inplace\n`, "{}"},
		},
		{
			"hook blocks through the vetoes flag",
			args{args: []string{"--vetoes", "testdata/valid.yaml"}, stdin: sed, settings: "{}"},
			want{2, "", `^nodloop guard: Bash call blocked by veto no-sed-inplace\n`, "{}"},
		},
		{
			"hook fails on a missing veto file",
			args{args: []string{"--vetoes", "nope.yaml"}, stdin: sed, home: "{home}", settings: "{}"},
			want{
				1, "",
				`^nodloop guard: failed to load vetoes, skipped: nope.yaml: failed to read veto file: open nope.yaml: no such file`,
				"{}",
			},
		},
		{
			"hook fails on a veto file whose only entry is broken",
			args{args: []string{"--vetoes", "testdata/invalid_regex.yaml"}, stdin: sed, home: "{home}", settings: "{}"},
			want{1, "", `^nodloop guard: failed to load vetoes, skipped: testdata/invalid_regex.yaml: vetoes\[0\] \(bad-regex\)`, "{}"},
		},
		{
			"hook blocks through the valid entries of a file with a broken entry",
			args{args: []string{"--vetoes", "testdata/partial.yaml"}, stdin: sed, home: "{home}", settings: "{}"},
			want{
				2, "",
				`^nodloop guard: failed to load vetoes, skipped: testdata/partial.yaml: vetoes\[1\] \(bad-regex\).*\n` +
					`nodloop guard: Bash call blocked by veto no-sed-inplace\n`,
				"{}",
			},
		},
		{
			"hook blocks a plain call while the user veto file is not yaml",
			args{stdin: plain, home: "{home}", settings: "{}", vetoes: "vetoes: ["},
			want{
				2, "",
				`^nodloop guard: failed to load vetoes, skipped: .*/vetoes\.yaml: failed to read veto file: failed to parse yaml: .*\n` +
					`nodloop guard: Bash call blocked because a veto file is not valid YAML\n`,
				"{}",
			},
		},
		{
			"hook blocks a plain call while the user veto file has an unknown top level key",
			args{stdin: plain, home: "{home}", settings: "{}", vetoes: "veto: []"},
			want{2, "", `unknown key: line 1: "veto"\nnodloop guard: Bash call blocked because a veto file is not valid YAML\n`, "{}"},
		},
		{
			"hook lets the repair of a broken user veto file through",
			args{
				stdin: []byte(`{"tool_name":"Edit","tool_input":{"file_path":"{home}/{rel}"}}`),
				home:  "{home}", settings: "{}", vetoes: "vetoes: [",
			},
			want{1, "", `^nodloop guard: failed to load vetoes, skipped: [^\n]*\n$`, "{}"},
		},
		{
			"hook lets the repair of a broken veto file named by the vetoes flag through",
			args{
				args:  []string{"--vetoes", "{home}/dotfiles/team-vetoes.yaml"},
				stdin: []byte(`{"tool_name":"Read","tool_input":{"file_path":"{home}/dotfiles/team-vetoes.yaml"}}`),
				home:  "{home}", settings: "{}", files: map[string]string{"{home}/dotfiles/team-vetoes.yaml": "vetoes: ["},
			},
			want{1, "", `^nodloop guard: failed to load vetoes, skipped: [^\n]*team-vetoes\.yaml[^\n]*\n$`, "{}"},
		},
		{
			"hook blocks a plain call while the veto file named by the vetoes flag is not yaml",
			args{
				args:  []string{"--vetoes", "{home}/dotfiles/team-vetoes.yaml"},
				stdin: plain, home: "{home}", settings: "{}", files: map[string]string{"{home}/dotfiles/team-vetoes.yaml": "vetoes: ["},
			},
			want{2, "", `\nnodloop guard: Bash call blocked because a veto file is not valid YAML\n`, "{}"},
		},
		{
			"hook blocks through the vetoes of an enclosing project below a nested veto file",
			args{
				stdin: []byte(`{"tool_name":"Bash","tool_input":{"command":"sed -i x f"},"cwd":"{home}/mono/web/src"}`),
				home:  "{home}", settings: "{}", vetoes: "vetoes: []",
				files: map[string]string{
					"{home}/mono/{rel}": string(valid),
					"{home}/mono/web/{rel}": "vetoes:\n  - id: no-npm-publish\n    tool: Bash\n" +
						"    when: [{field: command, match: npm publish}]\n    reason: no publish\n",
				},
			},
			want{2, "", `^nodloop guard: Bash call blocked by veto no-sed-inplace\n`, "{}"},
		},
		{
			"hook runs a plain call while the user veto file has an extra top level key",
			args{stdin: plain, home: "{home}", settings: "{}", vetoes: "version: 1\n" + string(valid)},
			want{1, "", `^nodloop guard: failed to load vetoes, skipped: .*unknown key: line 1: "version"\n$`, "{}"},
		},
		{
			"hook blocks through the vetoes of a user file with an extra top level key",
			args{stdin: sed, home: "{home}", settings: "{}", vetoes: "version: 1\n" + string(valid)},
			want{2, "", `\nnodloop guard: Bash call blocked by veto no-sed-inplace\n`, "{}"},
		},
		{
			"hook reads no veto file above a cwd outside home without .git",
			args{
				stdin: []byte(`{"tool_name":"Bash","tool_input":{"command":"ls"},"cwd":"{shared}/someone/scratch"}`),
				home:  "{home}", settings: "{}", files: map[string]string{"{shared}/{rel}": "vetoes: ["},
			},
			want{0, "", `^$`, "{}"},
		},
		{
			"hook runs a plain call while a project file outside home is not yaml",
			args{
				stdin: []byte(`{"tool_name":"Bash","tool_input":{"command":"ls"},"cwd":"{shared}/me"}`),
				home:  "{home}", settings: "{}", files: map[string]string{"{shared}/{rel}": "vetoes: ["}, dirs: []string{"{shared}/.git"},
			},
			want{1, "", `^nodloop guard: failed to load vetoes, skipped: outside home so it does not block every call: [^\n]*\n$`, "{}"},
		},
		{
			"hook runs a plain call while a directory sits at a project veto path",
			args{
				stdin: []byte(`{"tool_name":"Bash","tool_input":{"command":"ls"},"cwd":"{home}/repo"}`),
				home:  "{home}", settings: "{}", dirs: []string{"{home}/repo/.git", "{home}/repo/{rel}"},
			},
			want{1, "", `^nodloop guard: failed to load vetoes, skipped: [^\n]*is a directory\n$`, "{}"},
		},
		{
			"hook blocks a plain call below a repository root under home whose file is not yaml",
			args{
				stdin: []byte(`{"tool_name":"Bash","tool_input":{"command":"ls"},"cwd":"{home}/repo/pkg"}`),
				home:  "{home}", settings: "{}", files: map[string]string{"{home}/repo/{rel}": "vetoes: ["}, dirs: []string{"{home}/repo/.git"},
			},
			want{2, "", `\nnodloop guard: Bash call blocked because a veto file is not valid YAML\n`, "{}"},
		},
		{
			"hook passes a plain call beside an approved file of a relative record directory",
			args{
				stdin: plain, home: "{home}", settings: "{}",
				files: map[string]string{"{home}/.claude/nodloop/vetoes.approved.cb41b38387d6.yaml": "# Generated by nodloop from " +
					"the approved judgment knowledge of rec\nvetoes: [{id: k, tool: Bash, when: [{field: command, match: push}], reason: r}]\n"},
			},
			want{0, "", `^$`, "{}"},
		},
		{
			"hook fails on an unknown flag",
			args{args: []string{"--nope"}, home: "{home}", settings: "{}"},
			want{1, "", `^flag provided but not defined: -nope\n`, "{}"},
		},
		{
			"install registers the absolute executable",
			args{args: []string{"install"}, home: "{home}", settings: "{}"},
			want{0, "installed PreToolUse hook for {exe} in {path}\n", `^$`, strings.ReplaceAll(written, "{hooked}", "{exe}")},
		},
		{
			"install registers the stable link that points at the running binary",
			args{args: []string{"install"}, home: "{home}", settings: "{}", stable: true},
			want{0, "installed PreToolUse hook for {link} in {path}\n", `^$`, strings.ReplaceAll(written, "{hooked}", "{link}")},
		},
		{
			"install replaces a hook of another version on Bash alone",
			args{args: []string{"install"}, home: "{home}", settings: stale},
			want{0, "installed PreToolUse hook for {exe} in {path}\n", `^$`, strings.ReplaceAll(written, "{hooked}", "{exe}")},
		},
		{
			"install moves a stale versioned hook to the stable link",
			args{args: []string{"install"}, home: "{home}", settings: strings.ReplaceAll(installed, "{exe}", "{home}/.nodloop/bin/v0.4.1/nodloop"), stable: true},
			want{0, "installed PreToolUse hook for {link} in {path}\n", `^$`, strings.ReplaceAll(written, "{hooked}", "{link}")},
		},
		{
			"install after the move changes nothing",
			args{args: []string{"install"}, home: "{home}", settings: strings.ReplaceAll(installed, "{exe}", "{link}"), stable: true},
			want{0, "already installed in {path}\n", `^$`, strings.ReplaceAll(installed, "{exe}", "{link}")},
		},
		{
			"install refuses a go run build",
			args{args: []string{"install"}, home: "{home}", settings: "{}", exe: "{home}/go-build123/b001/exe/nodloop"},
			want{1, "", `^nodloop guard install: the executable is a temporary go build\. .*/go-build123/b001/exe/nodloop\n$`, "{}"},
		},
		{
			"install twice changes nothing",
			args{args: []string{"install"}, home: "{home}", settings: installed},
			want{0, "already installed in {path}\n", `^$`, installed},
		},
		{
			"uninstall removes the hook",
			args{args: []string{"uninstall"}, home: "{home}", settings: installed},
			want{0, "removed PreToolUse hook from {path}\n", `^$`, "{}\n"},
		},
		{
			"uninstall without the hook changes nothing",
			args{args: []string{"uninstall"}, home: "{home}", settings: "{}"},
			want{0, "not installed in {path}\n", `^$`, "{}"},
		},
		{
			"install fails on broken settings",
			args{args: []string{"install"}, home: "{home}", settings: "{broken"},
			want{1, "", `^nodloop guard install: .*settings.json`, "{broken"},
		},
		{
			"uninstall fails on broken settings",
			args{args: []string{"uninstall"}, home: "{home}", settings: "{broken"},
			want{1, "", `^nodloop guard uninstall: .*settings.json`, "{broken"},
		},
		{
			"install with an unknown home fails",
			args{args: []string{"install"}, settings: "{}"},
			want{1, "", "^nodloop guard install: home directory unknown: cannot locate settings.json\n$", "{}"},
		},
		{
			"uninstall with an unknown home fails",
			args{args: []string{"uninstall"}, settings: "{}"},
			want{1, "", "^nodloop guard uninstall: home directory unknown: cannot locate settings.json\n$", "{}"},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, shared := t.TempDir(), t.TempDir()
			path := filepath.Join(home, ".claude", "settings.json")
			exe := filepath.Join(home, "opt", "nodloop")
			link := filepath.Join(home, ".nodloop", "bin", "nodloop")
			r := strings.NewReplacer(
				"{home}", home, "{shared}", shared, "{path}", path, "{exe}", exe, "{link}", link, "{rel}", vetofile.RelPath,
			)
			vetoes := filepath.Join(home, vetofile.RelPath)
			require.NoError(t, os.MkdirAll(filepath.Dir(vetoes), 0o755))
			content := valid
			if tc.args.vetoes != "" {
				content = []byte(tc.args.vetoes)
			}
			require.NoError(t, os.WriteFile(vetoes, content, 0o644))
			for name, content := range tc.args.files {
				path := r.Replace(name)
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
				require.NoError(t, os.WriteFile(path, []byte(r.Replace(content)), 0o644))
			}
			for _, dir := range tc.args.dirs {
				require.NoError(t, os.MkdirAll(r.Replace(dir), 0o755))
			}
			args := make([]string, len(tc.args.args))
			for i, arg := range tc.args.args {
				args[i] = r.Replace(arg)
			}
			require.NoError(t, os.WriteFile(path, []byte(r.Replace(tc.args.settings)), 0o600))
			require.NoError(t, os.MkdirAll(filepath.Dir(exe), 0o755))
			require.NoError(t, os.WriteFile(exe, nil, 0o700))
			if tc.args.stable {
				require.NoError(t, os.MkdirAll(filepath.Dir(link), 0o755))
				require.NoError(t, os.Symlink(exe, link))
			}
			running := r.Replace(tc.args.exe)
			if running == "" {
				running = exe
			}
			executable := func() (string, error) { return running, nil }
			getenv := func(k string) string { return map[string]string{"HOME": r.Replace(tc.args.home)}[k] }
			var stdout, stderr bytes.Buffer

			stdin := strings.NewReader(r.Replace(string(tc.args.stdin)))
			got := runGuard(args, getenv, executable, func() time.Time { return at }, stdin, &stdout, &stderr)

			assert.Equal(t, tc.want.code, got)
			assert.Equal(t, r.Replace(tc.want.stdout), stdout.String())
			assert.Regexp(t, tc.want.stderr, stderr.String())
			settings, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, r.Replace(tc.want.settings), string(settings))
		})
	}
}

// The hook logs every block and ask under home and guard log reads them back newest first
func TestRunGuardDecisionLog(t *testing.T) {
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	const vetoes = "vetoes:\n" +
		"  - {id: no-sed, tool: Bash, when: [{field: command, match: 'sed -i'}], reason: use Edit}\n" +
		"  - {id: no-rm, tool: Bash, action: ask, when: [{field: command, match: 'rm -rf'}], reason: deletes files}\n"
	type call struct {
		args    []string
		command string
	}
	type want struct {
		// Exit code of each call
		codes  []int
		stdout string
		// Regexp matched against stderr
		stderr string
	}
	sed, rm, ls := call{command: "sed -i s/a/b/ f"}, call{command: "rm -rf build"}, call{command: "ls"}
	ask := `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"ask",` +
		`"permissionDecisionReason":"nodloop veto no-rm: deletes files"}}` + "\n"
	tcs := []struct {
		name string
		args []call
		// Whether a file stands where the nodloop directory of home goes
		blocked bool
		want    want
	}{
		{
			"a block and an ask log one line each and a pass logs none",
			[]call{sed, rm, ls, {args: []string{"log"}}}, false,
			want{
				[]int{2, 0, 0, 0},
				ask + "2026-10-01T09:00:00Z\task\tno-rm\tBash\t{cwd}\n2026-10-01T09:00:00Z\tblock\tno-sed\tBash\t{cwd}\n",
				`^nodloop guard: Bash call blocked by veto no-sed\nuse Edit\n$`,
			},
		},
		{
			"the limit keeps the newest decisions",
			[]call{sed, rm, {args: []string{"log", "--limit", "1"}}}, false,
			want{[]int{2, 0, 0}, ask + "2026-10-01T09:00:00Z\task\tno-rm\tBash\t{cwd}\n", `^nodloop guard: Bash call blocked`},
		},
		{"no decision yet prints nothing", []call{{args: []string{"log"}}}, false, want{[]int{0}, "", `^$`}},
		{
			"a log that cannot be written warns and keeps the block",
			[]call{sed}, true,
			want{[]int{2}, "", `^nodloop guard: Bash call blocked by veto no-sed\nuse Edit\nnodloop guard: decision not logged: `},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, cwd := t.TempDir(), t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude", "nodloop"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(home, vetofile.RelPath), []byte(vetoes), 0o600))
			if tc.blocked {
				require.NoError(t, os.WriteFile(filepath.Join(home, ".nodloop"), nil, 0o600))
			}
			getenv := func(k string) string { return map[string]string{"HOME": home}[k] }
			var stdout, stderr bytes.Buffer
			var codes []int
			for _, c := range tc.args {
				stdin := strings.NewReader(`{"tool_name":"Bash","tool_input":{"command":"` + c.command + `"},"cwd":"` + cwd + `"}`)
				codes = append(codes, runGuard(c.args, getenv, os.Executable, func() time.Time { return at }, stdin, &stdout, &stderr))
			}

			assert.Equal(t, tc.want.codes, codes)
			assert.Equal(t, strings.ReplaceAll(tc.want.stdout, "{cwd}", cwd), stdout.String())
			assert.Regexp(t, tc.want.stderr, stderr.String())
		})
	}
}

func TestGuardCommandCheck(t *testing.T) {
	valid, err := os.ReadFile("testdata/valid.yaml")
	require.NoError(t, err)
	broken, err := os.ReadFile("testdata/invalid_regex.yaml")
	require.NoError(t, err)
	partial, err := os.ReadFile("testdata/partial.yaml")
	require.NoError(t, err)
	const (
		unhooked = "guard hook not installed. Run nodloop guard install to enforce them\n"
		hook     = `{"hooks":{"PreToolUse":[{"matcher":"{matcher}","hooks":[{"type":"command","command":"{exe} guard"}]}]}}`
	)
	type args struct {
		// Veto file content keyed by the `{cwd}` or `{parent}` or `{home}` placeholder
		// `{parent}` is the parent directory of the cwd
		files map[string][]byte
		// Content of settings.json with `{exe}` for an executable file and `{matcher}` for the matcher
		// Empty leaves no settings file
		settings string
		matcher  string
		// The hook executable with `{bin}` for the plugin folder that holds v0.4.1 and v0.5.0 and the stable link to v0.5.0
		// Empty means an executable file under home
		exe string
		// Whether the home is unknown
		homeless bool
		// Approved files under home keyed by the record directory their first line names
		approved map[string]string
	}
	type want struct {
		stdout string
		stderr string
		// Every veto file error is a load error of the veto package
		err error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"no file reports where it looked",
			args{},
			want{"no veto file found (looked for {rel} from {cwd} up to its project root and under {home})\n" + unhooked, "", nil},
		},
		{
			"both files list their counts and the merge",
			args{files: map[string][]byte{"{cwd}": valid, "{home}": valid}},
			want{"{cwd}/{rel}: 2 vetoes\n{home}/{rel}: 2 vetoes\nmerged: 2 vetoes\n" + unhooked, "", nil},
		},
		{
			"project file alone is listed",
			args{files: map[string][]byte{"{cwd}": valid}},
			want{"{cwd}/{rel}: 2 vetoes\nmerged: 2 vetoes\n" + unhooked, "", nil},
		},
		{
			"project file in a parent of the cwd is listed",
			args{files: map[string][]byte{"{parent}": valid}},
			want{"{parent}/{rel}: 2 vetoes\nmerged: 2 vetoes\n" + unhooked, "", nil},
		},
		{
			"project files of the cwd and a parent are both listed nearest first",
			args{files: map[string][]byte{"{cwd}": valid, "{parent}": valid}},
			want{"{cwd}/{rel}: 2 vetoes\n{parent}/{rel}: 2 vetoes\nmerged: 2 vetoes\n" + unhooked, "", nil},
		},
		{
			"user file alone is listed",
			args{files: map[string][]byte{"{home}": valid}},
			want{"{home}/{rel}: 2 vetoes\nmerged: 2 vetoes\n" + unhooked, "", nil},
		},
		{
			"invalid regexp fails after the listing",
			args{files: map[string][]byte{"{cwd}": broken}},
			want{"merged: 0 vetoes\n" + unhooked, "", veto.ErrMatchInvalid},
		},
		{
			"broken project file still lists the user file",
			args{files: map[string][]byte{"{cwd}": broken, "{home}": valid}},
			want{"{home}/{rel}: 2 vetoes\nmerged: 2 vetoes\n" + unhooked, "", veto.ErrMatchInvalid},
		},
		{
			"file with a broken entry lists its valid entries and names a veto that blocks nothing",
			args{files: map[string][]byte{"{home}": partial}},
			want{
				"{home}/{rel}: 2 vetoes\n{home}/{rel}: veto lower-case-tool blocks nothing on [\"bash\"]: " +
					veto.ErrToolUnknown.Error() + "\nmerged: 2 vetoes\n" + unhooked,
				"",
				veto.ErrMatchInvalid,
			},
		},
		{
			"a registered hook is reported installed",
			args{files: map[string][]byte{"{home}": valid}, settings: hook, matcher: "*"},
			want{"{home}/{rel}: 2 vetoes\nmerged: 2 vetoes\nguard hook installed\n", "", nil},
		},
		{
			"a hook left on an older plugin binary is reported stale",
			args{files: map[string][]byte{"{home}": valid}, settings: hook, matcher: "*", exe: "{bin}/v0.4.1/nodloop"},
			want{
				"{home}/{rel}: 2 vetoes\nmerged: 2 vetoes\n" +
					"guard hook stale: hook runs another binary than the stable link: {bin}/v0.4.1/nodloop runs while the stable link runs " +
					"{bin}/nodloop. Run {bin}/nodloop guard install so the hook follows the plugin\n",
				"",
				nil,
			},
		},
		{
			"a hook on the stable link is reported installed",
			args{files: map[string][]byte{"{home}": valid}, settings: hook, matcher: "*", exe: "{bin}/nodloop"},
			want{"{home}/{rel}: 2 vetoes\nmerged: 2 vetoes\nguard hook installed\n", "", nil},
		},
		{
			"a hook on the binary the stable link names is reported installed",
			args{files: map[string][]byte{"{home}": valid}, settings: hook, matcher: "*", exe: "{bin}/v0.5.0/nodloop"},
			want{"{home}/{rel}: 2 vetoes\nmerged: 2 vetoes\nguard hook installed\n", "", nil},
		},
		{
			"a hook on Bash alone is reported broken",
			args{files: map[string][]byte{"{home}": valid}, settings: hook, matcher: "Bash"},
			want{
				"{home}/{rel}: 2 vetoes\nmerged: 2 vetoes\n" +
					"guard hook broken: hook matcher leaves tools out: \"Bash\". Run nodloop guard install to repair it\n",
				"",
				nil,
			},
		},
		{
			"a hook whose executable is gone is reported broken",
			args{
				files:    map[string][]byte{"{home}": valid},
				settings: strings.ReplaceAll(hook, "{exe}", "{home}/gone/nodloop"), matcher: "*",
			},
			want{
				"{home}/{rel}: 2 vetoes\nmerged: 2 vetoes\n" +
					"guard hook broken: hook executable missing: {home}/gone/nodloop. Run nodloop guard install to repair it\n",
				"",
				nil,
			},
		},
		{
			"a registered hook under disableAllHooks is reported off",
			args{
				files:    map[string][]byte{"{home}": valid},
				settings: `{"disableAllHooks":true,` + hook[1:], matcher: "*",
			},
			want{
				"{home}/{rel}: 2 vetoes\nmerged: 2 vetoes\n" +
					"guard hook off: disableAllHooks is true in {home}/.claude/settings.json so no hook runs. " +
					"Remove it to enforce them\n",
				"",
				nil,
			},
		},
		{
			"broken settings leave the hook state unknown",
			args{files: map[string][]byte{"{home}": valid}, settings: "{"},
			want{"{home}/{rel}: 2 vetoes\nmerged: 2 vetoes\nguard hook unknown: settings file: not valid JSON: " +
				"{home}/.claude/settings.json: unexpected end of JSON input\n", "", nil},
		},
		{
			"an unknown home still lists the project file",
			args{files: map[string][]byte{"{cwd}": valid}, homeless: true},
			want{"{cwd}/{rel}: 2 vetoes\nmerged: 2 vetoes\nguard hook unknown: home directory unknown\n", "", nil},
		},
		{
			"an approved file of a relative record directory is named on stderr",
			args{approved: map[string]string{"rec": "cb41b38387d6"}},
			want{
				"{home}/.claude/nodloop/vetoes.approved.cb41b38387d6.yaml: 1 vetoes\nmerged: 1 vetoes\n" + unhooked,
				"nodloop: {home}/.claude/nodloop/vetoes.approved.cb41b38387d6.yaml: record directory \"rec\": " +
					vetofile.ErrOrphan.Error() + "\n",
				nil,
			},
		},
		{
			"an approved file of an absolute record directory is listed alone",
			args{approved: map[string]string{"/records": "e7024ac054e4"}},
			want{"{home}/.claude/nodloop/vetoes.approved.e7024ac054e4.yaml: 1 vetoes\nmerged: 1 vetoes\n" + unhooked, "", nil},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			parent, home := t.TempDir(), t.TempDir()
			cwd := filepath.Join(parent, "sub")
			require.NoError(t, os.Mkdir(cwd, 0o755))
			// The parent is the project root so the walk reads it
			require.NoError(t, os.Mkdir(filepath.Join(parent, ".git"), 0o755))
			bin := filepath.Dir(homeDir(home).stableBinary())
			for _, version := range []string{"v0.4.1", "v0.5.0"} {
				require.NoError(t, os.MkdirAll(filepath.Join(bin, version), 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(bin, version, "nodloop"), nil, 0o700))
			}
			require.NoError(t, os.Symlink(filepath.Join("v0.5.0", "nodloop"), homeDir(home).stableBinary()))
			exe := filepath.Join(home, "nodloop")
			require.NoError(t, os.WriteFile(exe, nil, 0o700))
			if tc.args.exe != "" {
				exe = strings.ReplaceAll(tc.args.exe, "{bin}", bin)
			}
			r := strings.NewReplacer(
				"{cwd}", cwd, "{parent}", parent, "{home}", home, "{rel}", vetofile.RelPath,
				"{exe}", exe, "{matcher}", tc.args.matcher, "{bin}", bin,
			)
			for base, content := range tc.args.files {
				path := filepath.Join(r.Replace(base), vetofile.RelPath)
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
				require.NoError(t, os.WriteFile(path, content, 0o644))
			}
			if tc.args.settings != "" {
				path := homeDir(home).settingsPath()
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
				require.NoError(t, os.WriteFile(path, []byte(r.Replace(tc.args.settings)), 0o600))
			}
			for recordDir, sum := range tc.args.approved {
				path := filepath.Join(home, ".claude", "nodloop", "vetoes.approved."+sum+".yaml")
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
				require.NoError(t, os.WriteFile(path, []byte("# Generated by nodloop from the approved judgment knowledge of "+
					recordDir+"\nvetoes: [{id: k, tool: Bash, when: [{field: command, match: x}], reason: r}]\n"), 0o644))
			}
			var stdout, stderr bytes.Buffer
			cmd := guardCommand{home: homeDir(home), out: &stdout, errOut: &stderr}
			if tc.args.homeless {
				cmd.home = ""
			}

			err := cmd.check(cwd)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, r.Replace(tc.want.stdout), stdout.String())
			assert.Equal(t, r.Replace(tc.want.stderr), stderr.String())
		})
	}
}

// guard call answers an agent outside Claude Code with the vetoes the hook would apply
func TestRunGuardCall(t *testing.T) {
	valid, err := os.ReadFile("testdata/valid.yaml")
	require.NoError(t, err)
	home, project := t.TempDir(), t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude", "nodloop"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".claude", "nodloop", "vetoes.yaml"), valid, 0o600))
	getenv := func(k string) string { return map[string]string{"HOME": home}[k] }
	type want struct {
		code   int
		stdout string
		// The start of the first stderr line
		stderr string
	}
	tcs := []struct {
		name string
		args []string
		want want
	}{
		{"a forbidden call is blocked with its reason", []string{"call", "--tool", "Bash", "--input", `{"command":"sed -i '' s/a/b/ f.txt"}`, "--dir", project},
			want{0, `{"action":"block","reason":"sed -i and perl -i are forbidden. Use the Edit tool to modify files","veto":"no-sed-inplace"}` + "\n", ""}},
		{"another call is allowed", []string{"call", "--tool", "Bash", "--input", `{"command":"ls"}`, "--dir", project},
			want{0, `{"action":"allow"}` + "\n", ""}},
		{"no tool is refused", []string{"call", "--dir", project},
			want{1, "", "nodloop guard call: call: --tool is required"}},
		{"an input that is no object is refused", []string{"call", "--tool", "Bash", "--input", `[1]`, "--dir", project},
			want{1, "", "nodloop guard call: call input is not a JSON object"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer

			code := runGuard(tc.args, getenv, os.Executable, time.Now, strings.NewReader(""), &stdout, &stderr)

			assert.Equal(t, tc.want.code, code)
			assert.Equal(t, tc.want.stdout, stdout.String())
			line, _, _ := strings.Cut(stderr.String(), "\n")
			assert.True(t, strings.HasPrefix(line, tc.want.stderr), stderr.String())
		})
	}
}
