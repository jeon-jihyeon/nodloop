package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveConfig(t *testing.T) {
	saved := homeDir(t.TempDir())
	records := filepath.Join(t.TempDir(), "records")
	require.NoError(t, os.MkdirAll(saved.dir(), 0o755))
	require.NoError(t, os.WriteFile(saved.configPath(), []byte(`{"file_dir":"/old/data","record_dir":"`+records+`"}`), 0o600))
	bare := homeDir(t.TempDir())
	broken := homeDir(t.TempDir())
	require.NoError(t, os.MkdirAll(broken.dir(), 0o755))
	require.NoError(t, os.WriteFile(broken.configPath(), []byte("{broken"), 0o600))
	relative := homeDir(t.TempDir())
	require.NoError(t, os.MkdirAll(relative.dir(), 0o755))
	require.NoError(t, os.WriteFile(relative.configPath(), []byte(`{"record_dir":"records"}`), 0o600))
	wd, err := os.Getwd()
	require.NoError(t, err)
	type args struct {
		env       map[string]string
		recordDir string
	}
	type want struct {
		cfg config
		err error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"the env names the records", args{env: map[string]string{envRecordDir: "/records"}}, want{config{recordDir: "/records"}, nil}},
		{"the flag wins over the env", args{map[string]string{envRecordDir: "/records"}, "/flag-records"}, want{config{recordDir: "/flag-records"}, nil}},
		{
			"the saved config fills an unset value and an old file_dir is ignored",
			args{env: map[string]string{"HOME": string(saved)}},
			want{config{recordDir: records, home: saved}, nil},
		},
		{
			"the env wins over the saved config",
			args{env: map[string]string{"HOME": string(saved), envRecordDir: "/records"}},
			want{config{recordDir: "/records", home: saved}, nil},
		},
		{"a home without a config defaults records under home", args{env: map[string]string{"HOME": string(bare)}}, want{config{recordDir: bare.recordDir(), home: bare}, nil}},
		{"no home and no record dir stay unset", args{}, want{config{}, nil}},
		{"a broken saved config fails", args{env: map[string]string{"HOME": string(broken)}}, want{config{}, errConfigInvalid}},
		{
			"a relative flag resolves against the working directory",
			args{recordDir: "rec"},
			want{config{recordDir: filepath.Join(wd, "rec")}, nil},
		},
		{"a flag with dot segments is cleaned", args{recordDir: "/a/./b/../rec/"}, want{config{recordDir: "/a/rec"}, nil}},
		{
			"an absolute variable with a trailing slash is cleaned",
			args{env: map[string]string{envRecordDir: "/records/"}},
			want{config{recordDir: "/records"}, nil},
		},
		{
			"a relative variable fails because a server started elsewhere would read other records",
			args{env: map[string]string{envRecordDir: "records"}},
			want{config{}, errRecordDirRelative},
		},
		{"a relative record dir in the saved config fails", args{env: map[string]string{"HOME": string(relative)}}, want{config{}, errRecordDirRelative}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := resolveConfig(func(k string) string { return tc.args.env[k] }, tc.args.recordDir)

			require.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.cfg, got)
		})
	}
}

func TestConfigRecordArgs(t *testing.T) {
	assert.Equal(t, "", config{}.recordArgs())
	assert.Equal(t, "--record-dir /records", config{recordDir: "/records"}.recordArgs())
	assert.Equal(t, "--record-dir '/my records'", config{recordDir: "/my records"}.recordArgs())
}

func TestRunConfig(t *testing.T) {
	type args struct {
		// config.json before the call
		// None when empty
		saved string
		args  []string
		// HOME is unset
		homeless bool
	}
	type want struct {
		code   int
		stdout string
		stderr string
		// config.json after the call
		// Empty when there is none
		saved string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"nothing saved prints nothing", args{"", []string{"approver"}, false}, want{0, "", "", ""}},
		{"a saved name is printed", args{`{"approver":"ann"}`, []string{"approver"}, false}, want{0, "ann\n", "", `{"approver":"ann"}`}},
		{
			"a name is saved beside the other keys",
			args{`{"file_dir":"/old","record_dir":"/r"}`, []string{"approver", "Ann", "Lee"}, false},
			want{0, "Ann Lee\n", "", "{\n  \"approver\": \"Ann Lee\",\n  \"file_dir\": \"/old\",\n  \"record_dir\": \"/r\"\n}\n"},
		},
		{"a name is saved without a config", args{"", []string{"approver", "ann"}, false}, want{0, "ann\n", "", "{\n  \"approver\": \"ann\"\n}\n"}},
		{"a broken config is not overwritten", args{"{broken", []string{"approver", "ann"}, false}, want{1, "", "is not valid JSON", "{broken"}},
		{"a broken config is not printed", args{"{broken", []string{"approver"}, false}, want{1, "", "is not valid JSON", "{broken"}},
		{"another key is refused", args{"", []string{"record_dir", "/r"}, false}, want{1, "", "unknown action", ""}},
		{"no home is refused", args{"", []string{"approver", "ann"}, true}, want{1, "", "home directory unknown", ""}},
		{"a holdout share is saved", args{"", []string{"holdout", "0.1"}, false}, want{0, "0.1\n", "", "{\n  \"holdout\": 0.1\n}\n"}},
		{"a saved holdout is printed", args{`{"holdout":0.2}`, []string{"holdout"}, false}, want{0, "0.2\n", "", `{"holdout":0.2}`}},
		{"no holdout prints 0", args{"", []string{"holdout"}, false}, want{0, "0\n", "", ""}},
		{"a holdout of 1 is refused", args{"", []string{"holdout", "1"}, false}, want{1, "", "invalid holdout", ""}},
		{"a holdout that is no number is refused", args{"", []string{"holdout", "some"}, false}, want{1, "", "invalid holdout", ""}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home := homeDir(t.TempDir())
			require.NoError(t, os.MkdirAll(home.dir(), 0o755))
			if tc.args.saved != "" {
				require.NoError(t, os.WriteFile(home.configPath(), []byte(tc.args.saved), 0o600))
			}
			env := map[string]string{"HOME": string(home)}
			if tc.args.homeless {
				env = nil
			}
			var stdout, stderr bytes.Buffer

			code := runConfig(tc.args.args, func(k string) string { return env[k] }, &stdout, &stderr)

			assert.Equal(t, tc.want.code, code)
			assert.Equal(t, tc.want.stdout, stdout.String())
			assert.Contains(t, stderr.String(), tc.want.stderr)
			saved, _ := os.ReadFile(home.configPath())
			assert.Equal(t, tc.want.saved, string(saved))
		})
	}
}
