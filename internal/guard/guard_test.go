package guard_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/guard"
	"github.com/jeon-jihyeon/nodloop/internal/veto"
	vetofile "github.com/jeon-jihyeon/nodloop/internal/veto/file"
)

func TestRun(t *testing.T) {
	b, err := os.ReadFile("testdata/vetoes.yaml")
	require.NoError(t, err)
	vetoes, err := veto.Parse(b)
	require.NoError(t, err)
	fixtures := map[string]string{}
	for _, name := range []string{"bash_plain.json", "bash_sed.json", "monitor_sed.json", "write_readme.json"} {
		b, err := os.ReadFile(filepath.Join("testdata", name))
		require.NoError(t, err)
		fixtures[name] = string(b)
	}
	repo := "/Users/me/repo"
	sedBlocked := "nodloop guard: Bash call blocked by veto no-sed-inplace\n" +
		"sed -i and perl -i are forbidden. Use the Edit tool to modify files\n"
	loadWarning := "nodloop guard: failed to load vetoes, skipped: " + assert.AnError.Error() + "\n"
	parseFailed := "nodloop guard: failed to parse hook input: "
	vetoPath := "/Users/me/repo/.claude/nodloop/vetoes.yaml"
	unreadable := fmt.Errorf("%s: %w: %w", vetoPath, vetofile.ErrRead, veto.ErrYAMLInvalid)
	unreadableWarning := "nodloop guard: failed to load vetoes, skipped: " + unreadable.Error() + "\n"
	missing := fmt.Errorf("%s: %w: %w", vetoPath, vetofile.ErrRead, os.ErrNotExist)
	denied := fmt.Errorf("%s: %w: %w", vetoPath, vetofile.ErrRead, os.ErrPermission)
	shared := fmt.Errorf("%w: %s", vetofile.ErrOutsideHome, unreadable)
	teamPath := "/Users/me/dotfiles/team-vetoes.yaml"
	teamUnreadable := fmt.Errorf("%s: %w: %w", teamPath, vetofile.ErrRead, veto.ErrYAMLInvalid)
	teamWarning := "nodloop guard: failed to load vetoes, skipped: " + teamUnreadable.Error() + "\n"
	type args struct {
		stdin  string
		vetoes veto.Vetoes
		err    error
		named  string
	}
	type want struct {
		code   guard.Exit
		cwd    string
		stderr string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"plain bash input passes", args{fixtures["bash_plain.json"], vetoes, nil, ""}, want{guard.ExitPass, repo, ""}},
		{
			"sed in place edit is blocked with the veto id and reason",
			args{fixtures["bash_sed.json"], vetoes, nil, ""},
			want{guard.ExitBlock, repo, sedBlocked},
		},
		{
			"sed in place edit through Monitor is blocked by the derived commands",
			args{fixtures["monitor_sed.json"], vetoes, nil, ""},
			want{guard.ExitBlock, repo, strings.Replace(sedBlocked, "Bash call", "Monitor call", 1)},
		},
		{
			"readme write is blocked",
			args{fixtures["write_readme.json"], vetoes, nil, ""},
			want{guard.ExitBlock, repo, "nodloop guard: Write call blocked by veto no-readme\nDo not create README files\n"},
		},
		{
			"sed in place edit passes without vetoes",
			args{fixtures["bash_sed.json"], nil, nil, ""},
			want{guard.ExitPass, repo, ""},
		},
		{
			"non string tool input field passes",
			args{`{"tool_name":"Bash","tool_input":{"command":["sed","-i"]}}`, vetoes, nil, ""},
			want{guard.ExitPass, "", ""},
		},
		{
			"non json input fails to parse",
			args{"not json", vetoes, nil, ""},
			want{guard.ExitFail, "", parseFailed + "invalid character 'o' in literal null (expecting 'u')\n"},
		},
		{
			"missing tool name fails to parse",
			args{`{"tool_input":{"command":"ls"}}`, vetoes, nil, ""},
			want{guard.ExitFail, "", parseFailed + guard.ErrToolNameMissing.Error() + "\n"},
		},
		{
			"load failure is warned and the loaded vetoes still block",
			args{fixtures["bash_sed.json"], vetoes, assert.AnError, ""},
			want{guard.ExitBlock, repo, loadWarning + sedBlocked},
		},
		{
			"load failure without a match fails with a warning",
			args{fixtures["bash_plain.json"], vetoes, assert.AnError, ""},
			want{guard.ExitFail, repo, loadWarning},
		},
		{
			"unreadable veto file blocks a call no veto matches",
			args{fixtures["bash_plain.json"], vetoes, unreadable, ""},
			want{guard.ExitBlock, repo, unreadableWarning + "nodloop guard: Bash call blocked because a veto file is not valid YAML\n" +
				"Fix the file named above. Reading and editing a veto file still pass\n"},
		},
		{
			"unreadable veto file still lets the loaded vetoes name the block",
			args{fixtures["bash_sed.json"], vetoes, unreadable, ""},
			want{guard.ExitBlock, repo, unreadableWarning + sedBlocked},
		},
		{
			"unreadable veto file lets an edit of a veto file through",
			args{`{"tool_name":"Edit","tool_input":{"file_path":"` + vetoPath + `"},"cwd":"/Users/me/repo"}`, vetoes, unreadable, ""},
			want{guard.ExitFail, repo, unreadableWarning},
		},
		{
			"unreadable veto file lets a read of an approved veto file through",
			args{
				`{"tool_name":"Read","tool_input":{"file_path":"/Users/me/.claude/nodloop/vetoes.approved.0123456789ab.yaml"}}`,
				vetoes, unreadable, "",
			},
			want{guard.ExitFail, "", unreadableWarning},
		},
		{
			"unreadable veto file blocks an edit of another file",
			args{`{"tool_name":"Edit","tool_input":{"file_path":"/Users/me/repo/main.go"}}`, nil, unreadable, ""},
			want{guard.ExitBlock, "", unreadableWarning + "nodloop guard: Edit call blocked because a veto file is not valid YAML\n" +
				"Fix the file named above. Reading and editing a veto file still pass\n"},
		},
		{
			"unreadable file named on the command line lets a read of it through",
			args{`{"tool_name":"Read","tool_input":{"file_path":"` + teamPath + `"}}`, nil, teamUnreadable, teamPath},
			want{guard.ExitFail, "", teamWarning},
		},
		{
			"unreadable file named on the command line lets an edit of it through",
			args{`{"tool_name":"Edit","tool_input":{"file_path":"` + teamPath + `"}}`, nil, teamUnreadable, teamPath},
			want{guard.ExitFail, "", teamWarning},
		},
		{
			"unreadable file named on the command line still blocks another call",
			args{fixtures["bash_plain.json"], nil, teamUnreadable, teamPath},
			want{guard.ExitBlock, repo, teamWarning + "nodloop guard: Bash call blocked because a veto file is not valid YAML\n" +
				"Fix the file named above. Reading and editing a veto file still pass\n"},
		},
		{
			"unreadable file under another name without a command line name blocks a read of it",
			args{`{"tool_name":"Read","tool_input":{"file_path":"` + teamPath + `"}}`, nil, teamUnreadable, ""},
			want{guard.ExitBlock, "", teamWarning + "nodloop guard: Read call blocked because a veto file is not valid YAML\n" +
				"Fix the file named above. Reading and editing a veto file still pass\n"},
		},
		{
			"veto file without read permission fails open with a warning because no edit repairs it",
			args{fixtures["bash_plain.json"], vetoes, denied, ""},
			want{guard.ExitFail, repo, "nodloop guard: failed to load vetoes, skipped: " + denied.Error() + "\n"},
		},
		{
			"veto file without read permission still lets the loaded vetoes block",
			args{fixtures["bash_sed.json"], vetoes, denied, ""},
			want{guard.ExitBlock, repo, "nodloop guard: failed to load vetoes, skipped: " + denied.Error() + "\n" + sedBlocked},
		},
		{
			"invalid yaml outside home fails open with a warning",
			args{fixtures["bash_plain.json"], vetoes, shared, ""},
			want{guard.ExitFail, repo, "nodloop guard: failed to load vetoes, skipped: " + shared.Error() + "\n"},
		},
		{
			"invalid yaml under home beside one outside still blocks",
			args{fixtures["bash_plain.json"], nil, errors.Join(shared, unreadable), ""},
			want{guard.ExitBlock, repo, "nodloop guard: failed to load vetoes, skipped: " + errors.Join(shared, unreadable).Error() + "\n" +
				"nodloop guard: Bash call blocked because a veto file is not valid YAML\n" +
				"Fix the file named above. Reading and editing a veto file still pass\n"},
		},
		{
			"missing veto file named by hand fails open with a warning",
			args{fixtures["bash_plain.json"], vetoes, missing, ""},
			want{guard.ExitFail, repo, "nodloop guard: failed to load vetoes, skipped: " + missing.Error() + "\n"},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			var cwd string
			load := func(c string) (veto.Vetoes, error) {
				cwd = c
				return tc.args.vetoes, tc.args.err
			}
			got := guard.Run(strings.NewReader(tc.args.stdin), &stdout, &stderr, load, tc.args.named, time.Time{})
			assert.Equal(t, tc.want, want{got.Exit, cwd, stderr.String()})
			assert.Empty(t, stdout.String())
		})
	}
}

// A matched veto decides with a log entry and only an ask writes the hook output
func TestRunDecision(t *testing.T) {
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.FixedZone("KST", 9*60*60))
	condition, err := veto.NewCondition("command", "rm -rf", "")
	require.NoError(t, err)
	vetoOf := func(action veto.Action) veto.Vetoes {
		v, err := veto.New("no-rm", "Bash", []veto.Condition{condition}, "Ask before deleting", action, true)
		require.NoError(t, err)
		return veto.Vetoes{v}
	}
	unreadable := fmt.Errorf("%w: broken", veto.ErrYAMLInvalid)
	removal := `{"session_id":"s1","tool_name":"Bash","tool_input":{"command":"rm -rf build"},"cwd":"/repo"}`
	entry := func(action veto.Action) *guard.Entry {
		return &guard.Entry{Time: at.UTC(), Session: "s1", Cwd: "/repo", Tool: "Bash", Veto: "no-rm", Action: action}
	}
	type args struct {
		stdin  string
		vetoes veto.Vetoes
		err    error
	}
	type want struct {
		decision guard.Decision
		stdout   string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"an ask veto passes with the hook output and an entry",
			args{removal, vetoOf(veto.ActionAsk), nil},
			want{
				guard.Decision{Exit: guard.ExitPass, Entry: entry(veto.ActionAsk)},
				`{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"ask",` +
					`"permissionDecisionReason":"nodloop veto no-rm: Ask before deleting"}}` + "\n",
			},
		},
		{
			"a block veto blocks with an entry and no hook output",
			args{removal, vetoOf(veto.ActionBlock), nil},
			want{guard.Decision{Exit: guard.ExitBlock, Entry: entry(veto.ActionBlock)}, ""},
		},
		{
			"a call no veto matches carries no entry",
			args{`{"tool_name":"Bash","tool_input":{"command":"ls"}}`, vetoOf(veto.ActionAsk), nil},
			want{guard.Decision{Exit: guard.ExitPass}, ""},
		},
		{
			"the lockout of an invalid veto file carries no entry",
			args{`{"tool_name":"Bash","tool_input":{"command":"ls"}}`, nil, unreadable},
			want{guard.Decision{Exit: guard.ExitBlock}, ""},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var stdout bytes.Buffer
			load := func(string) (veto.Vetoes, error) { return tc.args.vetoes, tc.args.err }
			got := guard.Run(strings.NewReader(tc.args.stdin), &stdout, io.Discard, load, "", at)
			assert.Equal(t, tc.want, want{got, stdout.String()})
		})
	}
}

// Cost of parsing and evaluating 50 vetoes on every call
// The hook is a fresh process per tool call so parsing is paid every run
func BenchmarkRunWithFiftyVetoes(b *testing.B) {
	yamlBytes, err := os.ReadFile("testdata/fifty.yaml")
	require.NoError(b, err)
	input, err := os.ReadFile("testdata/bash_sed.json")
	require.NoError(b, err)
	load := func(string) (veto.Vetoes, error) { return veto.Parse(yamlBytes) }
	b.ReportAllocs()
	for b.Loop() {
		got := guard.Run(bytes.NewReader(input), io.Discard, io.Discard, load, "", time.Time{})
		require.Equal(b, guard.ExitBlock, got.Exit, "exit code")
	}
}
