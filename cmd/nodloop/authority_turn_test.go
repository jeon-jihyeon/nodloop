package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/markfile"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
)

func TestKnowledgeDecisionTurn(t *testing.T) {
	tcs := []struct {
		name string
		args []string
	}{
		{"approve", []string{"approve", "example", "--version", "1"}},
		{"retire", []string{"retire", "a", "--version", "1"}},
		{"reaffirm", []string{"reaffirm", "a", "--version", "1"}},
		{"approve compaction", []string{"approve-compaction"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			coverage := `{"items":[{"old":"a","covered_by":["a"]},{"old":"b","covered_by":["a"]}]}`
			getenv, records, compaction := compactionHome(t, coverage)
			store, err := tracefile.New(records)
			require.NoError(t, err)
			runs, err := store.List(t.Context(), record.TraceFilter{Name: string(trace.NameRun), Limit: 1})
			require.NoError(t, err)
			require.Len(t, runs, 1)
			var stderr bytes.Buffer
			require.Equal(t, 0, runKnowledge([]string{"propose", "--id", "example", "--kind", "judgment", "--content", "Example rule",
				"--producer", "session", "--label", "repo=nodloop", "--trace", runs[0].ID}, getenv, nil, time.Now, atTerminal, io.Discard, &stderr), stderr.String())
			args := append([]string(nil), tc.args...)
			if tc.name == "approve compaction" {
				args = append(args, compaction)
			}
			before, err := store.List(t.Context(), record.TraceFilter{Name: string(trace.NameIntervention)})
			require.NoError(t, err)
			reader, writer := io.Pipe()
			done := make(chan struct{})
			var code int
			go func() {
				defer close(done)
				defer func() { _ = writer.Close() }()
				code = runKnowledge(args, getenv, nil, time.Now, atTerminal, writer, &stderr)
			}()
			t.Cleanup(func() {
				_ = reader.Close()
				_ = writer.Close()
				<-done
			})
			_, err = io.ReadFull(reader, make([]byte, 1))
			require.NoError(t, err)

			held, err := markfile.Hold[struct{}](filepath.Join(records, curate.TurnFile))
			require.NoError(t, err)
			assert.Nil(t, held)
			if held != nil {
				require.NoError(t, held.Release())
			}
			inFlight, err := store.List(t.Context(), record.TraceFilter{Name: string(trace.NameIntervention)})
			require.NoError(t, err)
			assert.Len(t, inFlight, len(before))
			_, err = io.Copy(io.Discard, reader)
			require.NoError(t, err)
			<-done
			after, err := store.List(t.Context(), record.TraceFilter{Name: string(trace.NameIntervention)})
			require.NoError(t, err)
			assert.Equal(t, 0, code, stderr.String())
			assert.Len(t, after, len(before)+1)
		})
	}
}

func TestKnowledgeDecisionTurnFailure(t *testing.T) {
	tcs := []struct {
		name string
		args []string
	}{
		{"approve", []string{"approve", "git-c", "--version", "1"}},
		{"retire", []string{"retire", "git-c", "--version", "1"}},
		{"reaffirm", []string{"reaffirm", "git-c", "--version", "1"}},
		{"approve compaction", []string{"approve-compaction", "example"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, records, _ := hookHome(t, "Example rule")
			path := filepath.Join(records, curate.TurnFile)
			require.NoError(t, os.Remove(path))
			require.NoError(t, os.Mkdir(path, 0o700))
			getenv := envOf(map[string]string{"HOME": home, envRecordDir: records})
			var stderr bytes.Buffer

			code := runKnowledge(tc.args, getenv, nil, time.Now, atTerminal, io.Discard, &stderr)

			assert.Equal(t, 1, code)
			assert.Contains(t, stderr.String(), curate.TurnFile)
		})
	}
}
