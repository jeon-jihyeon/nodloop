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
	knowledgefile "github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
)

func TestKnowledgeForLabels(t *testing.T) {
	type want struct {
		code     int
		err, out string
	}
	tcs := []struct {
		name string
		args []string
		want want
	}{
		{"empty labels", nil, want{out: "total\t0 items\t0 chars\n"}},
		{"repeated value", []string{"--label", "context=x", "--label", "context=x"},
			want{out: "total\t0 items\t0 chars\n"}},
		{"different alternatives", []string{"--label", "context=x", "--label", "context=y"},
			want{code: 1, err: trace.ErrLabelValues.Error()}},
		{"empty value", []string{"--label", "context="}, want{code: 1, err: trace.ErrLabelEmpty.Error()}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, records := t.TempDir(), t.TempDir()
			getenv := func(k string) string { return map[string]string{"HOME": home, envRecordDir: records}[k] }
			var stdout, stderr bytes.Buffer
			code := runKnowledge(append([]string{"for", "--producer", "session"}, tc.args...),
				getenv, nil, time.Now, atTerminal, &stdout, &stderr)
			assert.Equal(t, tc.want.code, code, stderr.String())
			assert.Equal(t, tc.want.err == "", stderr.Len() == 0)
			assert.Contains(t, stderr.String(), tc.want.err)
			assert.Equal(t, tc.want.out, stdout.String())
		})
	}
}

func TestRunHookValidityContext(t *testing.T) {
	tcs := []struct {
		name, end string
	}{
		{"second boundary", "2026-10-02T01:00:01Z"},
		{"fractional boundary", "2026-10-02T01:00:00.500Z"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, records := t.TempDir(), t.TempDir()
			repo := filepath.Join(t.TempDir(), "acme")
			require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0o755))
			getenv := func(k string) string { return map[string]string{"HOME": home, envRecordDir: records}[k] }
			at := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
			now := func() time.Time { return at }
			output := filepath.Join(home, "acme.txt")
			require.NoError(t, os.WriteFile(output, []byte("acme"), 0o600))
			var stdout, stderr bytes.Buffer
			require.Equal(t, 0, runRun([]string{"record", "--producer", "session", "--label", "repo=acme",
				"--output", output}, getenv, now, &stdout, &stderr), stderr.String())
			run := strings.TrimSpace(stdout.String())
			stdout.Reset()
			require.Equal(t, 0, runKnowledge([]string{"propose", "--id", "acme", "--kind", "judgment",
				"--content", "Use the temporary presentation", "--trace", run, "--producer", "session", "--label", "repo=acme",
				"--valid-from", "2026-10-02T01:00:00Z", "--valid-until", tc.end},
				getenv, nil, now, atTerminal, &stdout, &stderr), stderr.String())
			require.Equal(t, 0, runKnowledge([]string{"approve", "acme", "--version", "1", "--approver", "ann"},
				getenv, nil, now, atTerminal, &stdout, &stderr), stderr.String())
			input, err := json.Marshal(map[string]string{"session_id": "C1", "cwd": repo, "last_assistant_message": "acme"})
			require.NoError(t, err)
			at = at.Add(time.Hour)
			stdout.Reset()
			require.Equal(t, 0, runHook([]string{"prompt"}, getenv, noStart, now, bytes.NewReader(input), &stdout, &stderr))
			assert.Contains(t, stdout.String(), "Use the temporary presentation")
			end, err := time.Parse(time.RFC3339Nano, tc.end)
			require.NoError(t, err)
			assert.Contains(t, stdout.String(), "until "+end.Format(time.RFC3339Nano))
			at, err = time.Parse(time.RFC3339Nano, tc.end)
			require.NoError(t, err)
			require.Equal(t, 0, runHook([]string{"stop"}, getenv, noStart, now, bytes.NewReader(input), &stdout, &stderr))
			stdout.Reset()
			require.Equal(t, 0, runHook([]string{"prompt"}, getenv, noStart, now, bytes.NewReader(input), &stdout, &stderr))
			assert.Contains(t, stdout.String(), "stop following inactive items acme v1")
			assert.NotContains(t, stdout.String(), "Use the temporary presentation")
			traces, err := tracefile.New(records)
			require.NoError(t, err)
			runs, err := traces.List(context.Background(), record.TraceFilter{Name: string(trace.NameRun)})
			require.NoError(t, err)
			require.NotEmpty(t, runs)
			var applied knowledge.RunInput
			require.NoError(t, json.Unmarshal(runs[0].Input, &applied))
			assert.Equal(t, []knowledge.Ref{{ID: "acme", Version: 1}}, applied.Applied)
			turns, err := traces.List(context.Background(), record.TraceFilter{Name: string(trace.NameTurn)})
			require.NoError(t, err)
			require.Len(t, turns, 2)
			var checkpoint turnInput
			require.NoError(t, json.Unmarshal(turns[0].Input, &checkpoint))
			require.NotNil(t, checkpoint.Context)
			assert.Empty(t, *checkpoint.Context)
			store, err := knowledgefile.New(records)
			require.NoError(t, err)
			rows, err := store.List(context.Background())
			require.NoError(t, err)
			require.NotEmpty(t, rows)
			assert.Equal(t, knowledge.StatusApproved, knowledge.Restore(rows[0]).Status)
			assert.Empty(t, stderr.String())
		})
	}
}
