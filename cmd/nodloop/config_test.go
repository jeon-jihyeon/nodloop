package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/testkit"
)

func TestResolveConfig(t *testing.T) {
	demo := testkit.DemoDir(t)
	h := homeDir(t.TempDir())
	records := filepath.Join(t.TempDir(), "records")
	uc, err := newUserConfig(demo, records)
	require.NoError(t, err)
	require.NoError(t, h.save(uc))
	dataOnly := homeDir(t.TempDir())
	uc, err = newUserConfig(demo, "")
	require.NoError(t, err)
	require.NoError(t, dataOnly.save(uc))
	bare := homeDir(t.TempDir())
	broken := homeDir(t.TempDir())
	require.NoError(t, os.MkdirAll(broken.dir(), 0o755))
	require.NoError(t, os.WriteFile(broken.configPath(), []byte("{broken"), 0o600))
	env := map[string]string{envFileDir: "/data", envRecordDir: "/records"}
	relative := homeDir(t.TempDir())
	require.NoError(t, os.MkdirAll(relative.dir(), 0o755))
	require.NoError(t, os.WriteFile(relative.configPath(), []byte(`{"record_dir":"records"}`), 0o600))
	wd, err := os.Getwd()
	require.NoError(t, err)
	type args struct {
		env       map[string]string
		source    string
		dataDir   string
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
		{"env names both directories", args{env: env}, want{config{dataDir: "/data", recordDir: "/records"}, nil}},
		{
			"flags win over the env",
			args{env, string(sourceFile), "/flag-data", "/flag-records"},
			want{config{dataDir: "/flag-data", recordDir: "/flag-records"}, nil},
		},
		{
			"setup config fills unset values",
			args{env: map[string]string{"HOME": string(h)}},
			want{config{dataDir: demo, recordDir: records, home: h}, nil},
		},
		{
			"env wins over the setup config",
			args{env: map[string]string{"HOME": string(h), envFileDir: "/elsewhere", envRecordDir: "/records"}},
			want{config{dataDir: "/elsewhere", recordDir: "/records", home: h}, nil},
		},
		{
			"setup config without a record dir defaults records under home",
			args{env: map[string]string{"HOME": string(dataOnly)}},
			want{config{dataDir: demo, recordDir: dataOnly.recordDir(), home: dataOnly}, nil},
		},
		{
			"home without setup config defaults records under home",
			args{env: map[string]string{"HOME": string(bare), envFileDir: "/data"}},
			want{config{dataDir: "/data", recordDir: bare.recordDir(), home: bare}, nil},
		},
		{
			"records without home or record dir stay unset",
			args{env: map[string]string{envFileDir: "/data"}},
			want{config{dataDir: "/data"}, nil},
		},
		{
			"broken setup config fails",
			args{env: map[string]string{"HOME": string(broken), envFileDir: "/data"}},
			want{config{}, errConfigInvalid},
		},
		{"missing data dir fails", args{}, want{config{}, errDataDirUnset}},
		{
			"unknown env source fails",
			args{env: map[string]string{envSource: "postgres", envFileDir: "/data", envRecordDir: "/records"}},
			want{config{}, errUnknownSource},
		},
		{"unknown flag source fails", args{env: env, source: "postgres"}, want{config{}, errUnknownSource}},
		{
			"a relative flag resolves against the working directory",
			args{env: env, recordDir: "rec"},
			want{config{dataDir: "/data", recordDir: filepath.Join(wd, "rec")}, nil},
		},
		{
			"a flag with dot segments is cleaned",
			args{env: env, recordDir: "/a/./b/../rec/"},
			want{config{dataDir: "/data", recordDir: "/a/rec"}, nil},
		},
		{
			"an absolute variable with a trailing slash is cleaned",
			args{env: map[string]string{envFileDir: "/data", envRecordDir: "/records/"}},
			want{config{dataDir: "/data", recordDir: "/records"}, nil},
		},
		{
			"a relative variable fails because a server started elsewhere would read other records",
			args{env: map[string]string{envFileDir: "/data", envRecordDir: "records"}},
			want{config{}, errRecordDirRelative},
		},
		{
			"a relative record dir in the setup config fails",
			args{env: map[string]string{"HOME": string(relative), envFileDir: "/data"}},
			want{config{}, errRecordDirRelative},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			getenv := func(k string) string { return tc.args.env[k] }

			got, err := resolveConfig(getenv, tc.args.source, tc.args.dataDir, tc.args.recordDir)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.cfg, got)
		})
	}
}
