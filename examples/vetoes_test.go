package examples_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/veto"
)

func TestVetoes(t *testing.T) {
	b, err := os.ReadFile("vetoes.yaml")
	require.NoError(t, err)
	vetoes, err := veto.Parse(b)
	require.NoError(t, err)
	byID := map[string]veto.Veto{}
	for _, v := range vetoes {
		byID[v.ID()] = v
	}
	fields := map[string]string{"Bash": "command", "Write": "file_path"}
	type args struct {
		id    string
		tool  string
		value string
	}
	tcs := []struct {
		name string
		args args
		want bool
	}{
		{"sed in place edit is blocked", args{"no-sed-inplace", "Bash", "sed -i 's/a/b/' f"}, true},
		{"leading space before sed is blocked", args{"no-sed-inplace", "Bash", "  sed -i x f"}, true},
		{"sed after a newline is blocked", args{"no-sed-inplace", "Bash", "ls\nsed -i x f"}, true},
		{"sed in a subshell is blocked", args{"no-sed-inplace", "Bash", "(sed -i x f)"}, true},
		{"sed in command substitution is blocked", args{"no-sed-inplace", "Bash", "echo $(sed -i x f)"}, true},
		{"sed in backticks is blocked", args{"no-sed-inplace", "Bash", "echo `sed -i x f`"}, true},
		{"in place flag after another option is blocked", args{"no-sed-inplace", "Bash", "sed -e x -i f"}, true},
		{"sed through xargs is blocked", args{"no-sed-inplace", "Bash", "ls | xargs sed -i x"}, true},
		{"sed through xargs with options is blocked", args{"no-sed-inplace", "Bash", "xargs -n 1 sed -i x"}, true},
		{"sed by absolute path is blocked", args{"no-sed-inplace", "Bash", "/usr/bin/sed -i x f"}, true},
		{"long in place flag is blocked", args{"no-sed-inplace", "Bash", "sed --in-place=.bak x f"}, true},
		{"perl in place edit after a pipe is blocked", args{"no-sed-inplace", "Bash", "cat f | perl -pi -e x"}, true},
		{"sed without in place flag passes", args{"no-sed-inplace", "Bash", "sed -n 's/a/b/p' f"}, false},
		{"file name ending in i passes", args{"no-sed-inplace", "Bash", "sed -n p file-i"}, false},
		{"word containing sed passes", args{"no-sed-inplace", "Bash", "used -i x"}, false},
		{"eval is blocked", args{"no-eval", "Bash", "eval x"}, true},
		{"eval after a newline is blocked", args{"no-eval", "Bash", "ls\neval x"}, true},
		{"eval in a group is blocked", args{"no-eval", "Bash", "{ eval x; }"}, true},
		{"builtin eval is blocked", args{"no-eval", "Bash", "builtin eval x"}, true},
		{"word starting with eval passes", args{"no-eval", "Bash", "evaluate x"}, false},
		{"bash -c is blocked", args{"no-shell-c", "Bash", "bash -c 'ls'"}, true},
		{"combined shell flags are blocked", args{"no-shell-c", "Bash", "bash -lc 'ls'"}, true},
		{"shell flag after another option is blocked", args{"no-shell-c", "Bash", "sh -x -c 'ls'"}, true},
		{"shell -c through env is blocked", args{"no-shell-c", "Bash", "env FOO=1 zsh -c 'ls'"}, true},
		{"shell script passes", args{"no-shell-c", "Bash", "bash script"}, false},
		{"cd then git is blocked", args{"no-cd-then-git", "Bash", "cd repo && git status"}, true},
		{"cd with a quoted path then git is blocked", args{"no-cd-then-git", "Bash", `cd "my repo" && git status`}, true},
		{"cd then git after a semicolon is blocked", args{"no-cd-then-git", "Bash", "cd repo; git status"}, true},
		{"cd in a subshell then git is blocked", args{"no-cd-then-git", "Bash", "(cd repo && git status)"}, true},
		{"git with a directory flag passes", args{"no-cd-then-git", "Bash", "git -C repo status"}, false},
		{"cd then another command passes", args{"no-cd-then-git", "Bash", "cd repo && ls"}, false},
		{"readme write is blocked", args{"no-readme", "Write", "/repo/README.md"}, true},
		{"lower case readme write is blocked", args{"no-readme", "Write", "/repo/readme.md"}, true},
		{"readme under node_modules passes", args{"no-readme", "Write", "/repo/node_modules/x/README.md"}, false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Contains(t, byID, tc.args.id)
			got := byID[tc.args.id].Matches(tc.args.tool, map[string]any{fields[tc.args.tool]: tc.args.value})
			assert.Equal(t, tc.want, got)
		})
	}
}
