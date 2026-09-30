package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/testkit"
)

func TestRunSetup(t *testing.T) {
	cwd, err := os.Getwd()
	require.NoError(t, err)
	type args struct {
		args []string
		// Empty for an unknown home
		home string
	}
	type want struct {
		code   int
		stdout string
		// Regexp matched against stderr
		stderr string
		// The config a later data command resolves from HOME alone
		cfg config
		err error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"data dir and record dir are stored absolute",
			args{[]string{"--data-dir", "../../examples/demo", "--record-dir", "records"}, "{home}"},
			want{
				0, "data {demo}\nconfig {home}/.nodloop/config.json\n", `^$`,
				config{dataDir: "{demo}", recordDir: "{cwd}/records", home: "{home}"}, nil,
			},
		},
		{
			"data dir without events fails",
			args{[]string{"--data-dir", "{empty}"}, "{home}"},
			want{1, "", "^nodloop setup: no events.csv: {empty}\n$", config{}, errDataDirUnset},
		},
		{
			"data dir without a policy fails",
			args{[]string{"--data-dir", "{events}"}, "{home}"},
			want{1, "", "^nodloop setup: no policy.yaml: {events}\n$", config{}, errDataDirUnset},
		},
		{
			"no flags fail",
			args{nil, "{home}"},
			want{1, "", "^nodloop setup: --data-dir is required\n\nusage:", config{}, errDataDirUnset},
		},
		{
			"unknown home fails",
			args{[]string{"--data-dir", "{demo}"}, ""},
			want{1, "", "^nodloop setup: home directory unknown: HOME is not set\n$", config{}, errDataDirUnset},
		},
		{
			"the removed demo flag is unknown",
			args{[]string{"--demo"}, "{home}"},
			want{1, "", "^flag provided but not defined: -demo\n", config{}, errDataDirUnset},
		},
		{
			"unknown flag fails",
			args{[]string{"--nope"}, "{home}"},
			want{1, "", "^flag provided but not defined: -nope\n", config{}, errDataDirUnset},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			events := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(events, "events.csv"), nil, 0o600))
			r := strings.NewReplacer(
				"{home}", t.TempDir(), "{empty}", t.TempDir(), "{events}", events,
				"{demo}", testkit.DemoDir(t), "{cwd}", cwd,
			)
			args := make([]string, 0, len(tc.args.args))
			for _, a := range tc.args.args {
				args = append(args, r.Replace(a))
			}
			home := r.Replace(tc.args.home)
			getenv := func(k string) string { return map[string]string{"HOME": home}[k] }
			var stdout, stderr bytes.Buffer

			got := runSetup(args, getenv, &stdout, &stderr)

			assert.Equal(t, tc.want.code, got)
			assert.Equal(t, r.Replace(tc.want.stdout), stdout.String())
			assert.Regexp(t, r.Replace(tc.want.stderr), stderr.String())
			cfg, err := resolveConfig(getenv, "", "", "")
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, config{
				dataDir: r.Replace(tc.want.cfg.dataDir), recordDir: r.Replace(tc.want.cfg.recordDir),
				home: homeDir(r.Replace(string(tc.want.cfg.home))),
			}, cfg)
		})
	}
}

// A config.json kept in a dotfiles folder and linked into place
func TestRunSetupConfigLink(t *testing.T) {
	type args struct {
		// The dotfiles copy before the run
		// Empty leaves the link dangling
		target string
	}
	type want struct {
		code   int
		stderr string
		// The dotfiles copy after the run
		target string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"a linked config stays a link and its target gets the new dirs",
			args{`{"file_dir":"/nowhere","record_dir":"{old}"}`},
			want{0, `^$`, "{\n  \"file_dir\": \"{demo}\",\n  \"record_dir\": \"{new}\"\n}\n"},
		},
		{
			"a dangling config link fails and stays dangling",
			args{""},
			want{1, `^nodloop setup: symlink to a missing file: {home}/\.nodloop/config\.json\n$`, ""},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, dotfiles := homeDir(t.TempDir()), t.TempDir()
			r := strings.NewReplacer("{demo}", testkit.DemoDir(t), "{old}", t.TempDir(), "{new}", t.TempDir(), "{home}", string(home))
			target := filepath.Join(dotfiles, "nodloop-config.json")
			if tc.args.target != "" {
				require.NoError(t, os.WriteFile(target, []byte(r.Replace(tc.args.target)), 0o600))
			}
			require.NoError(t, os.MkdirAll(home.dir(), 0o755))
			require.NoError(t, os.Symlink(target, home.configPath()))
			getenv := func(k string) string { return map[string]string{"HOME": string(home)}[k] }
			var stdout, stderr bytes.Buffer

			got := runSetup([]string{"--data-dir", r.Replace("{demo}"), "--record-dir", r.Replace("{new}")}, getenv, &stdout, &stderr)

			assert.Equal(t, tc.want.code, got)
			assert.Regexp(t, r.Replace(tc.want.stderr), stderr.String())
			info, err := os.Lstat(home.configPath())
			require.NoError(t, err)
			assert.Equal(t, os.ModeSymlink, info.Mode().Type(), "the link stays a link")
			after, _ := os.ReadFile(target)
			assert.Equal(t, r.Replace(tc.want.target), string(after))
		})
	}
}

// A write that fails keeps the saved config whole and leaves no temp file beside it
func TestRunSetupWriteFailure(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("directory permissions do not stop the write here")
	}
	type args struct {
		// Mode of `.nodloop` during the run
		mode os.FileMode
	}
	type want struct {
		code int
		// The record dir a later command resolves from HOME alone
		recordDir string
		// Entries of `.nodloop` after the run
		entries []string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a config dir that takes no new file fails and keeps the config", args{0o500}, want{1, "{saved}", []string{configFile}}},
		{"a writable config dir replaces the config and leaves no temp file", args{0o755}, want{0, "{other}", []string{configFile}}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, saved, other := homeDir(t.TempDir()), t.TempDir(), t.TempDir()
			r := strings.NewReplacer("{saved}", saved, "{other}", other)
			require.NoError(t, os.MkdirAll(home.dir(), 0o755))
			before := []byte(`{"file_dir":"` + testkit.DemoDir(t) + `","record_dir":"` + saved + `"}`)
			require.NoError(t, os.WriteFile(home.configPath(), before, 0o600))
			require.NoError(t, os.Chmod(home.dir(), tc.args.mode))
			t.Cleanup(func() { _ = os.Chmod(home.dir(), 0o755) })
			getenv := func(k string) string { return map[string]string{"HOME": string(home)}[k] }
			var stdout, stderr bytes.Buffer

			got := runSetup([]string{"--data-dir", testkit.DemoDir(t), "--record-dir", other}, getenv, &stdout, &stderr)

			assert.Equal(t, tc.want.code, got, stderr.String())
			cfg, err := resolveConfig(getenv, "", "", "")
			require.NoError(t, err)
			assert.Equal(t, r.Replace(tc.want.recordDir), cfg.recordDir)
			entries, err := os.ReadDir(home.dir())
			require.NoError(t, err)
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				names = append(names, e.Name())
			}
			assert.Equal(t, tc.want.entries, names)
		})
	}
}
