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
	"go.uber.org/mock/gomock"

	"github.com/jeon-jihyeon/nodloop/internal/llm"
	"github.com/jeon-jihyeon/nodloop/internal/llm/llmmock"
)

// extract proposes only for add and update and names the item of a duplicate
func TestRunKnowledgeExtract(t *testing.T) {
	at := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	lesson := "Run git with -C <dir> instead of changing into the directory"
	passed := `{"states":true,"holds":true,"fits":true,"why":"ok"}`
	tcs := []struct {
		name  string
		draft string
		want  []string
	}{
		{"an add proposes a candidate scoped to the repo", `{"relation":"add","kind":"judgment","content":"` + lesson + `","keys":["repo"]}`,
			[]string{"relation\tadd\n", "candidate\tk-", "scope\truns of session. repo=nodloop\n", "next\tnodloop knowledge approve"}},
		{"a duplicate proposes nothing", `{"relation":"duplicate","relates_to":"git-c","kind":"judgment","content":"` + lesson + `"}`,
			[]string{"relation\tduplicate\n", "related\tgit-c\tv1\t", "next\tnothing proposed"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, records := t.TempDir(), t.TempDir()
			getenv := func(k string) string { return map[string]string{"HOME": dir, envRecordDir: records}[k] }
			now := func() time.Time { return at }
			out := filepath.Join(dir, "out.txt")
			require.NoError(t, os.WriteFile(out, []byte("cd repo && git status"), 0o600))
			var id bytes.Buffer
			require.Equal(t, 0, runRun([]string{"record", "--producer", "session", "--label", "repo=nodloop", "--label", "dir=cmd", "--output", out}, getenv, now, &id, &bytes.Buffer{}))
			run := strings.TrimSpace(id.String())
			require.Equal(t, 0, runFeedback([]string{"add", "--trace", run, "--verdict", "reject", "--reason", "ran cd before git"}, getenv, now, &bytes.Buffer{}, &bytes.Buffer{}))
			var stderr bytes.Buffer
			require.Equal(t, 0, runKnowledge([]string{"propose", "--id", "git-c", "--kind", "judgment", "--content", "Prefer git -C", "--from", run, "--label", "repo=nodloop"},
				getenv, nil, now, &bytes.Buffer{}, &stderr), stderr.String())
			require.Equal(t, 0, runKnowledge([]string{"approve", "git-c", "--version", "1", "--approver", "ann"}, getenv, nil, now, &bytes.Buffer{}, &stderr), stderr.String())
			client := llmmock.NewMockClient(gomock.NewController(t))
			answers := []string{tc.draft, passed}
			client.EXPECT().Complete(gomock.Any(), gomock.Any()).Times(2).DoAndReturn(func(context.Context, llm.Request) (llm.Response, error) {
				next := answers[0]
				answers = answers[1:]
				return llm.Response{Output: json.RawMessage(next)}, nil
			})
			var stdout bytes.Buffer

			code := runKnowledge([]string{"extract", "--from", run}, getenv, client, now, &stdout, &stderr)

			require.Equal(t, 0, code, stderr.String())
			for _, want := range tc.want {
				assert.Contains(t, stdout.String(), want)
			}
		})
	}
}
