package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/diagnose"
)

func TestRunWritesTheCommittedSkill(t *testing.T) {
	t.Parallel()
	out := filepath.Join(t.TempDir(), "review", "SKILL.md")
	var stderr bytes.Buffer

	code := run([]string{"-out", out}, &stderr)
	got, err := os.ReadFile(out)
	require.NoError(t, err)
	committed, err := os.ReadFile("../../../plugin/skills/review/SKILL.md")
	require.NoError(t, err)

	assert.Equal(t, 0, code)
	assert.Empty(t, stderr.String())
	assert.Contains(t, string(got), diagnose.Rules)
	for _, tool := range []string{"`observe`", "`context`", "`select`", "`record`", "`feedback`"} {
		assert.Contains(t, string(got), tool)
	}
	assert.Equal(
		t, string(got), string(committed), "plugin/skills/review/SKILL.md is stale. Run go generate ./internal/diagnose/",
	)
}

func TestRun(t *testing.T) {
	const usage = "Usage of gen:\n  -out string\n    \toutput path (default \"SKILL.md\")\n"
	type args struct {
		// Output path under the test root passed as the value of out ahead of the flags
		out   string
		flags []string
	}
	type want struct {
		code int
		// The test root reads as <root>
		stderr string
		// What a stat of the output path returns after the run
		stat error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			name: "writes to the path named by out",
			args: args{out: filepath.Join("review", "SKILL.md")},
		},
		{
			name: "refuses an unknown flag with usage",
			args: args{out: filepath.Join("review", "SKILL.md"), flags: []string{"-in", "SKILL.md"}},
			want: want{code: 2, stderr: "flag provided but not defined: -in\n" + usage, stat: os.ErrNotExist},
		},
		{
			name: "refuses out without a path",
			args: args{out: filepath.Join("review", "SKILL.md"), flags: []string{"-out"}},
			want: want{code: 2, stderr: "flag needs an argument: -out\n" + usage, stat: os.ErrNotExist},
		},
		{
			name: "reports a failed write and exits 1",
			args: args{out: filepath.Join("file", "SKILL.md")},
			want: want{code: 1, stderr: "gen: mkdir <root>/file: not a directory\n", stat: syscall.ENOTDIR},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(root, "file"), nil, 0o644))
			out := filepath.Join(root, tc.args.out)
			var stderr bytes.Buffer

			code := run(append([]string{"-out", out}, tc.args.flags...), &stderr)
			_, err := os.Stat(out)

			assert.Equal(t, tc.want.code, code)
			assert.Equal(t, tc.want.stderr, strings.ReplaceAll(stderr.String(), root, "<root>"))
			assert.ErrorIs(t, err, tc.want.stat)
		})
	}
}

func TestWrite(t *testing.T) {
	tcs := []struct {
		name string
		args string
		want error
	}{
		{"creates the missing directories", filepath.Join("review", "SKILL.md"), nil},
		{"fails when a parent is a file", filepath.Join("file", "review", "SKILL.md"), syscall.ENOTDIR},
		{"fails when the output is a directory", "dir", syscall.EISDIR},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(root, "file"), nil, 0o644))
			require.NoError(t, os.Mkdir(filepath.Join(root, "dir"), 0o755))

			assert.ErrorIs(t, write(filepath.Join(root, tc.args)), tc.want)
		})
	}
}
