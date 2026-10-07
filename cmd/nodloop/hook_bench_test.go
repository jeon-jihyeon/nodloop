package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// The prompt and stop hooks over records of many sessions
// 1. s-last answered among the newest runs
// 2. s-new has no run yet so its hook reads back to the start of sessionWindow
// A stop hook records a run so s-new takes a session id of its own on every iteration and stays the slowest case
// A run holds about 2 KB of output like a recorded answer
func BenchmarkHook(b *testing.B) {
	for _, runs := range []int{1_000, 50_000} {
		for _, hook := range []string{"prompt", "stop"} {
			for _, session := range []string{"s-last", "s-new"} {
				b.Run(fmt.Sprintf("%s/%d/%s", hook, runs, session), func(b *testing.B) {
					home, records, repo := hookHome(b, "use git -C")
					appendRuns(b, records, repo, runs)
					getenv := func(k string) string { return map[string]string{"HOME": home, envRecordDir: records}[k] }
					iteration := 0
					for b.Loop() {
						id := session
						if session == "s-new" {
							id = fmt.Sprintf("s-new-%d", iteration)
						}
						iteration++
						stdin := fmt.Sprintf(`{"session_id":%q,"cwd":%q,"prompt":"next","last_assistant_message":"done"}`, id, repo)
						require.Equal(b, 0, runHook([]string{hook}, getenv, nil, time.Now, strings.NewReader(stdin), io.Discard, io.Discard))
					}
				})
			}
		}
	}
}

// Runs of sessions of twenty runs each a minute apart up to now
// The last three are in session s-last
func appendRuns(b *testing.B, records, repo string, n int) {
	b.Helper()
	file, err := os.OpenFile(filepath.Join(records, "traces.jsonl"), os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(b, err)
	defer func() { require.NoError(b, file.Close()) }()
	enc := json.NewEncoder(file)
	output, err := json.Marshal(strings.Repeat("an answer line of a session ", 72))
	require.NoError(b, err)
	start := time.Now().Add(-time.Duration(n) * time.Minute)
	for i := range n {
		session := fmt.Sprintf("s-%d", i/20)
		if i >= n-3 {
			session = "s-last"
		}
		labels := trace.Labels{"repo": {filepath.Base(repo)}, "dir": {"."}}
		run, err := trace.NewRun(sessionProducer, "", labels, json.RawMessage(`{"applied":[]}`), output, start.Add(time.Duration(i)*time.Minute))
		require.NoError(b, err)
		run.SessionID = session
		require.NoError(b, enc.Encode(run))
	}
}
