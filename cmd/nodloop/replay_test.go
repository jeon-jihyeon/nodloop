package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

// replay prints a line per case and a summary, and knowledge waiting shows how the candidate fared
func TestRunKnowledgeReplay(t *testing.T) {
	at := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	tcs := []struct {
		name string
		// Whether the judge says the lesson changes the approved output
		args bool
		want []string
	}{
		{"a lesson that catches the correction alone passes", false, []string{"\tbreaks\tbreaks true\tok\t", "\tkeeps\tbreaks false\tok\t", "replay\tgit-c\tv1\tpassed\tmissed 0 of 1\toverreach 0 of 1\n", "\treplay passed\n"}},
		{"a lesson that changes an approved output fails", true, []string{"\tkeeps\tbreaks true\twrong\t", "replay\tgit-c\tv1\tfailed\tmissed 0 of 1\toverreach 1 of 1\n", "\treplay failed\n"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, records := t.TempDir(), t.TempDir()
			getenv := func(k string) string { return map[string]string{"HOME": dir, envRecordDir: records}[k] }
			// Each record a second after the one before so the runs come before the candidate
			tick := 0
			now := func() time.Time {
				tick++
				return at.Add(time.Duration(tick) * time.Second)
			}
			record := func(output, verdict string) string {
				out := filepath.Join(dir, "out.txt")
				require.NoError(t, os.WriteFile(out, []byte(output), 0o600))
				var id bytes.Buffer
				require.Equal(t, 0, runRun([]string{"record", "--producer", "session", "--label", "repo=nodloop", "--output", out}, getenv, now, &id, &bytes.Buffer{}))
				run := strings.TrimSpace(id.String())
				require.Equal(t, 0, runFeedback([]string{"add", "--trace", run, "--verdict", verdict, "--reason", "r"}, getenv, now, &bytes.Buffer{}, &bytes.Buffer{}))
				return run
			}
			corrected, approved := record("cd repo && git status", "reject"), record("git -C repo status", "approve")
			var stderr bytes.Buffer
			require.Equal(t, 0, runKnowledge([]string{"propose", "--id", "git-c", "--kind", "judgment", "--content", "Prefer git -C", "--from", corrected},
				getenv, nil, now, &bytes.Buffer{}, &stderr), stderr.String())
			client := llmmock.NewMockClient(gomock.NewController(t))
			client.EXPECT().Complete(gomock.Any(), gomock.Any()).DoAndReturn(func(context.Context, llm.Request) (llm.Response, error) {
				return llm.Response{Output: json.RawMessage(fmt.Sprintf(`{"cases":[{"run":%q,"breaks":true,"why":"it ran cd"},{"run":%q,"breaks":%t,"why":"w"}]}`,
					corrected, approved, tc.args))}, nil
			})
			var stdout, waiting bytes.Buffer

			code := runKnowledge([]string{"replay", "git-c"}, getenv, client, now, &stdout, &stderr)
			require.Equal(t, 0, runKnowledge([]string{"waiting", "--producer", "session", "--label", "repo=nodloop"}, getenv, nil, now, &waiting, &stderr))

			require.Equal(t, 0, code, stderr.String())
			for _, want := range tc.want {
				assert.Contains(t, stdout.String()+waiting.String(), want)
			}
		})
	}
}
