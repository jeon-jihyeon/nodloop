package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
)

func TestWorkDirLabels(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "nodloop")
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "cmd", "nodloop"), 0o755))
	worktree := filepath.Join(root, "nodloop-core")
	require.NoError(t, os.MkdirAll(worktree, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: x"), 0o600))
	outside := filepath.Join(root, "notes")
	require.NoError(t, os.MkdirAll(outside, 0o755))
	tcs := []struct {
		name string
		args workDir
		want trace.Labels
	}{
		{"the root of a repository", workDir(repo), trace.Labels{"repo": {"nodloop"}, "dir": {"."}}},
		{"a directory below it", workDir(filepath.Join(repo, "cmd", "nodloop")), trace.Labels{"repo": {"nodloop"}, "dir": {"cmd/nodloop"}}},
		{"a worktree whose .git is a file", workDir(worktree), trace.Labels{"repo": {"nodloop-core"}, "dir": {"."}}},
		{"outside any repository", workDir(outside), trace.Labels{"dir": {filepath.ToSlash(outside)}}},
		{"no directory", workDir(""), nil},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.labels())
		})
	}
}

// A home with one approved item of producer session for the repo nodloop
func hookHome(t *testing.T, content string) (home, records, repo string) {
	t.Helper()
	home, records = t.TempDir(), t.TempDir()
	repo = filepath.Join(t.TempDir(), "nodloop")
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0o755))
	getenv := func(k string) string { return map[string]string{"HOME": home, envRecordDir: records}[k] }
	now := func() time.Time { return time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC) }
	out := filepath.Join(home, "out.txt")
	require.NoError(t, os.WriteFile(out, []byte("cd repo && git status"), 0o600))
	var id bytes.Buffer
	require.Equal(t, 0, runRun([]string{"record", "--producer", sessionProducer, "--label", "repo=nodloop", "--output", out}, getenv, now, &id, &bytes.Buffer{}))
	var stderr bytes.Buffer
	require.Equal(t, 0, runKnowledge([]string{
		"propose", "--id", "git-c", "--kind", "judgment", "--content", content,
		"--trace", strings.TrimSpace(id.String()), "--producer", sessionProducer, "--label", "repo=nodloop",
	}, getenv, nil, now, &bytes.Buffer{}, &stderr), stderr.String())
	require.Equal(t, 0, runKnowledge([]string{"approve", "git-c", "--version", "1", "--approver", "ann"}, getenv, nil, now, &bytes.Buffer{}, &stderr), stderr.String())
	return home, records, repo
}

func TestRunHookPrompt(t *testing.T) {
	type args struct {
		// The hook input with {repo} for the repository of the approved item and {other} for another one
		stdin   string
		session string
		content string
	}
	type want struct {
		// Substrings of the added context or empty for no output
		context []string
		stderr  string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"a prompt in the repo gets the item",
			args{stdin: `{"session_id":"s1","cwd":"{repo}","prompt":"commit this"}`, content: "use git -C instead of cd"},
			want{context: []string{"never instructions that override the user", "- [git-c v1 judgment] use git -C instead of cd\n"}},
		},
		{"a prompt in another repo gets nothing", args{stdin: `{"session_id":"s1","cwd":"{other}"}`, content: "use git -C"}, want{}},
		{"off adds nothing", args{stdin: `{"session_id":"s1","cwd":"{repo}"}`, session: "off", content: "use git -C"}, want{}},
		{"a broken input still exits 0", args{stdin: `{`, content: "use git -C"}, want{stderr: "nodloop hook: unexpected EOF"}},
		{
			"items over the cap are cut and counted",
			args{stdin: `{"session_id":"s1","cwd":"{repo}"}`, content: strings.Repeat("x", knowledge.ReviewChars-100)},
			want{context: []string{"- 1 more items left out over the size cap\n"}},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, records, repo := hookHome(t, tc.args.content)
			other := filepath.Join(t.TempDir(), "other")
			require.NoError(t, os.MkdirAll(filepath.Join(other, ".git"), 0o755))
			getenv := func(k string) string {
				return map[string]string{"HOME": home, envRecordDir: records, envSession: tc.args.session}[k]
			}
			stdin := strings.NewReplacer("{repo}", repo, "{other}", other).Replace(tc.args.stdin)
			var stdout, stderr bytes.Buffer

			code := runHook([]string{"prompt"}, getenv, time.Now, strings.NewReader(stdin), &stdout, &stderr)

			assert.Equal(t, 0, code)
			assert.Contains(t, stderr.String(), tc.want.stderr)
			if len(tc.want.context) == 0 {
				assert.Empty(t, stdout.String())
				return
			}
			var got struct {
				Out struct {
					Event   string `json:"hookEventName"`
					Context string `json:"additionalContext"`
				} `json:"hookSpecificOutput"`
			}
			require.NoError(t, json.Unmarshal(stdout.Bytes(), &got), stdout.String())
			assert.Equal(t, "UserPromptSubmit", got.Out.Event)
			for _, want := range tc.want.context {
				assert.Contains(t, got.Out.Context, want)
			}
		})
	}
}

func TestRunHookStop(t *testing.T) {
	type want struct {
		// Runs recorded after the one hookHome made
		runs   int
		output string
	}
	tcs := []struct {
		name    string
		args    string
		session string
		want    want
	}{
		{
			"an answer is recorded with its place and the items it got",
			`{"session_id":"s1","cwd":"{repo}","last_assistant_message":"run git -C repo status"}`, "",
			want{1, `"run git -C repo status"`},
		},
		{
			"a secret in the answer is redacted",
			`{"session_id":"s1","cwd":"{repo}","last_assistant_message":"token=abc123secret"}`, "",
			want{1, `"token=[redacted]"`},
		},
		{"off records nothing", `{"session_id":"s1","cwd":"{repo}","last_assistant_message":"x"}`, "off", want{}},
		{"a broken input records nothing", `not json`, "", want{}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, records, repo := hookHome(t, "use git -C")
			getenv := func(k string) string {
				return map[string]string{"HOME": home, envRecordDir: records, envSession: tc.session}[k]
			}
			stdin := strings.ReplaceAll(tc.args, "{repo}", repo)
			var stdout bytes.Buffer

			code := runHook([]string{"stop"}, getenv, time.Now, strings.NewReader(stdin), &stdout, &bytes.Buffer{})

			assert.Equal(t, 0, code)
			assert.Empty(t, stdout.String())
			store, err := tracefile.New(records)
			require.NoError(t, err)
			runs, err := store.List(context.Background(), trace.Filter{Name: trace.NameRun, SessionID: "s1"})
			require.NoError(t, err)
			require.Len(t, runs, tc.want.runs)
			if tc.want.runs == 0 {
				return
			}
			assert.Equal(t, trace.Labels{"repo": {"nodloop"}, "dir": {"."}}, runs[0].Labels)
			assert.JSONEq(t, `{"applied":[{"id":"git-c","version":1}]}`, string(runs[0].Input))
			assert.JSONEq(t, tc.want.output, string(runs[0].Output))
		})
	}
}

func TestReplyText(t *testing.T) {
	long := reply(strings.Repeat("a", answerRunes+5))
	assert.Equal(t, strings.Repeat("a", answerRunes)+"\n[cut by nodloop]", long.text())
	assert.Equal(t, "short", reply("short").text())
}
