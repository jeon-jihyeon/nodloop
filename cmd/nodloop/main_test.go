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
			want{1, "", `^flag provided but not defined: -nope\nusage:\n  llm probe`},
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
			"run without an action fails",
			args{[]string{"run"}, nil},
			want{1, "", `^nodloop run: an action is required\n\nusage:`},
		},
		{
			"removed data commands are unknown",
			args{[]string{"diagnose"}, nil},
			want{1, "", `^nodloop: unknown command "diagnose"\n\nusage:`},
		},
		{
			"mcp fails on an unknown flag",
			args{[]string{"mcp", "--nope"}, nil},
			want{1, "", `^flag provided but not defined: -nope\nusage:\n  mcp `},
		},
		{"--help prints the usage", args{[]string{"--help"}, nil}, want{0, usage, `^$`}},
		{"help prints the usage", args{[]string{"help"}, nil}, want{0, usage, `^$`}},
		{"help of a command prints its part", args{[]string{"help", "queue"}, nil}, want{0, usageText(usage).of("queue"), `^$`}},
		{"-h after a command prints its part", args{[]string{"report", "-h"}, nil}, want{0, usageText(usage).of("report"), `^$`}},
		{"-h after an action prints the part of the action", args{[]string{"knowledge", "for", "-h"}, nil}, want{0, usageText(usage).of("knowledge for"), `^$`}},
		{"-h after an action without flags never becomes its value", args{[]string{"config", "approver", "-h"}, nil}, want{0, usageText(usage).of("config approver"), `^$`}},
		{"-h after an action that changes settings never runs it", args{[]string{"guard", "install", "-h"}, nil}, want{0, usageText(usage).of("guard install"), `^$`}},
		{"-h as the value of a flag runs the command", args{[]string{"feedback", "add", "--trace", "t1", "--verdict", "bogus", "--reason", "-h"}, nil},
			want{1, "", `^nodloop feedback: verdict must be`}},
		{
			"an error that starts with the command names it once",
			args{[]string{"feedback", "add", "--trace", "t1", "--verdict", "bogus"}, nil},
			want{1, "", `^nodloop feedback: verdict must be`},
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

func TestPluginVersionMismatch(t *testing.T) {
	type args struct {
		want pluginVersion
		have string
	}
	tcs := []struct {
		name string
		args args
		want string
	}{
		{"outside the plugin", args{have: "0.5.15"}, ""},
		{"the same version", args{want: "0.5.15", have: "0.5.15"}, ""},
		{"a leading v on the binary", args{want: "0.5.15", have: "v0.5.15"}, ""},
		{"a leading v on the plugin", args{want: "v0.5.15", have: "0.5.15"}, ""},
		{
			"another release",
			args{want: "0.5.15", have: "0.5.14"},
			"/bin/nodloop is nodloop 0.5.14 while the plugin runs version 0.5.15, so a tool or a flag the skills name may be missing or work differently. " +
				"Restart Claude Code with network access so the plugin fetches v0.5.15, or put a build of v0.5.15 on PATH",
		},
		{
			"a dev build",
			args{want: "0.5.15", have: "dev"},
			"/bin/nodloop is nodloop dev while the plugin runs version 0.5.15, so a tool or a flag the skills name may be missing or work differently. " +
				"Restart Claude Code with network access so the plugin fetches v0.5.15, or put a build of v0.5.15 on PATH",
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.want.mismatch(tc.args.have, "/bin/nodloop"))
		})
	}
}

func TestUsageTextOf(t *testing.T) {
	text := usageText("usage: x <command>\n\nintro\n\ncommands:\n  a one     First\n              more of a one\n  a two     Second\n  b         Third\n\nnotes\n")
	tcs := []struct {
		name string
		args string
		want string
	}{
		{"an action keeps its block", "a two", "usage:\n  a two     Second\n\nnotes\n"},
		{"a command keeps the blocks of every action", "a", "usage:\n  a one     First\n              more of a one\n  a two     Second\n\nnotes\n"},
		{"an unknown action falls back to its command", "a three", "usage:\n  a one     First\n              more of a one\n  a two     Second\n\nnotes\n"},
		{"a name that only starts like a command is not it", "bb", string(text)},
		{"an unknown command gets the whole text", "c", string(text)},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, text.of(tc.args))
		})
	}
}
