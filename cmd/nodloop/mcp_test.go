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

	"github.com/jeon-jihyeon/nodloop/internal/mcp"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
)

func TestShellWord(t *testing.T) {
	tcs := []struct {
		name string
		args string
		want string
	}{
		{"a plain path stays as it is", "/home/u/.nodloop/bin/nodloop", "/home/u/.nodloop/bin/nodloop"},
		{"a path with a space is quoted", "/Users/u/My Tools/nodloop", "'/Users/u/My Tools/nodloop'"},
		{"a quote in the path is escaped", "/tmp/it's/nodloop", `'/tmp/it'\''s/nodloop'`},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, shellWord(tc.args))
		})
	}
}

func TestRunMCP(t *testing.T) {
	data := testkit.DemoDir(t)
	tools := strings.Join(mcp.Tools(), "\n") + "\n"
	at := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	const (
		unset = `^nodloop mcp: ` + envFileDir + ` is not set: run nodloop setup or set the variable\. Run .* setup --data-dir <dir> ` +
			`and reconnect the nodloop server\n$`
		fix = `\. Fix what this names or run .* setup --data-dir <dir> again and reconnect the nodloop server\n$`
	)
	type args struct {
		args []string
		// Values with the `{home}` and `{records}` placeholders
		env map[string]string
		// Files written under home before the run keyed by their relative path
		files map[string]string
	}
	type want struct {
		code int
		// Regexp matched against stderr
		stderr string
		stdout string
		// The setup config under home after the run
		config    string
		configErr error
		// Error of reading the record dir under home
		homeRecords error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"list prints the tool names without opening any data",
			args{[]string{"--list"}, map[string]string{"HOME": "{home}"}, nil},
			want{0, `^$`, tools, "", os.ErrNotExist, os.ErrNotExist},
		},
		{
			"serves until stdin closes",
			args{nil, map[string]string{envFileDir: data, envRecordDir: "{records}"}, nil},
			want{0, `^$`, "", "", os.ErrNotExist, os.ErrNotExist},
		},
		{
			"records default under home",
			args{nil, map[string]string{"HOME": "{home}", envFileDir: data}, nil},
			want{0, `^$`, "", "", os.ErrNotExist, nil},
		},
		{
			"nothing configured serves every tool with the setup error and writes nothing",
			args{nil, map[string]string{"HOME": "{home}"}, nil},
			want{0, unset, "", "", os.ErrNotExist, os.ErrNotExist},
		},
		{
			"broken config is served unconfigured and kept",
			args{nil, map[string]string{"HOME": "{home}"}, map[string]string{".nodloop/config.json": "{broken"}},
			want{0, `^nodloop mcp: config.json is not valid JSON: .*` + fix, "", "{broken", nil, os.ErrNotExist},
		},
		{
			"data dir that moved away is served unconfigured",
			args{nil, map[string]string{envFileDir: "{home}/moved", envRecordDir: "{records}"}, nil},
			want{0, `^nodloop mcp: evidence file source: stat {home}/moved: no such file or directory` + fix, "", "",
				os.ErrNotExist, os.ErrNotExist},
		},
		{
			"broken policy is served unconfigured",
			args{nil, map[string]string{envFileDir: "{home}/data", envRecordDir: "{records}"},
				map[string]string{"data/policy.yaml": "version: v1\n  bad: [\n"}},
			want{0, `^nodloop mcp: analysis: policy is not valid yaml: .*` + fix, "", "", os.ErrNotExist, os.ErrNotExist},
		},
		{
			"config without a data dir is served unconfigured and kept",
			args{nil, map[string]string{"HOME": "{home}"}, map[string]string{".nodloop/config.json": `{"file_dir":""}`}},
			want{0, unset, "", `{"file_dir":""}`, nil, os.ErrNotExist},
		},
		{
			"nothing configured without a home is served unconfigured",
			args{nil, nil, nil},
			want{0, unset, "", "", os.ErrNotExist, os.ErrNotExist},
		},
		{
			"record dir that is a file is served unconfigured",
			args{nil, map[string]string{envFileDir: data, envRecordDir: "{records}/regular"}, nil},
			want{0, "^nodloop mcp: record dir: .*" + fix, "", "", os.ErrNotExist, os.ErrNotExist},
		},
		{
			"unknown flag fails",
			args{[]string{"--nope"}, nil, nil},
			want{1, "^flag provided but not defined: -nope\n", "", "", os.ErrNotExist, os.ErrNotExist},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, records := t.TempDir(), t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(records, "regular"), nil, 0o600))
			for rel, content := range tc.args.files {
				path := filepath.Join(home, rel)
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
				require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
			}
			stdout, err := os.Create(filepath.Join(t.TempDir(), "stdout"))
			require.NoError(t, err)
			r := strings.NewReplacer("{home}", home, "{records}", records)
			getenv := func(k string) string { return r.Replace(tc.args.env[k]) }
			var stderr bytes.Buffer

			got := runMCP(tc.args.args, getenv, func() time.Time { return at }, strings.NewReader(""), stdout, &stderr)

			assert.Equal(t, tc.want.code, got)
			assert.Regexp(t, r.Replace(tc.want.stderr), stderr.String())
			out, err := os.ReadFile(stdout.Name())
			require.NoError(t, err)
			assert.Equal(t, tc.want.stdout, string(out))
			config, err := os.ReadFile(homeDir(home).configPath())
			assert.ErrorIs(t, err, tc.want.configErr)
			assert.Equal(t, r.Replace(tc.want.config), string(config))
			_, err = os.Stat(homeDir(home).recordDir())
			assert.ErrorIs(t, err, tc.want.homeRecords)
		})
	}
}
