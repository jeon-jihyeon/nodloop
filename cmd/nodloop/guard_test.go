package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/veto"
	vetofile "github.com/jeon-jihyeon/nodloop/internal/veto/file"
)

func TestRunGuard(t *testing.T) {
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
			"hook fails on an unknown flag",
			args{args: []string{"--nope"}, home: "{home}", settings: "{}"},
			want{1, "", `^flag provided but not defined: -nope\n`, "{}"},
		},
		{
			"check lists the user vetoes and the hook state",
			args{args: []string{"check"}, home: "{home}", settings: "{}"},
			want{0, "{home}/{rel}: 2 vetoes\nmerged: 2 vetoes\nguard hook not installed. Run nodloop guard install to enforce them\n", `^$`, "{}"},
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
			home := t.TempDir()
			path := filepath.Join(home, ".claude", "settings.json")
			exe := filepath.Join(home, "opt", "nodloop")
			link := filepath.Join(home, ".nodloop", "bin", "nodloop")
			r := strings.NewReplacer(
				"{home}", home, "{path}", path, "{exe}", exe, "{link}", link, "{rel}", vetofile.RelPath,
			)
			vetoes := filepath.Join(home, vetofile.RelPath)
			require.NoError(t, os.MkdirAll(filepath.Dir(vetoes), 0o755))
			require.NoError(t, os.WriteFile(vetoes, valid, 0o644))
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
			got := runGuard(args, getenv, executable, stdin, &stdout, &stderr)

			assert.Equal(t, tc.want.code, got)
			assert.Equal(t, r.Replace(tc.want.stdout), stdout.String())
			assert.Regexp(t, tc.want.stderr, stderr.String())
			settings, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, r.Replace(tc.want.settings), string(settings))
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
	}
	type want struct {
		stdout string
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
			want{"no veto file found (looked for {rel} from {cwd} up to its project root and under {home})\n" + unhooked, nil},
		},
		{
			"both files list their counts and the merge",
			args{files: map[string][]byte{"{cwd}": valid, "{home}": valid}},
			want{"{cwd}/{rel}: 2 vetoes\n{home}/{rel}: 2 vetoes\nmerged: 2 vetoes\n" + unhooked, nil},
		},
		{
			"project file alone is listed",
			args{files: map[string][]byte{"{cwd}": valid}},
			want{"{cwd}/{rel}: 2 vetoes\nmerged: 2 vetoes\n" + unhooked, nil},
		},
		{
			"project file in a parent of the cwd is listed",
			args{files: map[string][]byte{"{parent}": valid}},
			want{"{parent}/{rel}: 2 vetoes\nmerged: 2 vetoes\n" + unhooked, nil},
		},
		{
			"project files of the cwd and a parent are both listed nearest first",
			args{files: map[string][]byte{"{cwd}": valid, "{parent}": valid}},
			want{"{cwd}/{rel}: 2 vetoes\n{parent}/{rel}: 2 vetoes\nmerged: 2 vetoes\n" + unhooked, nil},
		},
		{
			"user file alone is listed",
			args{files: map[string][]byte{"{home}": valid}},
			want{"{home}/{rel}: 2 vetoes\nmerged: 2 vetoes\n" + unhooked, nil},
		},
		{
			"invalid regexp fails after the listing",
			args{files: map[string][]byte{"{cwd}": broken}},
			want{"merged: 0 vetoes\n" + unhooked, veto.ErrMatchInvalid},
		},
		{
			"broken project file still lists the user file",
			args{files: map[string][]byte{"{cwd}": broken, "{home}": valid}},
			want{"{home}/{rel}: 2 vetoes\nmerged: 2 vetoes\n" + unhooked, veto.ErrMatchInvalid},
		},
		{
			"file with a broken entry lists its valid entries and names a veto that blocks nothing",
			args{files: map[string][]byte{"{home}": partial}},
			want{
				"{home}/{rel}: 2 vetoes\n{home}/{rel}: veto lower-case-tool blocks nothing on [\"bash\"]: " +
					veto.ErrToolUnknown.Error() + "\nmerged: 2 vetoes\n" + unhooked,
				veto.ErrMatchInvalid,
			},
		},
		{
			"a registered hook is reported installed",
			args{files: map[string][]byte{"{home}": valid}, settings: hook, matcher: "*"},
			want{"{home}/{rel}: 2 vetoes\nmerged: 2 vetoes\nguard hook installed\n", nil},
		},
		{
			"a hook left on an older plugin binary is reported stale",
			args{files: map[string][]byte{"{home}": valid}, settings: hook, matcher: "*", exe: "{bin}/v0.4.1/nodloop"},
			want{
				"{home}/{rel}: 2 vetoes\nmerged: 2 vetoes\n" +
					"guard hook stale: hook runs another binary than the stable link: {bin}/v0.4.1/nodloop runs while the stable link runs " +
					"{bin}/nodloop. Run {bin}/nodloop guard install so the hook follows the plugin\n",
				nil,
			},
		},
		{
			"a hook on the stable link is reported installed",
			args{files: map[string][]byte{"{home}": valid}, settings: hook, matcher: "*", exe: "{bin}/nodloop"},
			want{"{home}/{rel}: 2 vetoes\nmerged: 2 vetoes\nguard hook installed\n", nil},
		},
		{
			"a hook on the binary the stable link names is reported installed",
			args{files: map[string][]byte{"{home}": valid}, settings: hook, matcher: "*", exe: "{bin}/v0.5.0/nodloop"},
			want{"{home}/{rel}: 2 vetoes\nmerged: 2 vetoes\nguard hook installed\n", nil},
		},
		{
			"a hook on Bash alone is reported broken",
			args{files: map[string][]byte{"{home}": valid}, settings: hook, matcher: "Bash"},
			want{
				"{home}/{rel}: 2 vetoes\nmerged: 2 vetoes\n" +
					"guard hook broken: hook matcher leaves tools out: \"Bash\". Run nodloop guard install to repair it\n",
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
				nil,
			},
		},
		{
			"broken settings leave the hook state unknown",
			args{files: map[string][]byte{"{home}": valid}, settings: "{"},
			want{"{home}/{rel}: 2 vetoes\nmerged: 2 vetoes\nguard hook unknown: settings file: not valid JSON: " +
				"{home}/.claude/settings.json: unexpected end of JSON input\n", nil},
		},
		{
			"an unknown home still lists the project file",
			args{files: map[string][]byte{"{cwd}": valid}, homeless: true},
			want{"{cwd}/{rel}: 2 vetoes\nmerged: 2 vetoes\nguard hook unknown: home directory unknown\n", nil},
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
			var stdout bytes.Buffer
			cmd := guardCommand{home: homeDir(home), out: &stdout}
			if tc.args.homeless {
				cmd.home = ""
			}

			err := cmd.check(cwd)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, r.Replace(tc.want.stdout), stdout.String())
		})
	}
}
