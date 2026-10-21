package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
)

func TestRunHookReactionTurnBoundary(t *testing.T) {
	type args struct {
		pending, unwritable bool
	}
	tcs := []struct {
		name string
		args args
		want int
	}{
		{"a completed answer receives the reaction", args{}, 1},
		{"an unfinished turn blocks an older answer", args{pending: true}, 0},
		{"a turn that cannot be recorded judges no older answer", args{unwritable: true}, 0},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.args.unwritable && (runtime.GOOS == "windows" || os.Geteuid() == 0) {
				t.Skip("a read only file stops no write on windows or as root")
			}
			home, records, repo := hookHome(t, "use git -C")
			getenv := func(k string) string { return map[string]string{"HOME": home, envRecordDir: records}[k] }
			now := hookClock()
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				_, _ = w.Write([]byte(`{"answers":{"corrects":{"noul":0.05},"approves":{"noul":0.95}}}`))
			}))
			t.Cleanup(srv.Close)
			require.Equal(t, 0, runClassifier([]string{"set", "reaction", "--url", srv.URL}, getenv, now, atTerminal, &bytes.Buffer{}, &bytes.Buffer{}))
			hookPromptSetup(t, getenv, now, repo, true, false, false)
			store, err := tracefile.New(records)
			require.NoError(t, err)
			if tc.args.pending {
				at := now()
				tr := trace.Trace{ID: trace.NewID(at), Name: trace.NameTurn, SessionID: "s1", Time: at, Input: json.RawMessage(`{}`)}
				require.NoError(t, store.Append(context.Background(), tr.Record()))
			}
			if tc.args.unwritable {
				require.NoError(t, os.Chmod(filepath.Join(records, "traces.jsonl"), 0o400))
			}
			var stderr bytes.Buffer
			stdin := `{"session_id":"s1","cwd":"` + repo + `","prompt":"right"}`

			code := runHook([]string{"prompt"}, getenv, noStart, now, strings.NewReader(stdin), &bytes.Buffer{}, &stderr)

			require.Equal(t, 0, code, stderr.String())
			found, err := store.List(context.Background(), record.TraceFilter{Name: string(trace.NameClassify)})
			require.NoError(t, err)
			assert.Len(t, found, tc.want)
			assert.Equal(t, int32(tc.want), calls.Load())
		})
	}
}
