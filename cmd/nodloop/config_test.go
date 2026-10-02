package main

import (
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
