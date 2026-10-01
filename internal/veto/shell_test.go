package veto_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/veto"
)

func TestCommandLines(t *testing.T) {
	tcs := []struct {
		name string
		args string
		want string
	}{
		{"quotes and escapes are removed", `\sed -i 's/a/b/' "f"`, "sed -i s/a/b/ f"},
		{"a line continuation joins the words", "sed \\\n -i x f", "sed -i x f"},
		{"leading assignments and redirections are left out", "FOO=1 sed -i x f > out 2>&1", "sed -i x f"},
		{"a word holding a space comes back quoted", `sed -e "s/ -i /x/" f`, "sed -e 's/ -i /x/' f"},
		{"an empty word comes back quoted", "sed -i '' x", "sed -i '' x"},
		{"a backslash inside double quotes stays before a plain character", `grep "a\|b" f`, `grep a\|b f`},
		{"an expansion renders as a dollar sign", `sed -i"$sfx" "$f"`, "sed -i$ $"},
		{"every command of a list and a pipe gets a line", "ls | xargs sed -i x; cd a && git log", "ls\nxargs sed -i x\ncd a\ngit log"},
		{"commands in conditions and loop bodies get lines", "if ! eval x; then for f in *; do rm $f; done; fi", "eval x\nrm $"},
		{
			"a substitution opens and closes a scope before the command that holds it",
			`cd "$(git rev-parse --show-toplevel)" && git status`,
			"(\ngit rev-parse --show-toplevel\n)\ncd $\ngit status",
		},
		{"a subshell opens and closes a scope", "(cd x && make); git status", "(\ncd x\nmake\n)\ngit status"},
		{"a process substitution opens a scope", "diff <(sort a) b", "(\nsort a\n)\ndiff $ b"},
		{"a heredoc body is no command", "python3 - <<'EOF'\nprint('x')\neval\nEOF", "python3 -"},
		{
			"the command find runs is a line of its own up to its terminator",
			`find . -name '*.go' -exec sed -n 1p {} \; -iname x -execdir rm {} +`,
			"find . -name *.go -exec sed -n 1p {} ; -iname x -execdir rm {} +\nsed -n 1p {}\nrm {}",
		},
		{"an assignment alone gets no line", "V=$(pwd)", "(\npwd\n)"},
		{"zsh syntax parses after bash fails", "echo =(ls)", "(\nls\n)\necho $"},
		{"text that does not parse comes back as written", "sed -i 's/a/b f", "sed -i 's/a/b f"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, veto.Command(tc.args).Lines())
		})
	}
}

func TestBlocksDerivesCommands(t *testing.T) {
	condition, err := veto.NewCondition(veto.FieldCommands, "(?m)^sed -i", "")
	require.NoError(t, err)
	v, err := veto.New("no-sed", "Bash|Monitor", []veto.Condition{condition}, "r", veto.ActionBlock, true)
	require.NoError(t, err)
	type args struct {
		tool  string
		input map[string]any
	}
	tcs := []struct {
		name string
		args args
		want bool
	}{
		{"a command is parsed into commands", args{"Bash", map[string]any{"command": "ls && sed -i x f"}}, true},
		{"a Monitor command is parsed the same way", args{"Monitor", map[string]any{"command": "tail -f l | sed -i x f"}}, true},
		{"quoted text is no command", args{"Bash", map[string]any{"command": "echo 'a; sed -i b'"}}, false},
		{"an input with its own commands keeps them", args{"Bash", map[string]any{"command": "sed -i x", "commands": "ls"}}, false},
		{"a non string command derives nothing", args{"Bash", map[string]any{"command": 42}}, false},
		{"an input without a command derives nothing", args{"Monitor", map[string]any{"ws": "wss://x"}}, false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			input := map[string]any{}
			for k, val := range tc.args.input {
				input[k] = val
			}
			assert.Equal(t, tc.want, v.Matches(tc.args.tool, tc.args.input))
			assert.Equal(t, tc.want, veto.Vetoes{v}.Match(tc.args.tool, tc.args.input) != nil)
			assert.Equal(t, input, tc.args.input, "the caller input stays as it was")
		})
	}
}
