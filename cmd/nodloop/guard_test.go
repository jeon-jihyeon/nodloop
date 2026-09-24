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
	exe, err := os.Executable()
	require.NoError(t, err)
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
			"            \"command\": \"{exe} guard\",\n            \"timeout\": 5,\n            \"type\": \"command\"\n" +
			"          }\n        ],\n        \"matcher\": \"*\"\n      }\n    ]\n  }\n}\n"
	)
	type args struct {
		args  []string
		stdin []byte
		// Empty for an unknown home
		home string
		// Content of settings.json before the run
		settings string
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
		{"hook passes a plain call", args{nil, plain, "{home}", "{}"}, want{0, "", `^$`, "{}"}},
		{
			"hook blocks through the user vetoes",
			args{nil, sed, "{home}", "{}"},
			want{2, "", `^nodloop guard: Bash call blocked by veto no-sed-inplace\n`, "{}"},
		},
		{
			"hook blocks through the vetoes flag",
			args{[]string{"--vetoes", "testdata/valid.yaml"}, sed, "", "{}"},
			want{2, "", `^nodloop guard: Bash call blocked by veto no-sed-inplace\n`, "{}"},
		},
		{
			"hook fails on a missing veto file",
			args{[]string{"--vetoes", "nope.yaml"}, sed, "{home}", "{}"},
			want{1, "", `^nodloop guard: nope.yaml: failed to read veto file: open nope.yaml: no such file`, "{}"},
		},
		{
			"hook fails on an invalid veto file",
			args{[]string{"--vetoes", "testdata/invalid_regex.yaml"}, sed, "{home}", "{}"},
			want{1, "", `^nodloop guard: testdata/invalid_regex.yaml: vetoes\[0\] \(bad-regex\)`, "{}"},
		},
		{
			"hook fails on an unknown flag",
			args{[]string{"--nope"}, nil, "{home}", "{}"},
			want{1, "", `^flag provided but not defined: -nope\n`, "{}"},
		},
		{
			"check lists the user vetoes",
			args{[]string{"check"}, nil, "{home}", "{}"},
			want{0, "{home}/{rel}: 2 vetoes\nmerged: 2 vetoes\n", `^$`, "{}"},
		},
		{
			"install registers the absolute executable",
			args{[]string{"install"}, nil, "{home}", "{}"},
			want{0, "installed PreToolUse hook for {exe} in {path}\n", `^$`, written},
		},
		{
			"install twice changes nothing",
			args{[]string{"install"}, nil, "{home}", installed},
			want{0, "already installed in {path}\n", `^$`, installed},
		},
		{
			"uninstall removes the hook",
			args{[]string{"uninstall"}, nil, "{home}", installed},
			want{0, "removed PreToolUse hook from {path}\n", `^$`, "{}\n"},
		},
		{
			"uninstall without the hook changes nothing",
			args{[]string{"uninstall"}, nil, "{home}", "{}"},
			want{0, "not installed in {path}\n", `^$`, "{}"},
		},
		{
			"install fails on broken settings",
			args{[]string{"install"}, nil, "{home}", "{broken"},
			want{1, "", `^nodloop guard install: .*settings.json`, "{broken"},
		},
		{
			"uninstall fails on broken settings",
			args{[]string{"uninstall"}, nil, "{home}", "{broken"},
			want{1, "", `^nodloop guard uninstall: .*settings.json`, "{broken"},
		},
		{
			"install with an unknown home fails",
			args{[]string{"install"}, nil, "", "{}"},
			want{1, "", "^nodloop guard install: home directory unknown: cannot locate settings.json\n$", "{}"},
		},
		{
			"uninstall with an unknown home fails",
			args{[]string{"uninstall"}, nil, "", "{}"},
			want{1, "", "^nodloop guard uninstall: home directory unknown: cannot locate settings.json\n$", "{}"},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			path := filepath.Join(home, ".claude", "settings.json")
			r := strings.NewReplacer("{home}", home, "{path}", path, "{exe}", exe, "{rel}", vetofile.RelPath)
			vetoes := filepath.Join(home, vetofile.RelPath)
			require.NoError(t, os.MkdirAll(filepath.Dir(vetoes), 0o755))
			require.NoError(t, os.WriteFile(vetoes, valid, 0o644))
			require.NoError(t, os.WriteFile(path, []byte(r.Replace(tc.args.settings)), 0o600))
			getenv := func(k string) string { return map[string]string{"HOME": r.Replace(tc.args.home)}[k] }
			var stdout, stderr bytes.Buffer

			got := runGuard(tc.args.args, getenv, bytes.NewReader(tc.args.stdin), &stdout, &stderr)

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
	type args struct {
		// Veto file content keyed by the `{cwd}` or `{home}` placeholder
		files map[string][]byte
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
			args{nil},
			want{"no veto file found (looked for {rel} under {cwd} and {home})\n", nil},
		},
		{
			"both files list their counts and the merge",
			args{map[string][]byte{"{cwd}": valid, "{home}": valid}},
			want{"{cwd}/{rel}: 2 vetoes\n{home}/{rel}: 2 vetoes\nmerged: 2 vetoes\n", nil},
		},
		{
			"project file alone is listed",
			args{map[string][]byte{"{cwd}": valid}},
			want{"{cwd}/{rel}: 2 vetoes\nmerged: 2 vetoes\n", nil},
		},
		{
			"user file alone is listed",
			args{map[string][]byte{"{home}": valid}},
			want{"{home}/{rel}: 2 vetoes\nmerged: 2 vetoes\n", nil},
		},
		{
			"invalid regexp fails after the listing",
			args{map[string][]byte{"{cwd}": broken}},
			want{"merged: 0 vetoes\n", veto.ErrMatchInvalid},
		},
		{
			"broken project file still lists the user file",
			args{map[string][]byte{"{cwd}": broken, "{home}": valid}},
			want{"{home}/{rel}: 2 vetoes\nmerged: 2 vetoes\n", veto.ErrMatchInvalid},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cwd, home := t.TempDir(), t.TempDir()
			r := strings.NewReplacer("{cwd}", cwd, "{home}", home, "{rel}", vetofile.RelPath)
			for base, content := range tc.args.files {
				path := filepath.Join(r.Replace(base), vetofile.RelPath)
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
				require.NoError(t, os.WriteFile(path, content, 0o644))
			}
			var stdout bytes.Buffer

			err := guardCommand{home: homeDir(home), out: &stdout}.check(cwd)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, r.Replace(tc.want.stdout), stdout.String())
		})
	}
}
