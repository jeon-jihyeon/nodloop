package main

import (
	"bytes"
	"os"
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
			"demo unpacks the data set and points the config at it",
			args{[]string{"--demo"}, "{home}"},
			want{
				0, "data {home}/.nodloop/demo\nconfig {home}/.nodloop/config.json\n" +
					"demo events tq-001 to tq-024. Try: review event tq-023\n",
				`^$`, config{"{home}/.nodloop/demo", "{home}/.nodloop/records"}, nil,
			},
		},
		{
			"data dir and record dir are stored absolute",
			args{[]string{"--data-dir", "../../internal/evidence/file/testdata", "--record-dir", "records"}, "{home}"},
			want{0, "data {demo}\nconfig {home}/.nodloop/config.json\n", `^$`, config{"{demo}", "{cwd}/records"}, nil},
		},
		{
			"data dir without events fails",
			args{[]string{"--data-dir", "{empty}"}, "{home}"},
			want{1, "", "^nodloop setup: no events.csv: {empty}\n$", config{}, errDataDirUnset},
		},
		{
			"no flags fail",
			args{nil, "{home}"},
			want{1, "", "^nodloop setup: --data-dir or --demo is required\n\nusage:", config{}, errDataDirUnset},
		},
		{
			"unknown home fails",
			args{[]string{"--demo"}, ""},
			want{1, "", "^nodloop setup: home directory unknown: HOME is not set\n$", config{}, errDataDirUnset},
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
			r := strings.NewReplacer("{home}", t.TempDir(), "{empty}", t.TempDir(), "{demo}", testkit.DemoDir(t), "{cwd}", cwd)
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
			assert.Equal(t, config{r.Replace(tc.want.cfg.dataDir), r.Replace(tc.want.cfg.recordDir)}, cfg)
		})
	}
}
