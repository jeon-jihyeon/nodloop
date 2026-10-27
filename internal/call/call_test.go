package call_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jeon-jihyeon/nodloop/internal/call"
)

func TestOf(t *testing.T) {
	type args struct {
		tool  string
		input map[string]any
	}
	tcs := []struct {
		name string
		args args
		want call.Shapes
	}{
		{
			"a commit keeps its words and drops the message",
			args{"Bash", map[string]any{"command": `git commit -m "fix the parser"`}},
			call.Shapes{{Tool: "Bash", Command: "git commit"}},
		},
		{
			"a flag and its value are skipped",
			args{"Bash", map[string]any{"command": "git -C /work/o/svc commit --amend"}},
			call.Shapes{{Tool: "Bash", Command: "git commit"}},
		},
		{
			"a flag holding its value skips only itself",
			args{"Bash", map[string]any{"command": "gh --repo=o/svc pr create --title x"}},
			call.Shapes{{Tool: "Bash", Command: "gh pr create"}},
		},
		{
			"a boolean flag ends the head so its next value is never read as a subcommand",
			args{"Bash", map[string]any{"command": "git --no-pager show acme-branch"}},
			call.Shapes{{Tool: "Bash", Command: "git"}},
		},
		{
			"a value flag of another program ends the head too",
			args{"Bash", map[string]any{"command": "helm --debug install acme-release"}},
			call.Shapes{{Tool: "Bash", Command: "helm"}},
		},
		{
			"an argument after a listed subcommand is never kept",
			args{"Bash", map[string]any{"command": "gh api acme"}},
			call.Shapes{{Tool: "Bash", Command: "gh api"}},
		},
		{
			"a flag of another program takes no value here",
			args{"Bash", map[string]any{"command": "uv -n run acme"}},
			call.Shapes{{Tool: "Bash", Command: "uv"}},
		},
		{
			"a word the program never defines is no subcommand",
			args{"Bash", map[string]any{"command": "kubectl acme"}},
			call.Shapes{{Tool: "Bash", Command: "kubectl"}},
		},
		{
			"a script runner keeps no script name",
			args{"Bash", map[string]any{"command": "yarn deploy-acme"}},
			call.Shapes{{Tool: "Bash", Command: "yarn"}},
		},
		{
			"a numeric ext keeps no file",
			args{"Write", map[string]any{"file_path": "notes.20261026"}},
			call.Shapes{{Tool: "Write"}},
		},
		{
			"three words at most",
			args{"Bash", map[string]any{"command": "gh pr create draft now"}},
			call.Shapes{{Tool: "Bash", Command: "gh pr create"}},
		},
		{
			"a word with a digit ends the head",
			args{"Bash", map[string]any{"command": "git checkout ann-136 now"}},
			call.Shapes{{Tool: "Bash", Command: "git checkout"}},
		},
		{
			"a path ends the head",
			args{"Bash", map[string]any{"command": "go test ./... -race"}},
			call.Shapes{{Tool: "Bash", Command: "go test"}},
		},
		{
			"an unlisted program keeps no word so its argument never lands",
			args{"Bash", map[string]any{"command": "echo acme"}},
			call.Shapes{{Tool: "Bash"}},
		},
		{
			"a quoted argument of an unlisted program keeps nothing",
			args{"Bash", map[string]any{"command": `echo 'acme'`}},
			call.Shapes{{Tool: "Bash"}},
		},
		{
			"a program with no subcommand keeps its name alone",
			args{"Bash", map[string]any{"command": "make deploy"}},
			call.Shapes{{Tool: "Bash", Command: "make"}},
		},
		{
			"a program by its path keeps its base name",
			args{"Bash", map[string]any{"command": "/usr/bin/git status"}},
			call.Shapes{{Tool: "Bash", Command: "git status"}},
		},
		{
			"an uppercase subcommand ends the head",
			args{"Bash", map[string]any{"command": "git Secret value"}},
			call.Shapes{{Tool: "Bash", Command: "git"}},
		},
		{
			"a quoted subcommand ends the head",
			args{"Bash", map[string]any{"command": `git 'two words' more`}},
			call.Shapes{{Tool: "Bash", Command: "git"}},
		},
		{
			"every command line gets a shape and scope lines none",
			args{"Bash", map[string]any{"command": `cd "$(git rev-parse --show-toplevel)" && git status | grep x`}},
			call.Shapes{
				{Tool: "Bash", Command: "git rev-parse"},
				{Tool: "Bash"},
				{Tool: "Bash", Command: "git status"},
			},
		},
		{
			"a repeated line shapes once",
			args{"Bash", map[string]any{"command": "git status; git status"}},
			call.Shapes{{Tool: "Bash", Command: "git status"}},
		},
		{
			"a line that starts with a value keeps no words",
			args{"Bash", map[string]any{"command": "./run.sh"}},
			call.Shapes{{Tool: "Bash"}},
		},
		{
			"a go test file",
			args{"Edit", map[string]any{"file_path": "/work/o/svc/parse_test.go"}},
			call.Shapes{{Tool: "Edit", File: "*_test.go"}},
		},
		{
			"a python test file",
			args{"Write", map[string]any{"file_path": "tests/test_parse.py"}},
			call.Shapes{{Tool: "Write", File: "test_*.py"}},
		},
		{
			"a test and a spec file",
			args{"Edit", map[string]any{"file_path": "src/parse.test.ts"}},
			call.Shapes{{Tool: "Edit", File: "*.test.ts"}},
		},
		{
			"a spec file",
			args{"Edit", map[string]any{"file_path": "src/parse.spec.ts"}},
			call.Shapes{{Tool: "Edit", File: "*.spec.ts"}},
		},
		{
			"a plain file keeps its ext lowercased",
			args{"Write", map[string]any{"file_path": `C:\work\README.MD`}},
			call.Shapes{{Tool: "Write", File: "*.md"}},
		},
		{
			"a notebook",
			args{"NotebookEdit", map[string]any{"notebook_path": "a/b.ipynb"}},
			call.Shapes{{Tool: "NotebookEdit", File: "*.ipynb"}},
		},
		{"no ext keeps no file", args{"Write", map[string]any{"file_path": "Makefile"}}, call.Shapes{{Tool: "Write"}}},
		{"a dot file keeps no file", args{"Write", map[string]any{"file_path": ".env"}}, call.Shapes{{Tool: "Write"}}},
		{
			"an ext past the grammar keeps no file",
			args{"Write", map[string]any{"file_path": "a.verylongext"}},
			call.Shapes{{Tool: "Write"}},
		},
		{
			"a field that is no string keeps no file",
			args{"Write", map[string]any{"file_path": 7}},
			call.Shapes{{Tool: "Write"}},
		},
		{"a tool with no input", args{"mcp__acme__send", nil}, call.Shapes{{Tool: "mcp__acme__send"}}},
		{"a tool name past the grammar shapes nothing", args{"bad tool", map[string]any{}}, nil},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, call.Of(tc.args.tool, tc.args.input))
		})
	}
}

func TestGrammar(t *testing.T) {
	tcs := []struct {
		name string
		args string
		want [2]bool
	}{
		{"a program", "git", [2]bool{true, false}},
		{"a program and its subcommands", "gh pr create", [2]bool{true, false}},
		{"more subcommands than the program lists", "git commit now", [2]bool{false, false}},
		{"an unlisted program", "echo", [2]bool{false, false}},
		{"an inner hyphen", "git rev-parse", [2]bool{true, false}},
		{"a trailing hyphen", "git rev-", [2]bool{false, false}},
		{"a double space", "git  commit", [2]bool{false, false}},
		{"a trailing space", "git ", [2]bool{false, false}},
		{"empty", "", [2]bool{false, false}},
		{"a test shape", "*_test.go", [2]bool{false, true}},
		{"a plain shape", "*.md", [2]bool{false, true}},
		{"a name is no shape", "CHANGELOG.md", [2]bool{false, false}},
		{"an uppercase ext", "*.MD", [2]bool{false, false}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, [2]bool{call.Command(tc.args), call.File(tc.args)})
		})
	}
}

func TestShapeFits(t *testing.T) {
	s := call.Shape{Tool: "Bash", Command: "gh pr create", File: "*_test.go"}
	assert.True(t, s.Starts("gh pr"))
	assert.True(t, s.Starts("gh pr create"))
	assert.False(t, s.Starts("gh p"))
	assert.False(t, s.Starts("gh pr create now"))
	assert.True(t, s.Matches("*.go"))
	assert.True(t, s.Matches("*_test.go"))
	assert.False(t, s.Matches("*.md"))
	assert.False(t, call.Shape{Tool: "Edit"}.Matches("*.go"))
	assert.True(t, call.Tool("mcp__acme__send"))
	assert.False(t, call.Tool("9tool"))
}
