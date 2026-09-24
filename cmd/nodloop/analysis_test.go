package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/testkit"
)

func TestRunAnalysis(t *testing.T) {
	brokenPolicy := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(brokenPolicy, "policy.yaml"), []byte("rules: ["), 0o600))
	data := testkit.DemoDir(t)
	getenv := func(k string) string {
		return map[string]string{envFileDir: data}[k]
	}
	at := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	type want struct {
		code int
		// Regexp matched against stdout
		stdout string
		// Regexp matched against stderr
		stderr string
	}
	tcs := []struct {
		name string
		args []string
		want want
	}{
		{"policy prints the demo policy", []string{"policy"}, want{0, `(?s)^{\n  "version": "demo-1",.*\n}\n$`, `^$`}},
		{
			"policy defaults without a policy file",
			[]string{"policy", "--data-dir", t.TempDir()},
			want{0, `^{\n  "version": "demo-1",`, `^$`},
		},
		{
			"policy does not open the data dir",
			[]string{"policy", "--data-dir", "/nonexistent"},
			want{0, `^{\n  "version": "demo-1",`, `^$`},
		},
		{
			"broken policy fails",
			[]string{"policy", "--data-dir", brokenPolicy},
			want{1, `^$`, `^nodloop analysis: analysis: policy is not valid yaml: `},
		},
		{
			"observe prints a spike",
			[]string{"observe", "--event", "tq-005"},
			want{0, "^zscore\tclick_count\tseverity=", `^$`},
		},
		{"observe of normal variation prints nothing", []string{"observe", "--event", "tq-001"}, want{0, `^$`, `^$`}},
		{
			"observe of a hold names the coverage rule",
			[]string{"observe", "--event", "tq-017"},
			want{0, "coverage_rule", `^$`},
		},
		{
			"observe of an unknown event fails",
			[]string{"observe", "--event", "nope"},
			want{1, `^$`, `^nodloop analysis: event "nope": .*not found\n$`},
		},
		{
			"observe without the data dir fails",
			[]string{"observe", "--event", "tq-001", "--data-dir", "/nonexistent"},
			want{1, `^$`, `^nodloop analysis: evidence file source: stat /nonexistent: no such file or directory\n$`},
		},
		{
			"observe without an event fails",
			[]string{"observe"},
			want{1, `^$`, `^nodloop analysis: --event is required\n\nusage:`},
		},
		{
			"unknown source fails",
			[]string{"policy", "--source", "postgres"},
			want{1, `^$`, `^nodloop analysis: unknown source: "postgres"\n$`},
		},
		{"unknown flag fails", []string{"policy", "--nope"}, want{1, `^$`, `^flag provided but not defined: -nope\n`}},
		{
			"unknown action fails",
			[]string{"bogus"},
			want{1, `^$`, `^nodloop analysis: unknown action "bogus"\n\nusage:`},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer

			got := runAnalysis(tc.args, getenv, func() time.Time { return at }, &stdout, &stderr)

			assert.Equal(t, tc.want.code, got)
			assert.Regexp(t, tc.want.stdout, stdout.String())
			assert.Regexp(t, tc.want.stderr, stderr.String())
		})
	}
}
