package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunKnowledgeAsk(t *testing.T) {
	type args struct {
		args    []string
		propose bool
	}
	type want struct {
		code      int
		questions []draftQuestion
		stderr    string
	}
	asked := draftQuestion{ID: "git-c", Version: 1, Questions: []question{{
		Question: "Approve this lesson?\nUse git -C\nScope: runs of session. repo=nodloop\n" +
			`You said "ran cd" on the answer "cd repo && git status"`,
		Header: "Draft 1/1", Options: draftOptions,
	}}}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"a candidate is asked with its content, scope and the words that taught it",
			args{args: []string{"ask", "--producer", "session", "--label", "repo=nodloop"}, propose: true},
			want{questions: []draftQuestion{asked}},
		},
		{
			"no candidate prints nothing",
			args{args: []string{"ask", "--producer", "session", "--label", "repo=nodloop"}},
			want{},
		},
		{
			"a producer is required",
			args{args: []string{"ask"}, propose: true},
			want{code: 1, stderr: "--producer"},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			getenv, now := askSetup(t, tc.args.propose)
			var stdout, stderr bytes.Buffer

			code := runKnowledge(tc.args.args, getenv, nil, now, &stdout, &stderr)

			assert.Equal(t, tc.want.code, code, stderr.String())
			assert.Contains(t, stderr.String(), tc.want.stderr)
			var got []draftQuestion
			dec := json.NewDecoder(&stdout)
			for dec.More() {
				var q draftQuestion
				require.NoError(t, dec.Decode(&q))
				got = append(got, q)
			}
			assert.Equal(t, tc.want.questions, got)
		})
	}
}

// A run of two lines rejected with a reason and, when propose is set, the candidate git-c taught by it
func askSetup(t *testing.T, propose bool) (getenv func(string) string, now func() time.Time) {
	t.Helper()
	dir, records := t.TempDir(), t.TempDir()
	getenv = func(k string) string { return map[string]string{"HOME": dir, envRecordDir: records}[k] }
	tick := 0
	now = func() time.Time {
		tick++
		return time.Date(2026, 10, 8, 12, 0, tick, 0, time.UTC)
	}
	out := filepath.Join(dir, "out.txt")
	require.NoError(t, os.WriteFile(out, []byte("cd repo && git status\nclean"), 0o600))
	var id bytes.Buffer
	require.Equal(t, 0, runRun([]string{"record", "--producer", "session", "--label", "repo=nodloop", "--output", out}, getenv, now, &id, &bytes.Buffer{}))
	run := strings.TrimSpace(id.String())
	require.Equal(t, 0, runFeedback([]string{"add", "--trace", run, "--verdict", "reject", "--reason", "ran cd"}, getenv, now, &bytes.Buffer{}, &bytes.Buffer{}))
	if propose {
		var stderr bytes.Buffer
		require.Equal(t, 0, runKnowledge([]string{"propose", "--id", "git-c", "--kind", "judgment", "--content", "Use git -C", "--from", run}, getenv, nil, now, &bytes.Buffer{}, &stderr), stderr.String())
	}
	return getenv, now
}
