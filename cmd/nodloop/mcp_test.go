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

func TestRunMCP(t *testing.T) {
	data := testkit.DemoDir(t)
	tools := strings.Join(mcp.Tools(), "\n") + "\n"
	at := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	const demoConfig = "{\n  \"file_dir\": \"{home}/.nodloop/demo\"\n}\n"
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
			"nothing configured unpacks the demo under home",
			args{nil, map[string]string{"HOME": "{home}"}, nil},
			want{
				0, `^nodloop mcp: no data configured. Unpacked the demo set to {home}/.nodloop/demo and wrote ` +
					`{home}/.nodloop/config.json. `, "", demoConfig, nil, nil,
			},
		},
		{
			"demo setup keeps the record dir variable",
			args{nil, map[string]string{"HOME": "{home}", envRecordDir: "{records}"}, nil},
			want{0, `^nodloop mcp: no data configured. Unpacked the demo set`, "", demoConfig, nil, os.ErrNotExist},
		},
		{
			"broken config is reported and kept",
			args{nil, map[string]string{"HOME": "{home}"}, map[string]string{".nodloop/config.json": "{broken"}},
			want{1, `^nodloop mcp: config.json is not valid JSON: `, "", "{broken", nil, os.ErrNotExist},
		},
		{
			"config without a data dir is reported and kept",
			args{nil, map[string]string{"HOME": "{home}"}, map[string]string{".nodloop/config.json": `{"file_dir":""}`}},
			want{1, `^nodloop mcp: ` + envFileDir + ` is not set: `, "", `{"file_dir":""}`, nil, os.ErrNotExist},
		},
		{
			"nothing configured without a home fails",
			args{nil, nil, nil},
			want{1, "^nodloop mcp: home directory unknown: cannot set up the demo\n$", "", "", os.ErrNotExist, os.ErrNotExist},
		},
		{
			"record dir that is a file fails",
			args{nil, map[string]string{envFileDir: data, envRecordDir: "{records}/regular"}, nil},
			want{1, "^nodloop mcp: record dir: ", "", "", os.ErrNotExist, os.ErrNotExist},
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
