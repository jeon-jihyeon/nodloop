package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/llm"
)

func TestClaudeCLI(t *testing.T) {
	tcs := []struct {
		name string
		args map[string]string
		want *llm.ClaudeCLI
	}{
		{
			"env names the binary and the default model",
			map[string]string{envClaudeBin: "/opt/claude", envLLMModel: "opus"},
			llm.NewClaudeCLI("/opt/claude", "opus", "", 0),
		},
		{"empty env leaves the llm defaults", nil, llm.NewClaudeCLI("", "", "", 0)},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, claudeCLI(func(k string) string { return tc.args[k] }))
		})
	}
}

func TestRun(t *testing.T) {
	plain, err := os.ReadFile("testdata/bash_plain.json")
	require.NoError(t, err)
	type args struct {
		args  []string
		stdin []byte
	}
	type want struct {
		code   int
		stdout string
		// Regexp matched against stderr
		stderr string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"no arguments print the usage", args{nil, nil}, want{1, "", `^usage: nodloop <command>\n`}},
		{
			"unknown command fails",
			args{[]string{"bogus"}, nil},
			want{1, "", `^nodloop: unknown command "bogus"\n\nusage:`},
		},
		{"version prints the build version", args{[]string{"version"}, nil}, want{0, "dev\n", `^$`}},
		{"guard runs the hook", args{[]string{"guard"}, plain}, want{0, "", `^$`}},
		{
			"llm without an action fails",
			args{[]string{"llm"}, nil},
			want{1, "", `^nodloop llm: an action is required\n\nusage:`},
		},
		{
			"llm unknown action fails",
			args{[]string{"llm", "bogus"}, nil},
			want{1, "", `^nodloop llm: unknown action "bogus"\n\nusage:`},
		},
		{
			"llm probe fails on an unknown flag",
			args{[]string{"llm", "probe", "--nope"}, nil},
			want{1, "", `^flag provided but not defined: -nope\n`},
		},
		{
			"setup without a data dir fails",
			args{[]string{"setup"}, nil},
			want{1, "", `^nodloop setup: --data-dir or --demo is required\n\nusage:`},
		},
		{
			"evidence without an action fails",
			args{[]string{"evidence"}, nil},
			want{1, "", `^nodloop evidence: an action is required\n\nusage:`},
		},
		{
			"analysis without an action fails",
			args{[]string{"analysis"}, nil},
			want{1, "", `^nodloop analysis: an action is required\n\nusage:`},
		},
		{
			"trace without an action fails",
			args{[]string{"trace"}, nil},
			want{1, "", `^nodloop trace: an action is required\n\nusage:`},
		},
		{
			"feedback without an action fails",
			args{[]string{"feedback"}, nil},
			want{1, "", `^nodloop feedback: an action is required\n\nusage:`},
		},
		{
			"knowledge without an action fails",
			args{[]string{"knowledge"}, nil},
			want{1, "", `^nodloop knowledge: an action is required\n\nusage:`},
		},
		{
			"diagnose without an event fails",
			args{[]string{"diagnose"}, nil},
			want{1, "", `^nodloop diagnose: --event is required\n\nusage:`},
		},
		{
			"mcp fails on an unknown flag",
			args{[]string{"mcp", "--nope"}, nil},
			want{1, "", `^flag provided but not defined: -nope\n`},
		},
		{
			"eval without an action fails",
			args{[]string{"eval"}, nil},
			want{1, "", `^nodloop eval: an action is required\n\nusage:`},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			getenv := func(k string) string { return map[string]string{"HOME": home}[k] }
			stdout, err := os.Create(filepath.Join(t.TempDir(), "stdout"))
			require.NoError(t, err)
			var stderr bytes.Buffer

			got := run(tc.args.args, getenv, bytes.NewReader(tc.args.stdin), stdout, &stderr)

			assert.Equal(t, tc.want.code, got)
			out, err := os.ReadFile(stdout.Name())
			require.NoError(t, err)
			assert.Equal(t, tc.want.stdout, string(out))
			assert.Regexp(t, tc.want.stderr, stderr.String())
		})
	}
}
