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

	"github.com/jeon-jihyeon/nodloop/internal/compact"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	knowledgefile "github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	"github.com/jeon-jihyeon/nodloop/internal/llm"
	"github.com/jeon-jihyeon/nodloop/internal/llm/llmmock"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// The items a and b ann approved from one corrected run and the compaction drafted to merge them into a
// The coverage check answers the coverage and none runs for an empty one
func compactionHome(t *testing.T, coverage string) (getenv func(string) string, records, compaction string) {
	t.Helper()
	dir, records := t.TempDir(), t.TempDir()
	getenv = envOf(map[string]string{"HOME": dir, envRecordDir: records})
	tick := 0
	now := func() time.Time {
		tick++
		return time.Date(2026, 10, 7, 12, 0, tick, 0, time.UTC)
	}
	out := filepath.Join(dir, "out.txt")
	require.NoError(t, os.WriteFile(out, []byte("cd repo && git status"), 0o600))
	run := extractRun(t, getenv, now, out, "ran cd")
	var stderr bytes.Buffer
	for _, item := range []string{"a", "b"} {
		require.Equal(t, 0, runKnowledge([]string{"propose", "--id", item, "--kind", "judgment", "--content", "Lesson " + item, "--from", run},
			getenv, nil, now, atTerminal, &bytes.Buffer{}, &stderr), stderr.String())
		require.Equal(t, 0, runKnowledge([]string{"approve", item, "--version", "1", "--approver", "ann"}, getenv, nil, now, atTerminal, &bytes.Buffer{}, &stderr),
			stderr.String())
	}
	answers := []string{
		`{"items":[{"id":"a","kind":"judgment","content":"Lesson a and b","from":["a","b"],"producer":"session","labels":{"repo":["nodloop"]}}]}`, coverage,
	}
	if coverage == "" {
		answers = answers[:1]
	}
	client := llmmock.NewMockClient(gomock.NewController(t))
	client.EXPECT().Complete(gomock.Any(), gomock.Any()).Times(len(answers)).DoAndReturn(func(context.Context, llm.Request) (llm.Response, error) {
		next := answers[0]
		answers = answers[1:]
		return llm.Response{Output: json.RawMessage(next)}, nil
	})
	var compacted bytes.Buffer
	require.Equal(t, 0, runKnowledge([]string{"compact", "a"}, getenv, client, now, atTerminal, &compacted, &stderr), stderr.String())
	for line := range strings.Lines(compacted.String()) {
		if rest, ok := strings.CutPrefix(line, "compaction\t"); ok {
			compaction = strings.TrimSpace(rest)
		}
	}
	require.NotEmpty(t, compaction)
	if coverage != "" {
		require.Equal(t, 0, runKnowledge([]string{"check", compaction}, getenv, client, now, atTerminal, &bytes.Buffer{}, &stderr), stderr.String())
	}
	return getenv, records, compaction
}

// approve-compaction approves the merge under the person's name only with the terminal proof and records the intervention beside it
// 1. without the proof it records a proposal of the compaction and changes nothing
// 2. without a coverage check it writes nothing
// 3. a failed export still prints the records and records the intervention before the error
func TestRunKnowledgeApproveCompaction(t *testing.T) {
	type args struct {
		proof trace.Proof
		// What the coverage check answered and no check for none
		coverage string
		// approved.md of the records turned into a directory so the export after the write fails
		blocked bool
	}
	passed := `{"items":[{"old":"a","covered_by":["a"]},{"old":"b","covered_by":["a"]}]}`
	lost := `{"items":[{"old":"a","covered_by":["a"]},{"old":"b","covered_by":["a"],"lost":["the window"]}]}`
	type want struct {
		code   int
		stdout string
		stderr string
		// The newest status of each version of a and then of b
		statuses []string
		// The authority, proof, person and choice of each intervention after the approvals of the setup
		interventions []string
	}
	merged := []string{"v1 superseded", "v2 approved", "v1 retired"}
	unchanged := []string{"v1 approved", "v2 candidate", "v1 approved"}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"at the terminal the merge is approved under the person", args{atTerminal, passed, false},
			want{0, "new\ta\tv2\tapproved\tjudgment\tLesson a and b\n", "", merged, []string{"person terminal ann keep"}}},
		{"from a script it records a proposal and changes nothing", args{trace.ProofNone, passed, false},
			want{0, "\tapprove-compaction is recorded as a proposal and nothing changed", "", unchanged, []string{"agent none  keep"}}},
		{"without a coverage check nothing is written", args{atTerminal, "", false}, want{1, "", compact.ErrNoCoverage.Error(), unchanged, []string{}}},
		{"a coverage check that lost a fact writes nothing", args{atTerminal, lost, false},
			want{1, "", knowledge.ErrCoverageNotPassed.Error(), unchanged, []string{}}},
		{"a failed export still prints the merge and records its intervention", args{atTerminal, passed, true},
			want{1, "new\ta\tv2\tapproved", knowledge.ErrExport.Error(), merged, []string{"person terminal ann keep"}}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			getenv, records, compaction := compactionHome(t, tc.args.coverage)
			if tc.args.blocked {
				require.NoError(t, os.Remove(knowledgefile.RulesPath(records)))
				require.NoError(t, os.Mkdir(knowledgefile.RulesPath(records), 0o755))
			}
			var stdout, stderr bytes.Buffer

			code := runKnowledge([]string{"approve-compaction", compaction, "--approver", "ann"}, getenv, nil, time.Now, tc.args.proof, &stdout, &stderr)

			assert.Equal(t, tc.want.code, code, stderr.String())
			assert.Contains(t, stdout.String(), tc.want.stdout)
			assert.Contains(t, stderr.String(), tc.want.stderr)
			assert.Equal(t, tc.want.statuses, append(statusesOf(t, records, "a"), statusesOf(t, records, "b")...))
			assert.Equal(t, tc.want.interventions, interventionsIn(t, records)[2:])
		})
	}
}
