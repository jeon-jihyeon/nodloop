package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/testkit"
)

func TestRunSetup(t *testing.T) {
	cwd, err := os.Getwd()
	require.NoError(t, err)
	const passedOver = `Only \.md files directly under procedures are procedures and a folder setup cannot open is passed over\n$`
	type args struct {
		args []string
		// Empty for an unknown home
		home string
		// config.json under home before the run
		// Empty means none
		config string
		// NODLOOP_RECORD_DIR during the run
		recordEnv string
		// The default records under home hold a file
		seeded bool
		// NODLOOP_FILE_DIR during the run
		fileEnv string
	}
	type want struct {
		code   int
		stdout string
		// Regexp matched against stderr
		stderr string
		// The config a later data command resolves from HOME alone
		// A failed run leaves the config of before
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
			args{[]string{"--data-dir", "../../examples/demo", "--record-dir", "records"}, "{home}", "", "", false, ""},
			want{
				0, "data {demo}\nrecords {cwd}/records\nconfig {home}/.nodloop/config.json\n", `^$`,
				config{dataDir: "{demo}", recordDir: "{cwd}/records", home: "{home}"}, nil,
			},
		},
		{
			"records default under home",
			args{[]string{"--data-dir", "{demo}"}, "{home}", "", "", false, ""},
			want{
				0, "data {demo}\nrecords {home}/.nodloop/records\nconfig {home}/.nodloop/config.json\n", `^$`,
				config{dataDir: "{demo}", recordDir: "{home}/.nodloop/records", home: "{home}"}, nil,
			},
		},
		{
			"a rerun without a record dir keeps the saved one",
			args{[]string{"--data-dir", "{demo}"}, "{home}", `{"file_dir":"{own}","record_dir":"{team}"}`, "", false, ""},
			want{
				0, "data {demo}\nrecords {team}\nconfig {home}/.nodloop/config.json\n", `^$`,
				config{dataDir: "{demo}", recordDir: "{team}", home: "{home}"}, nil,
			},
		},
		{
			"a record dir flag replaces the saved one",
			args{[]string{"--data-dir", "{demo}", "--record-dir", "{team}"}, "{home}", `{"file_dir":"{demo}","record_dir":"{shared}"}`, "", false, ""},
			want{
				0, "data {demo}\nrecords {team}\nconfig {home}/.nodloop/config.json\n", `^$`,
				config{dataDir: "{demo}", recordDir: "{team}", home: "{home}"}, nil,
			},
		},
		{
			"a broken config without a record dir fails and stays as it was",
			args{[]string{"--data-dir", "{demo}"}, "{home}", `{"file_dir":"{own}","record_dir":"{team}",}`, "", false, ""},
			want{
				1, "", `^nodloop setup: config\.json is not valid JSON: .*\. Fix it or run setup again with --record-dir <dir> ` +
					`since the record dir saved in it cannot be read\n$`,
				config{}, errConfigInvalid,
			},
		},
		{
			"a broken config with a record dir is replaced",
			args{[]string{"--data-dir", "{demo}", "--record-dir", "{shared}"}, "{home}", "{broken", "", false, ""},
			want{
				0, "data {demo}\nrecords {shared}\nconfig {home}/.nodloop/config.json\n", `^$`,
				config{dataDir: "{demo}", recordDir: "{shared}", home: "{home}"}, nil,
			},
		},
		{
			"NODLOOP_FILE_DIR set to another dir prints that dir and warns",
			args{[]string{"--data-dir", "{own}"}, "{home}", "", "", false, "{demo}"},
			want{
				0, "data {demo}\nrecords {home}/.nodloop/records\nconfig {home}/.nodloop/config.json\n",
				`^nodloop setup: warning: NODLOOP_FILE_DIR is {demo} and wins over the saved data dir so every command started with it ` +
					`reviews {demo}\. Unset it to review {own}\n$`,
				config{dataDir: "{own}", recordDir: "{home}/.nodloop/records", home: "{home}"}, nil,
			},
		},
		{
			"NODLOOP_FILE_DIR naming the same dir relative stays silent",
			args{[]string{"--data-dir", "{demo}"}, "{home}", "", "", false, "../../examples/demo"},
			want{
				0, "data {demo}\nrecords {home}/.nodloop/records\nconfig {home}/.nodloop/config.json\n", `^$`,
				config{dataDir: "{demo}", recordDir: "{home}/.nodloop/records", home: "{home}"}, nil,
			},
		},
		{
			"a new data dir over records that hold files warns",
			args{[]string{"--data-dir", "{own}"}, "{home}", `{"file_dir":"{demo}","record_dir":"{shared}"}`, "", false, ""},
			want{
				0, "data {own}\nrecords {shared}\nconfig {home}/.nodloop/config.json\n",
				`^nodloop setup: warning: {shared} holds the reviews and knowledge of {demo} and they carry into reviews of {own}\. ` +
					`Run setup again with --record-dir <new dir> to keep them apart\n$`,
				config{dataDir: "{own}", recordDir: "{shared}", home: "{home}"}, nil,
			},
		},
		{
			"a new data dir with a new record dir stays silent",
			args{[]string{"--data-dir", "{own}", "--record-dir", "{team}"}, "{home}", `{"file_dir":"{demo}","record_dir":"{shared}"}`, "", false, ""},
			want{
				0, "data {own}\nrecords {team}\nconfig {home}/.nodloop/config.json\n", `^$`,
				config{dataDir: "{own}", recordDir: "{team}", home: "{home}"}, nil,
			},
		},
		{
			"a new record dir under NODLOOP_RECORD_DIR still warns",
			args{[]string{"--data-dir", "{own}", "--record-dir", "{team}"}, "{home}", `{"file_dir":"{demo}"}`, "{shared}", false, ""},
			want{
				0, "data {own}\nrecords {shared}\nconfig {home}/.nodloop/config.json\n",
				`^nodloop setup: warning: {shared} holds the reviews and knowledge of {demo} and they carry into reviews of {own}\. ` +
					`Set NODLOOP_RECORD_DIR to a new dir to keep them apart\n$`,
				config{dataDir: "{own}", recordDir: "{team}", home: "{home}"}, nil,
			},
		},
		{
			"the default records named explicitly over a config without a record dir warn",
			args{[]string{"--data-dir", "{own}", "--record-dir", "{home}/.nodloop/records"}, "{home}", `{"file_dir":"{demo}"}`, "", true, ""},
			want{
				0, "data {own}\nrecords {home}/.nodloop/records\nconfig {home}/.nodloop/config.json\n",
				`^nodloop setup: warning: {home}/\.nodloop/records holds the reviews and knowledge of {demo} and they carry into reviews of {own}\. ` +
					`Run setup again with --record-dir <new dir> to keep them apart\n$`,
				config{dataDir: "{own}", recordDir: "{home}/.nodloop/records", home: "{home}"}, nil,
			},
		},
		{
			"a relative NODLOOP_RECORD_DIR fails before the config changes",
			args{[]string{"--data-dir", "{demo}"}, "{home}", `{"file_dir":"{own}","record_dir":"{team}"}`, "records", false, ""},
			want{
				1, "", "^nodloop setup: the record directory must be an absolute path: NODLOOP_RECORD_DIR is \"records\"",
				config{dataDir: "{own}", recordDir: "{team}", home: "{home}"}, nil,
			},
		},
		{
			"a rerun with the same data dir stays silent",
			args{[]string{"--data-dir", "{demo}"}, "{home}", `{"file_dir":"{demo}","record_dir":"{shared}"}`, "", false, ""},
			want{
				0, "data {demo}\nrecords {shared}\nconfig {home}/.nodloop/config.json\n", `^$`,
				config{dataDir: "{demo}", recordDir: "{shared}", home: "{home}"}, nil,
			},
		},
		{
			"a rerun through a link to the saved data dir stays silent",
			args{[]string{"--data-dir", "{demolink}"}, "{home}", `{"file_dir":"{demo}","record_dir":"{shared}"}`, "", false, ""},
			want{
				0, "data {demolink}\nrecords {shared}\nconfig {home}/.nodloop/config.json\n", `^$`,
				config{dataDir: "{demolink}", recordDir: "{shared}", home: "{home}"}, nil,
			},
		},
		{
			"a rerun through the real path of a saved linked data dir stays silent",
			args{[]string{"--data-dir", "{demo}"}, "{home}", `{"file_dir":"{demolink}","record_dir":"{shared}"}`, "", false, ""},
			want{
				0, "data {demo}\nrecords {shared}\nconfig {home}/.nodloop/config.json\n", `^$`,
				config{dataDir: "{demo}", recordDir: "{shared}", home: "{home}"}, nil,
			},
		},
		{
			"NODLOOP_FILE_DIR naming the same dir through a link stays silent",
			args{[]string{"--data-dir", "{demo}"}, "{home}", "", "", false, "{demolink}"},
			want{
				0, "data {demolink}\nrecords {home}/.nodloop/records\nconfig {home}/.nodloop/config.json\n", `^$`,
				config{dataDir: "{demo}", recordDir: "{home}/.nodloop/records", home: "{home}"}, nil,
			},
		},
		{
			"a new data dir over the saved records named through a link warns",
			args{[]string{"--data-dir", "{own}", "--record-dir", "{shared}"}, "{home}", `{"file_dir":"{demo}","record_dir":"{sharedlink}"}`, "", false, ""},
			want{
				0, "data {own}\nrecords {shared}\nconfig {home}/.nodloop/config.json\n",
				`^nodloop setup: warning: {shared} holds the reviews and knowledge of {demo} and they carry into reviews of {own}\. ` +
					`Run setup again with --record-dir <new dir> to keep them apart\n$`,
				config{dataDir: "{own}", recordDir: "{shared}", home: "{home}"}, nil,
			},
		},
		{
			"Markdown beside the procedures warns with each entry named",
			args{[]string{"--data-dir", "{archived}"}, "{home}", "", "", false, ""},
			want{
				0, "data {archived}\nrecords {home}/.nodloop/records\nconfig {home}/.nodloop/config.json\n",
				`^nodloop setup: warning: reviews never read {archived}/procedures/archive\. ` + passedOver,
				config{dataDir: "{archived}", recordDir: "{home}/.nodloop/records", home: "{home}"}, nil,
			},
		},
		{
			"a procedures folder setup cannot open warns with the folder named",
			args{[]string{"--data-dir", "{private}"}, "{home}", "", "", false, ""},
			want{
				0, "data {private}\nrecords {home}/.nodloop/records\nconfig {home}/.nodloop/config.json\n",
				`^nodloop setup: warning: reviews never read {private}/procedures/private\. ` + passedOver,
				config{dataDir: "{private}", recordDir: "{home}/.nodloop/records", home: "{home}"}, nil,
			},
		},
		{
			"data dir whose export lost a metric the policy reads fails",
			args{[]string{"--data-dir", "{lost}"}, "{home}", "", "", false, ""},
			want{
				1, "", `^nodloop setup: analysis: policy names a metric or dimension no event carries: ` +
					`proportion_control reads metric "conversion_count"\n$`,
				config{}, errDataDirUnset,
			},
		},
		{
			"data dir with a policy that does not parse fails",
			args{[]string{"--data-dir", "{badpolicy}"}, "{home}", "", "", false, ""},
			want{1, "", "^nodloop setup: analysis: policy is not valid yaml: ", config{}, errDataDirUnset},
		},
		{
			"data dir with a policy that names a metric no event carries fails",
			args{[]string{"--data-dir", "{typo}"}, "{home}", "", "", false, ""},
			want{
				1, "", `^nodloop setup: analysis: policy names a metric or dimension no event carries: zscore reads metric "clicks"\n$`,
				config{}, errDataDirUnset,
			},
		},
		{
			"data dir without events fails",
			args{[]string{"--data-dir", "{empty}"}, "{home}", "", "", false, ""},
			want{1, "", "^nodloop setup: no events.csv: {empty}\n$", config{}, errDataDirUnset},
		},
		{
			"data dir without a policy fails",
			args{[]string{"--data-dir", "{events}"}, "{home}", "", "", false, ""},
			want{1, "", "^nodloop setup: no policy.yaml: {events}\n$", config{}, errDataDirUnset},
		},
		{
			"data dir without procedures fails",
			args{[]string{"--data-dir", "{policy}"}, "{home}", "", "", false, ""},
			want{
				1, "", "^nodloop setup: evidence file source: no procedure .md file under procedures: {policy}/procedures\n$",
				config{}, errDataDirUnset,
			},
		},
		{
			"data dir whose procedures sit in a subfolder fails",
			args{[]string{"--data-dir", "{nested}"}, "{home}", "", "", false, ""},
			want{
				1, "", "^nodloop setup: evidence file source: procedures must be .md files directly under procedures: " +
					"{nested}/procedures/team\n$",
				config{}, errDataDirUnset,
			},
		},
		{
			"no flags fail",
			args{nil, "{home}", "", "", false, ""},
			want{1, "", "^nodloop setup: --data-dir is required\n\nusage:", config{}, errDataDirUnset},
		},
		{
			"unknown home fails",
			args{[]string{"--data-dir", "{demo}"}, "", "", "", false, ""},
			want{1, "", "^nodloop setup: home directory unknown: HOME is not set\n$", config{}, errDataDirUnset},
		},
		{
			"the removed demo flag is unknown",
			args{[]string{"--demo"}, "{home}", "", "", false, ""},
			want{1, "", "^flag provided but not defined: -demo\n", config{}, errDataDirUnset},
		},
		{
			"unknown flag fails",
			args{[]string{"--nope"}, "{home}", "", "", false, ""},
			want{1, "", "^flag provided but not defined: -nope\n", config{}, errDataDirUnset},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			events := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(events, "events.csv"), nil, 0o600))
			policy, nested := t.TempDir(), t.TempDir()
			for _, dir := range []string{policy, nested} {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "events.csv"), []byte("event_id,timestamp,metric,value\n"), 0o600))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "policy.yaml"), []byte("version: v1\n"), 0o600))
			}
			require.NoError(t, os.MkdirAll(filepath.Join(nested, "procedures", "team"), 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(nested, "procedures", "team", "a.md"), nil, 0o600))
			demo, own, badPolicy := testkit.DemoDir(t), filepath.Join(t.TempDir(), "own"), filepath.Join(t.TempDir(), "bad")
			require.NoError(t, os.CopyFS(own, os.DirFS(demo)))
			require.NoError(t, os.CopyFS(badPolicy, os.DirFS(demo)))
			require.NoError(t, os.WriteFile(filepath.Join(badPolicy, "policy.yaml"), []byte("version: v1\n  bad: [\n"), 0o600))
			typo := filepath.Join(t.TempDir(), "typo")
			require.NoError(t, os.CopyFS(typo, os.DirFS(demo)))
			demoPolicy, err := os.ReadFile(filepath.Join(demo, "policy.yaml"))
			require.NoError(t, err)
			misspelled := strings.Replace(string(demoPolicy), "[click_count]", "[clicks]", 1)
			require.NoError(t, os.WriteFile(filepath.Join(typo, "policy.yaml"), []byte(misspelled), 0o600))
			archived := filepath.Join(t.TempDir(), "archived")
			require.NoError(t, os.CopyFS(archived, os.DirFS(demo)))
			require.NoError(t, os.MkdirAll(filepath.Join(archived, "procedures", "archive"), 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(archived, "procedures", "archive", "old.md"), nil, 0o600))
			private := filepath.Join(t.TempDir(), "private")
			require.NoError(t, os.CopyFS(private, os.DirFS(demo)))
			locked := filepath.Join(private, "procedures", "private")
			require.NoError(t, os.Mkdir(locked, 0))
			t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
			if _, err := os.ReadDir(locked); err == nil && slices.Contains(tc.args.args, "{private}") {
				t.Skip("folder permissions do not stop a read here")
			}
			shared := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(shared, "traces.jsonl"), nil, 0o600))
			links := t.TempDir()
			demoLink, sharedLink := filepath.Join(links, "demo"), filepath.Join(links, "shared")
			require.NoError(t, os.Symlink(demo, demoLink))
			require.NoError(t, os.Symlink(shared, sharedLink))
			r := strings.NewReplacer(
				"{demolink}", demoLink, "{sharedlink}", sharedLink,
				"{home}", t.TempDir(), "{empty}", t.TempDir(), "{events}", events, "{policy}", policy, "{nested}", nested,
				"{demo}", demo, "{own}", own, "{badpolicy}", badPolicy, "{shared}", shared, "{team}", t.TempDir(), "{cwd}", cwd,
				"{archived}", archived, "{typo}", typo, "{private}", private, "{lost}", lostMetricDir(t, "conversion_count"),
			)
			args := make([]string, 0, len(tc.args.args))
			for _, a := range tc.args.args {
				args = append(args, r.Replace(a))
			}
			home := r.Replace(tc.args.home)
			if tc.args.config != "" {
				require.NoError(t, os.MkdirAll(homeDir(home).dir(), 0o755))
				require.NoError(t, os.WriteFile(homeDir(home).configPath(), []byte(r.Replace(tc.args.config)), 0o600))
			}
			if tc.args.seeded {
				require.NoError(t, os.MkdirAll(homeDir(home).recordDir(), 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(homeDir(home).recordDir(), "traces.jsonl"), nil, 0o600))
			}
			getenv := func(k string) string {
				return map[string]string{
					"HOME": home, envRecordDir: r.Replace(tc.args.recordEnv), envFileDir: r.Replace(tc.args.fileEnv),
				}[k]
			}
			var stdout, stderr bytes.Buffer

			got := runSetup(args, getenv, &stdout, &stderr)

			assert.Equal(t, tc.want.code, got)
			assert.Equal(t, r.Replace(tc.want.stdout), stdout.String())
			assert.Regexp(t, r.Replace(tc.want.stderr), stderr.String())
			cfg, err := resolveConfig(func(k string) string { return map[string]string{"HOME": home}[k] }, "", "", "")
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
