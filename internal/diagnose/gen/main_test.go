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

// The skill and the prompt as the generator writes them equal the committed files
// The prompt keeps no part only Claude Code has
func TestRunWritesTheCommittedFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	out, prompt := filepath.Join(root, "review", "SKILL.md"), filepath.Join(root, "mcp", "review.md")
	var stderr bytes.Buffer

	code := run([]string{"-out", out, "-prompt", prompt}, &stderr)

	require.Equal(t, 0, code)
	assert.Empty(t, stderr.String())
	tcs := []struct {
		name      string
		written   string
		committed string
		// Text only the file of its host holds
		holds []string
		// Text the file must not hold
		lacks []string
	}{
		{
			"the skill", out, "../../../plugin/skills/review/SKILL.md",
			[]string{"run `python3 <data dir>/convert.py` through Bash without asking", "`nodloop:setup` skill", "AskUserQuestion"},
			nil,
		},
		{
			"the prompt", prompt, "../../mcp/review.md",
			[]string{"run `python3 <data dir>/convert.py` in a shell without asking", "`nodloop setup --data-dir <dir>`"},
			[]string{"CLAUDE_PLUGIN_ROOT", "~/.nodloop/bin", "AskUserQuestion", "nodloop:setup", "Bash", "name: review"},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			got, err := os.ReadFile(tc.written)
			require.NoError(t, err)
			committed, err := os.ReadFile(tc.committed)
			require.NoError(t, err)
			assert.Contains(t, string(got), diagnose.Rules)
			for _, tool := range []string{"`observe`", "`context`", "`select`", "`record`", "`feedback`"} {
				assert.Contains(t, string(got), tool)
			}
			for _, text := range tc.holds {
				assert.Contains(t, string(got), text)
			}
			for _, text := range tc.lacks {
				assert.NotContains(t, string(got), text)
			}
			assert.Equal(t, string(got), string(committed), tc.committed+" is stale. Run go generate ./internal/diagnose/")
		})
	}
}

func TestRun(t *testing.T) {
	const usage = "Usage of gen:\n  -out string\n    \tskill output path (default \"SKILL.md\")\n" +
		"  -prompt string\n    \tMCP prompt output path (default \"review.md\")\n"
	type args struct {
		// Output paths under the test root passed as the values of out and prompt ahead of the flags
		out, prompt string
		flags       []string
	}
	type want struct {
		code int
		// The test root reads as <root>
		stderr string
		// What a stat of each output path returns after the run
		stats [2]error
	}
	skill, prompt := filepath.Join("review", "SKILL.md"), filepath.Join("mcp", "review.md")
	tcs := []struct {
		name string
		args args
		want want
	}{
		{name: "writes to the paths named by out and prompt", args: args{out: skill, prompt: prompt}},
		{
			name: "refuses an unknown flag with usage",
			args: args{out: skill, prompt: prompt, flags: []string{"-in", "SKILL.md"}},
			want: want{code: 2, stderr: "flag provided but not defined: -in\n" + usage, stats: [2]error{os.ErrNotExist, os.ErrNotExist}},
		},
		{
			name: "refuses out without a path",
			args: args{out: skill, prompt: prompt, flags: []string{"-out"}},
			want: want{code: 2, stderr: "flag needs an argument: -out\n" + usage, stats: [2]error{os.ErrNotExist, os.ErrNotExist}},
		},
		{
			name: "reports a failed skill write and writes no prompt",
			args: args{out: filepath.Join("file", "SKILL.md"), prompt: prompt},
			want: want{code: 1, stderr: "gen: mkdir <root>/file: not a directory\n", stats: [2]error{syscall.ENOTDIR, os.ErrNotExist}},
		},
		{
			name: "reports a failed prompt write after the skill",
			args: args{out: skill, prompt: filepath.Join("file", "review.md")},
			want: want{code: 1, stderr: "gen: mkdir <root>/file: not a directory\n", stats: [2]error{nil, syscall.ENOTDIR}},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(root, "file"), nil, 0o644))
			out, prompt := filepath.Join(root, tc.args.out), filepath.Join(root, tc.args.prompt)
			var stderr bytes.Buffer

			code := run(append([]string{"-out", out, "-prompt", prompt}, tc.args.flags...), &stderr)
			_, outErr := os.Stat(out)
			_, promptErr := os.Stat(prompt)

			assert.Equal(t, tc.want.code, code)
			assert.Equal(t, tc.want.stderr, strings.ReplaceAll(stderr.String(), root, "<root>"))
			assert.ErrorIs(t, outErr, tc.want.stats[0])
			assert.ErrorIs(t, promptErr, tc.want.stats[1])
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

			assert.ErrorIs(t, write(filepath.Join(root, tc.args), "text"), tc.want)
		})
	}
}
