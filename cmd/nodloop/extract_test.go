package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/jeon-jihyeon/nodloop/internal/extract"
	"github.com/jeon-jihyeon/nodloop/internal/llm"
	"github.com/jeon-jihyeon/nodloop/internal/llm/llmmock"
)

// extract proposes only for add and update and names the item of a duplicate
func TestRunKnowledgeExtract(t *testing.T) {
	at := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	lesson := "Run git with -C <dir> instead of changing into the directory"
	passed := `{"states":true,"holds":true,"fits":true,"why":"ok"}`
	duplicate := `{"relation":"duplicate","relates_to":"git-c","kind":"judgment","content":"` + lesson + `"}`
	// The setup of git-c given the run extract learns from and its output file
	type setup func(t *testing.T, getenv func(string) string, now func() time.Time, run, out string)
	propose := func(t *testing.T, getenv func(string) string, now func() time.Time, taught string) {
		t.Helper()
		var stderr bytes.Buffer
		require.Equal(t, 0, runKnowledge([]string{"propose", "--id", "git-c", "--kind", "judgment", "--content", "Prefer git -C", "--from", taught, "--label", "repo=nodloop"},
			getenv, nil, now, &bytes.Buffer{}, &stderr), stderr.String())
	}
	var approved setup = func(t *testing.T, getenv func(string) string, now func() time.Time, run, _ string) {
		t.Helper()
		propose(t, getenv, now, run)
		var stderr bytes.Buffer
		require.Equal(t, 0, runKnowledge([]string{"approve", "git-c", "--version", "1", "--approver", "ann"}, getenv, nil, now, &bytes.Buffer{}, &stderr), stderr.String())
	}
	// git-c waits as a candidate another corrected run taught instead of being approved
	var waiting setup = func(t *testing.T, getenv func(string) string, now func() time.Time, _, out string) {
		t.Helper()
		propose(t, getenv, now, extractRun(t, getenv, now, out, "cd again"))
	}
	type args struct {
		init  setup
		flags []string
		// What the drafter and then the critic answer
		answers []string
	}
	type want struct {
		code int
		// The parts stdout holds in this order
		stdout []string
		stderr string
	}
	from := []string{"--from", "{run}"}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"an add proposes a candidate scoped to the repo", args{approved, from, []string{`{"relation":"add","kind":"judgment","content":"` + lesson + `","keys":["repo"]}`, passed}},
			want{0, []string{"relation\tadd\n", "candidate\tk-", "scope\truns of session. repo=nodloop\n", "next\t" + extractNext[extract.RelationAdd] + "\n"}, ""}},
		{"a duplicate proposes nothing", args{approved, from, []string{duplicate, passed}},
			want{0, []string{"relation\tduplicate\n", "related\tgit-c\tv1\t", "next\tnothing proposed"}, ""}},
		{"a duplicate of a waiting candidate joins its evidence", args{waiting, from, []string{duplicate, passed}},
			want{0, []string{"relation\tduplicate\n", "candidate\tgit-c\tv1\t", "next\t" + extractCorroborated + "\n"}, ""}},
		{"no run to learn from fails before any model call", args{approved, nil, nil}, want{1, nil, "--from is required"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, records := t.TempDir(), t.TempDir()
			getenv := func(k string) string { return map[string]string{"HOME": dir, envRecordDir: records}[k] }
			now := func() time.Time { return at }
			out := filepath.Join(dir, "out.txt")
			require.NoError(t, os.WriteFile(out, []byte("cd repo && git status"), 0o600))
			run := extractRun(t, getenv, now, out, "ran cd before git", "--label", "dir=cmd")
			tc.args.init(t, getenv, now, run, out)
			client := llmmock.NewMockClient(gomock.NewController(t))
			answers := tc.args.answers
			client.EXPECT().Complete(gomock.Any(), gomock.Any()).Times(len(answers)).DoAndReturn(func(context.Context, llm.Request) (llm.Response, error) {
				next := answers[0]
				answers = answers[1:]
				return llm.Response{Output: json.RawMessage(next)}, nil
			})
			flags := make([]string, 0, len(tc.args.flags))
			for _, f := range tc.args.flags {
				flags = append(flags, strings.ReplaceAll(f, "{run}", run))
			}
			var stdout, stderr bytes.Buffer

			code := runKnowledge(append([]string{"extract"}, flags...), getenv, client, now, &stdout, &stderr)

			assert.Equal(t, tc.want.code, code, stderr.String())
			assert.Regexp(t, extractInOrder(tc.want.stdout), stdout.String())
			assert.Contains(t, stderr.String(), tc.want.stderr)
			assert.NotRegexp(t, `knowledge (approve|retire|reaffirm)|approve-compaction|--approver`, stdout.String(), "no line names a command that decides")
		})
	}
}

// A rejected run of producer session in repo nodloop with the output file and the extra flags
func extractRun(t *testing.T, getenv func(string) string, now func() time.Time, out, reason string, flags ...string) string {
	t.Helper()
	var id bytes.Buffer
	args := append([]string{"record", "--producer", "session", "--label", "repo=nodloop", "--output", out}, flags...)
	require.Equal(t, 0, runRun(args, getenv, now, &id, &bytes.Buffer{}))
	run := strings.TrimSpace(id.String())
	require.Equal(t, 0, runFeedback([]string{"add", "--trace", run, "--verdict", "reject", "--reason", reason, "--reviewer", "ann"}, getenv, now, &bytes.Buffer{}, &bytes.Buffer{}))
	return run
}

// A pattern that matches text holding the parts in this order
func extractInOrder(parts []string) string {
	quoted := make([]string, 0, len(parts))
	for _, p := range parts {
		quoted = append(quoted, regexp.QuoteMeta(p))
	}
	return "(?s)^.*" + strings.Join(quoted, ".*")
}

// With llm set to a chat endpoint knowledge extract drafts and criticizes there and never runs claude
func TestRunKnowledgeExtractChat(t *testing.T) {
	at := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	answers := []string{
		`{"relation":"add","kind":"judgment","content":"Run git with -C <dir> instead of changing into the directory","keys":["repo"]}`,
		`{"states":true,"holds":true,"fits":true,"why":"ok"}`,
	}
	type want struct {
		code   int
		stdout string
		stderr string
		// The system prompt of each request the endpoint saw
		systems []string
	}
	tcs := []struct {
		name string
		// The status the endpoint answers
		args int
		want want
	}{
		{"the endpoint drafts and criticizes", http.StatusOK, want{0, "candidate\tk-", "", []string{extract.Rules, extract.CriticRules}}},
		{"an endpoint that refuses the draft fails the extract", http.StatusBadRequest, want{1, "", "endpoint answered an error status: 400", []string{extract.Rules}}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var mu sync.Mutex
			var systems []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Model    string `json:"model"`
					Messages []struct {
						Content string `json:"content"`
					} `json:"messages"`
				}
				assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				assert.Equal(t, "qwen3", body.Model)
				mu.Lock()
				defer mu.Unlock()
				systems = append(systems, body.Messages[0].Content)
				content, _ := json.Marshal(answers[len(systems)-1])
				w.WriteHeader(tc.args)
				_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":%s}}],"usage":{"prompt_tokens":100,"completion_tokens":20}}`, content)
			}))
			t.Cleanup(srv.Close)
			home := savedHome(t, `{"llm":{"backend":"chat","base_url":"`+srv.URL+`/v1","model":"qwen3"}}`)
			records := t.TempDir()
			// A claude binary that does not exist fails any call that reaches claude
			getenv := envOf(map[string]string{"HOME": string(home), envRecordDir: records, envClaudeBin: filepath.Join(records, "no-claude")})
			now := func() time.Time { return at }
			out := filepath.Join(records, "out.txt")
			require.NoError(t, os.WriteFile(out, []byte("cd repo && git status"), 0o600))
			runID := extractRun(t, getenv, now, out, "ran cd before git")
			stdout, err := os.Create(filepath.Join(t.TempDir(), "stdout"))
			require.NoError(t, err)
			var stderr bytes.Buffer

			code := run([]string{"knowledge", "extract", "--from", runID}, getenv, strings.NewReader(""), stdout, &stderr)

			assert.Equal(t, tc.want.code, code, stderr.String())
			assert.Contains(t, stderr.String(), tc.want.stderr)
			printed, err := os.ReadFile(stdout.Name())
			require.NoError(t, err)
			assert.Contains(t, string(printed), tc.want.stdout)
			mu.Lock()
			defer mu.Unlock()
			assert.Equal(t, tc.want.systems, systems)
		})
	}
}
