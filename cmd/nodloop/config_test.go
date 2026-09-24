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
	_, err := h.setup(demo, records)
	require.NoError(t, err)
	dataOnly := homeDir(t.TempDir())
	_, err = dataOnly.setup(demo, "")
	require.NoError(t, err)
	bare := homeDir(t.TempDir())
	broken := homeDir(t.TempDir())
	require.NoError(t, os.MkdirAll(broken.dir(), 0o755))
	require.NoError(t, os.WriteFile(broken.configPath(), []byte("{broken"), 0o600))
	env := map[string]string{envFileDir: "/data", envRecordDir: "/records"}
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
		{"env names both directories", args{env: env}, want{config{"/data", "/records"}, nil}},
		{
			"flags win over the env",
			args{env, string(sourceFile), "/flag-data", "/flag-records"},
			want{config{"/flag-data", "/flag-records"}, nil},
		},
		{
			"setup config fills unset values",
			args{env: map[string]string{"HOME": string(h)}},
			want{config{demo, records}, nil},
		},
		{
			"env wins over the setup config",
			args{env: map[string]string{"HOME": string(h), envFileDir: "/elsewhere", envRecordDir: "/records"}},
			want{config{"/elsewhere", "/records"}, nil},
		},
		{
			"setup config without a record dir defaults records under home",
			args{env: map[string]string{"HOME": string(dataOnly)}},
			want{config{demo, dataOnly.recordDir()}, nil},
		},
		{
			"home without setup config defaults records under home",
			args{env: map[string]string{"HOME": string(bare), envFileDir: "/data"}},
			want{config{"/data", bare.recordDir()}, nil},
		},
		{
			"records without home or record dir stay unset",
			args{env: map[string]string{envFileDir: "/data"}},
			want{config{"/data", ""}, nil},
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
