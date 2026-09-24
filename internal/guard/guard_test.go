package guard_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/guard"
	"github.com/jeon-jihyeon/nodloop/internal/veto"
)

func TestRun(t *testing.T) {
	b, err := os.ReadFile("testdata/vetoes.yaml")
	require.NoError(t, err)
	vetoes, err := veto.Parse(b)
	require.NoError(t, err)
	fixtures := map[string]string{}
	for _, name := range []string{"bash_plain.json", "bash_sed.json", "write_readme.json"} {
		b, err := os.ReadFile(filepath.Join("testdata", name))
		require.NoError(t, err)
		fixtures[name] = string(b)
	}
	repo := "/Users/me/repo"
	sedBlocked := "nodloop guard: Bash call blocked by veto no-sed-inplace\n" +
		"sed -i and perl -i are forbidden. Use the Edit tool to modify files\n"
	loadWarning := "nodloop guard: failed to load vetoes, skipped: " + assert.AnError.Error() + "\n"
	parseFailed := "nodloop guard: failed to parse hook input: "
	type args struct {
		stdin  string
		vetoes veto.Vetoes
		err    error
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
		{"plain bash input passes", args{fixtures["bash_plain.json"], vetoes, nil}, want{guard.ExitPass, repo, ""}},
		{
			"sed in place edit is blocked with the veto id and reason",
			args{fixtures["bash_sed.json"], vetoes, nil},
			want{guard.ExitBlock, repo, sedBlocked},
		},
		{
			"readme write is blocked",
			args{fixtures["write_readme.json"], vetoes, nil},
			want{guard.ExitBlock, repo, "nodloop guard: Write call blocked by veto no-readme\nDo not create README files\n"},
		},
		{
			"sed in place edit passes without vetoes",
			args{fixtures["bash_sed.json"], nil, nil},
			want{guard.ExitPass, repo, ""},
		},
		{
			"non string tool input field passes",
			args{`{"tool_name":"Bash","tool_input":{"command":["sed","-i"]}}`, vetoes, nil},
			want{guard.ExitPass, "", ""},
		},
		{
			"non json input fails to parse",
			args{"not json", vetoes, nil},
			want{guard.ExitFail, "", parseFailed + "invalid character 'o' in literal null (expecting 'u')\n"},
		},
		{
			"missing tool name fails to parse",
			args{`{"tool_input":{"command":"ls"}}`, vetoes, nil},
			want{guard.ExitFail, "", parseFailed + guard.ErrToolNameMissing.Error() + "\n"},
		},
		{
			"load failure is warned and the loaded vetoes still block",
			args{fixtures["bash_sed.json"], vetoes, assert.AnError},
			want{guard.ExitBlock, repo, loadWarning + sedBlocked},
		},
		{
			"load failure without a match fails with a warning",
			args{fixtures["bash_plain.json"], vetoes, assert.AnError},
			want{guard.ExitFail, repo, loadWarning},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var stderr bytes.Buffer
			var cwd string
			load := func(c string) (veto.Vetoes, error) {
				cwd = c
				return tc.args.vetoes, tc.args.err
			}
			got := guard.Run(strings.NewReader(tc.args.stdin), &stderr, load)
			assert.Equal(t, tc.want, want{got, cwd, stderr.String()})
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
		code := guard.Run(bytes.NewReader(input), io.Discard, load)
		require.Equal(b, guard.ExitBlock, code, "exit code")
	}
}
