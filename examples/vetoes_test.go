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
		{"sed in a for loop body is blocked", args{"no-sed-inplace", "Bash", `for f in *.go; do sed -i '' 's/a/b/' "$f"; done`}, true},
		{"sed in an if body is blocked", args{"no-sed-inplace", "Bash", "if true; then sed -i '' s/a/b/ x; fi"}, true},
		{"sed in an else body is blocked", args{"no-sed-inplace", "Bash", "if false; then ls; else sed -i x f; fi"}, true},
		{"sed in a while body is blocked", args{"no-sed-inplace", "Bash", "while read f; do sed -i x \"$f\"; done < list"}, true},
		{"sed through find exec is blocked", args{"no-sed-inplace", "Bash", "find . -name '*.go' -exec sed -i '' s/a/b/ {} +"}, true},
		{"sed through timeout is blocked", args{"no-sed-inplace", "Bash", "timeout 5 sed -i x f"}, true},
		{"sed after an assignment is blocked", args{"no-sed-inplace", "Bash", "FOO=1 sed -i x f"}, true},
		{"sed after a locale assignment is blocked", args{"no-sed-inplace", "Bash", "LC_ALL=C sed -i '' 's/a/b/' f"}, true},
		{"backslashed sed is blocked", args{"no-sed-inplace", "Bash", `\sed -i x f`}, true},
		{"single quoted sed is blocked", args{"no-sed-inplace", "Bash", "'sed' -i x f"}, true},
		{"double quoted sed is blocked", args{"no-sed-inplace", "Bash", `"sed" -i x f`}, true},
		{"negated sed is blocked", args{"no-sed-inplace", "Bash", "! sed -i x f"}, true},
		{"quoted semicolon in the script is blocked", args{"no-sed-inplace", "Bash", "sed -e 's/a/b/;s/c/d/' -i f"}, true},
		{"line continuation before the flag is blocked", args{"no-sed-inplace", "Bash", "sed \\\n -i x f"}, true},
		{"flag before an unbalanced quote is blocked", args{"no-sed-inplace", "Bash", "sed -i 's/a/b f"}, true},
		{"sed through sudo with a long option is blocked", args{"no-sed-inplace", "Bash", "sudo --user root sed -i x f"}, true},
		{"sed through xargs with a long option is blocked", args{"no-sed-inplace", "Bash", "xargs --max-args 1 sed -i x"}, true},
		{"sed through exec with a name is blocked", args{"no-sed-inplace", "Bash", "exec -a foo sed -i x"}, true},
		{"sed through stacked wrappers is blocked", args{"no-sed-inplace", "Bash", "sudo timeout 5 nice -n 5 sed -i x f"}, true},
		{"perl slurp in place edit is blocked", args{"no-sed-inplace", "Bash", "perl -0pi -e s/a/b/ f"}, true},
		{"perl in place flag first is blocked", args{"no-sed-inplace", "Bash", "perl -i -pe s/a/b/ f"}, true},
		{"perl in place flag after the script is blocked", args{"no-sed-inplace", "Bash", "perl -pe x -i f"}, true},
		{"perl with a module option passes", args{"no-sed-inplace", "Bash", "perl -Mstrict -e 'print 1'"}, false},
		{"perl with a library path passes", args{"no-sed-inplace", "Bash", "perl -Ilib x.pl"}, false},
		{"quoted semicolon without the flag passes", args{"no-sed-inplace", "Bash", "sed -n 's/;/x/' f"}, false},
		{"flag inside a quoted script passes", args{"no-sed-inplace", "Bash", "sed -e 's/ -i/x/' f"}, false},
		{"grep for an in place flag passes", args{"no-sed-inplace", "Bash", "grep -e -i sed.txt"}, false},
		{"xargs placing a name passes", args{"no-sed-inplace", "Bash", "xargs -I{} echo {}"}, false},
		{"BSD append cluster before the flag is blocked", args{"no-sed-inplace", "Bash", "sed -ai '' s/a/b/ f"}, true},
		{"BSD in place flag in a cluster is blocked", args{"no-sed-inplace", "Bash", "sed -Ii s/a/b/ f"}, true},
		{"GNU binary cluster before the flag is blocked", args{"no-sed-inplace", "Bash", "sed -bi s/a/b/ f"}, true},
		{"BSD in place flag with an empty suffix is blocked", args{"no-sed-inplace", "Bash", "sed -I '' s/a/b/ f"}, true},
		{"BSD in place flag with a suffix is blocked", args{"no-sed-inplace", "Bash", "sed -I.bak s/a/b/ f"}, true},
		{"GNU in place flag with a suffix is blocked", args{"no-sed-inplace", "Bash", "sed -i.bak s/a/b/ f"}, true},
		{"script cluster ending in i is blocked as a stated limit", args{"no-sed-inplace", "Bash", "sed -ei f"}, true},
		{"flag inside a double quoted script passes", args{"no-sed-inplace", "Bash", `sed -e "s/ -i /x/" f`}, false},
		{"flag inside a single quoted script word passes", args{"no-sed-inplace", "Bash", "sed 'x -i' f"}, false},
		{"extended regexp flag passes", args{"no-sed-inplace", "Bash", "sed -E s/a/b/ f"}, false},
		{
			"start word inside a commit message is blocked as a stated limit",
			args{"no-sed-inplace", "Bash", `git commit -m "then sed -i note"`},
			true,
		},
		{"start character inside quoted text is blocked as a stated limit", args{"no-sed-inplace", "Bash", "echo 'a; sed -i b'"}, true},
		{"eval in an if condition is blocked", args{"no-eval", "Bash", "if ! eval x; then ls; fi"}, true},
		{"eval in an if body is blocked", args{"no-eval", "Bash", "if true; then eval echo hi; fi"}, true},
		{"eval in a while body is blocked", args{"no-eval", "Bash", `while read l; do eval "$l"; done`}, true},
		{"eval through sudo with a user is blocked", args{"no-eval", "Bash", "sudo -u root eval x"}, true},
		{"eval through time is blocked", args{"no-eval", "Bash", "time eval x"}, true},
		{"eval through command with an option is blocked", args{"no-eval", "Bash", "command -p eval x"}, true},
		{"eval through exec with a name is blocked", args{"no-eval", "Bash", "exec -a n eval x"}, true},
		{"timed nodloop eval passes", args{"no-eval", "Bash", "time nodloop eval holdout --session s --model sonnet"}, false},
		{"nohup nodloop eval passes", args{"no-eval", "Bash", "nohup nodloop eval seed --session demo &"}, false},
		{"nodloop eval through sudo passes", args{"no-eval", "Bash", "sudo -u root nodloop eval report --session s"}, false},
		{"done after a loop passes", args{"no-eval", "Bash", "echo done"}, false},
		{"shell -c through timeout is blocked", args{"no-shell-c", "Bash", "timeout 5 bash -c 'echo'"}, true},
		{"shell -c through sudo is blocked", args{"no-shell-c", "Bash", "sudo bash -c 'ls'"}, true},
		{"shell -c after a set option is blocked", args{"no-shell-c", "Bash", "bash -o pipefail -c 'ls'"}, true},
		{"shell -c after a shopt option is blocked", args{"no-shell-c", "Bash", "bash -O extglob -c ls"}, true},
		{"shell -c after a long option is blocked", args{"no-shell-c", "Bash", "bash --norc -c ls"}, true},
		{"script flag after a script path passes", args{"no-shell-c", "Bash", "bash ./deploy.sh -c config.yaml"}, false},
		{"shell -c after a cluster ending in a set option is blocked", args{"no-shell-c", "Bash", "bash -euo pipefail -c 'rm -rf build'"}, true},
		{"shell -c after a long cluster ending in a set option is blocked", args{"no-shell-c", "Bash", "bash -xeuo pipefail -c ls"}, true},
		{"shell -c after a cluster starting with a set option is blocked", args{"no-shell-c", "Bash", "bash -oe pipefail -c ls"}, true},
		{"shell -c after a cluster with a set option inside is blocked", args{"no-shell-c", "Bash", "bash -ox pipefail -c ls"}, true},
		{"shell -c after long options and a set cluster is blocked", args{"no-shell-c", "Bash", "bash --noprofile --norc -eo pipefail -c ls"}, true},
		{"shell -c after an rcfile is blocked", args{"no-shell-c", "Bash", "bash --rcfile x -c ls"}, true},
		{"shell -c after an init file is blocked", args{"no-shell-c", "Bash", "bash --init-file x -c ls"}, true},
		{"shell -c through sudo with a user cluster is blocked", args{"no-shell-c", "Bash", "sudo -Eu root bash -c ls"}, true},
		{"script flag after a set option and a script passes", args{"no-shell-c", "Bash", "bash -o pipefail ./deploy.sh -c x"}, false},
		{"script flag after an rcfile and a script passes", args{"no-shell-c", "Bash", "bash --rcfile x ./s.sh -c y"}, false},
		{"script flag through sudo with a user cluster passes", args{"no-shell-c", "Bash", "sudo -Eu root bash ./s.sh -c y"}, false},
		{"sed through sudo with a user cluster is blocked", args{"no-sed-inplace", "Bash", "sudo -Eu root sed -i x f"}, true},
		{"sed through sudo with a home cluster is blocked", args{"no-sed-inplace", "Bash", "sudo -Hu www sed -i x f"}, true},
		{"sed through sudo with a login cluster is blocked", args{"no-sed-inplace", "Bash", "sudo -iu root sed -i x f"}, true},
		{"sed through sudo with a directory is blocked", args{"no-sed-inplace", "Bash", "sudo -D /tmp sed -i x f"}, true},
		{"sed through env with a path is blocked", args{"no-sed-inplace", "Bash", "env -P /usr/bin sed -i x f"}, true},
		{"sed through env with an unset cluster is blocked", args{"no-sed-inplace", "Bash", "env -iu X sed -i x f"}, true},
		{"sed through xargs with a replace string is blocked", args{"no-sed-inplace", "Bash", "xargs -J % sed -i '' s/a/b/ %"}, true},
		{"sed through xargs with a count cluster is blocked", args{"no-sed-inplace", "Bash", "xargs -0n 1 sed -i x"}, true},
		{"sed through timeout with a signal cluster is blocked", args{"no-sed-inplace", "Bash", "timeout -vs KILL 5 sed -i x f"}, true},
		{"nodloop eval through sudo with a user cluster passes", args{"no-eval", "Bash", "sudo -Eu root nodloop eval report --session s"}, false},
		{"nodloop eval through xargs with a count cluster passes", args{"no-eval", "Bash", "xargs -0n 1 nodloop eval"}, false},
		{"nodloop eval through env with a path passes", args{"no-eval", "Bash", "env -P /usr/bin nodloop eval"}, false},
		{"eval through sudo with a user cluster is blocked", args{"no-eval", "Bash", "sudo -Eu root eval x"}, true},
		{"cd then git with a pager assignment is blocked", args{"no-cd-then-git", "Bash", "cd repo && GIT_PAGER=cat git log"}, true},
		{"cd then git through sudo is blocked", args{"no-cd-then-git", "Bash", "cd repo && sudo git log"}, true},
		{"cd with a fallback exit then git is blocked", args{"no-cd-then-git", "Bash", "cd repo || exit 1; git status"}, true},
		{"line continuation before git is blocked", args{"no-cd-then-git", "Bash", "cd repo && \\\n git log"}, true},
		{"git after another command in the directory is blocked", args{"no-cd-then-git", "Bash", "cd x && make && git log"}, true},
		{"git with a directory flag after cd passes", args{"no-cd-then-git", "Bash", "cd x && make && git -C y log"}, false},
		{"git with a directory flag after a cd line passes", args{"no-cd-then-git", "Bash", "cd build\nmake\ngit -C repo status"}, false},
		{"git with a directory flag after a pager flag passes", args{"no-cd-then-git", "Bash", "cd x; git --no-pager -C y log"}, false},
		{"git after a cd subshell passes", args{"no-cd-then-git", "Bash", "(cd x && make); git status"}, false},
		{"git after a cd subshell of its own passes", args{"no-cd-then-git", "Bash", "(cd x); git status"}, false},
		{"git after a cd subshell with a quoted parenthesis passes", args{"no-cd-then-git", "Bash", `(cd "a)b"); git status`}, false},
		{"cd into a quoted substitution then git is blocked", args{"no-cd-then-git", "Bash", `cd "$(git rev-parse --show-toplevel)" && git status`}, true},
		{"cd into a substitution then git is blocked", args{"no-cd-then-git", "Bash", "cd $(git rev-parse --show-toplevel) && git status"}, true},
		{"cd into a nested quoted substitution then git is blocked", args{"no-cd-then-git", "Bash", `cd "$(dirname "$0")" && git log`}, true},
		{"cd into a temp directory then git is blocked", args{"no-cd-then-git", "Bash", `cd "$(mktemp -d)" && git init`}, true},
		{"cd into a path with parentheses then git is blocked", args{"no-cd-then-git", "Bash", `cd "My Repo (copy)" && git status`}, true},
		{"cd into a variable then git is blocked", args{"no-cd-then-git", "Bash", "cd $HOME/repo && git status"}, true},
		{"git after a command with a substitution in the directory is blocked", args{"no-cd-then-git", "Bash", `cd x && echo "$(pwd)" && git status`}, true},
		{"readme write is blocked", args{"no-readme", "Write", "/repo/README.md"}, true},
		{"lower case readme write is blocked", args{"no-readme", "Write", "/repo/readme.md"}, true},
		{"readme under node_modules passes", args{"no-readme", "Write", "/repo/node_modules/x/README.md"}, false},
		{"unindented continuation before the sed flag is blocked", args{"no-sed-inplace", "Bash", "sed \\\n-i 's/a/b/' f"}, true},
		{"unindented continuation after a sed option is blocked", args{"no-sed-inplace", "Bash", "sed -E \\\n-i 's/a/b/' f"}, true},
		{"unindented continuation after a sed script is blocked", args{"no-sed-inplace", "Bash", "sed -e 's/a/b/' \\\n-i f"}, true},
		{"unindented continuation before the perl flag is blocked", args{"no-sed-inplace", "Bash", "perl -pe 's/a/b/' \\\n-i f"}, true},
		{"continuation inside a quoted sed script passes", args{"no-sed-inplace", "Bash", "echo 'sed \\\n-i'"}, false},
		{"unindented continuation before the shell flag is blocked", args{"no-shell-c", "Bash", "bash --norc \\\n-c 'ls'"}, true},
		{"continuation before a shell option is blocked", args{"no-shell-c", "Bash", "bash \\\n--norc -c 'ls'"}, true},
		{"continuation before a script passes", args{"no-shell-c", "Bash", "bash \\\n./deploy.sh -c x"}, false},
		{"localized readme write is blocked", args{"no-readme", "Write", "/repo/README.ko.md"}, true},
		{"region localized readme write is blocked", args{"no-readme", "Write", "/repo/README.zh-CN.md"}, true},
		{"readme with an underscore suffix is blocked", args{"no-readme", "Write", "/repo/README_EN.md"}, true},
		{"readme with a hyphen suffix is blocked", args{"no-readme", "Write", "/repo/README-dev.md"}, true},
		{"readme with an upper case extension is blocked", args{"no-readme", "Write", "/repo/README.ko.MD"}, true},
		{"readme without an extension is blocked", args{"no-readme", "Write", "/repo/README"}, true},
		{"readme in restructured text is blocked", args{"no-readme", "Write", "/repo/docs/README.rst"}, true},
		{"readme with a language extension alone is blocked", args{"no-readme", "Write", "/repo/README.ko"}, true},
		{"readme in html is blocked", args{"no-readme", "Write", "/repo/README.htm"}, true},
		{"readme generator source passes", args{"no-readme", "Write", "/repo/readme_generator.go"}, false},
		{"readme backup passes", args{"no-readme", "Write", "/repo/README.md.bak"}, false},
		{"word starting with readme passes", args{"no-readme", "Write", "/repo/READMEfoo.md"}, false},
		{"word ending in readme passes", args{"no-readme", "Write", "/repo/MYREADME.md"}, false},
		{"shell flag first in an errexit cluster is blocked", args{"no-shell-c", "Bash", "bash -ce 'echo hi'"}, true},
		{"sh flag first in an errexit cluster is blocked", args{"no-shell-c", "Bash", "sh -ce 'ls'"}, true},
		{"sh flag first in a trace cluster is blocked", args{"no-shell-c", "Bash", "sh -cx 'ls'"}, true},
		{"shell flag first in a login cluster is blocked", args{"no-shell-c", "Bash", "bash -cl 'ls'"}, true},
		{"shell cluster without the command flag passes", args{"no-shell-c", "Bash", "bash -ex ./build.sh"}, false},
		{"cd with both streams redirected then git is blocked", args{"no-cd-then-git", "Bash", "cd x > /dev/null 2>&1 && git pull"}, true},
		{"cd with an ampersand redirection then git is blocked", args{"no-cd-then-git", "Bash", "cd x &>/dev/null && git pull"}, true},
		{"cd with a stream duplicated then git is blocked", args{"no-cd-then-git", "Bash", "cd x >&2 && git status"}, true},
		{"negated git after cd is blocked", args{"no-cd-then-git", "Bash", "cd x && ! git diff --quiet"}, true},
		{"git in a quoted substitution after cd is blocked", args{"no-cd-then-git", "Bash", `cd x && echo "$(git rev-parse HEAD)"`}, true},
		{"git in an assigned substitution after cd is blocked", args{"no-cd-then-git", "Bash", "cd x && V=$(git describe --tags) && echo $V"}, true},
		{"git in backticks after cd is blocked", args{"no-cd-then-git", "Bash", "cd x && echo `git rev-parse HEAD`"}, true},
		{"git with a directory flag in a substitution after cd passes", args{"no-cd-then-git", "Bash", `cd x && echo "$(git -C y log)"`}, false},
		{"redirected cd then another command passes", args{"no-cd-then-git", "Bash", "cd x >/dev/null 2>&1 && make"}, false},
		{
			"find predicate after an escaped exec terminator passes",
			args{"no-sed-inplace", "Bash", `find . -name '*.go' -exec sed -n 1p {} \; -iname x`},
			false,
		},
		{"find predicate after a plus exec terminator passes", args{"no-sed-inplace", "Bash", "find . -exec sed -n 1p {} + -iname '*.go'"}, false},
		{"find print after an exec terminator passes", args{"no-sed-inplace", "Bash", `find . -exec sed -n 1p {} \; -print`}, false},
		{"find exec in place edit ended by an escaped terminator is blocked", args{"no-sed-inplace", "Bash", `find . -exec sed -i '' s/a/b/ {} \;`}, true},
		{"plus inside a sed script before the flag is blocked", args{"no-sed-inplace", "Bash", "sed -E 's/a+/b/' -i f"}, true},
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
