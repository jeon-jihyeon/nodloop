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

	"github.com/jeon-jihyeon/nodloop/internal/consolidate"
	"github.com/jeon-jihyeon/nodloop/internal/llm"
	"github.com/jeon-jihyeon/nodloop/internal/llm/llmmock"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
)

var consolidateNow = time.Date(2026, 10, 12, 6, 0, 0, 0, time.UTC)

// A record directory with the candidates of producer session for the repo nodloop and a repo of that name to stop in
func consolidateHome(t *testing.T, candidates int, session string) (getenv func(string) string, records, repo string) {
	t.Helper()
	home, records := t.TempDir(), t.TempDir()
	repo = filepath.Join(t.TempDir(), "nodloop")
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0o755))
	getenv = func(k string) string {
		return map[string]string{"HOME": home, envRecordDir: records, envSession: session}[k]
	}
	now := func() time.Time { return consolidateNow }
	out := filepath.Join(home, "out.txt")
	require.NoError(t, os.WriteFile(out, []byte("answer"), 0o600))
	var id, stderr bytes.Buffer
	require.Equal(t, 0, runRun([]string{"record", "--producer", sessionProducer, "--label", "repo=nodloop", "--output", out}, getenv, now, &id, &stderr), stderr.String())
	for i := range candidates {
		require.Equal(t, 0, runKnowledge([]string{
			"propose", "--id", fmt.Sprintf("lesson-%d", i), "--kind", "meaning", "--content", fmt.Sprintf("acme fact %d", i),
			"--trace", strings.TrimSpace(id.String()), "--producer", sessionProducer, "--label", "repo=nodloop",
		}, getenv, nil, now, &bytes.Buffer{}, &stderr), stderr.String())
	}
	return getenv, records, repo
}

// The stop hook starts knowledge consolidate --daily only in a mode that infers when its mark is a day old and two candidates wait
func TestRunHookStopStartsTheDailyConsolidate(t *testing.T) {
	type args struct {
		candidates int
		session    string
		// consolidate.json before the stop and none when empty
		mark string
	}
	started := [][]string{{"knowledge", "consolidate", "--daily"}}
	tcs := []struct {
		name string
		args args
		want [][]string
	}{
		{"two waiting candidates and no mark start it", args{2, "", ""}, started},
		{"a mark a day old starts it", args{2, "", `{"at":"2026-10-11T06:00:00Z"}`}, started},
		{"a fresh mark starts nothing", args{2, "", `{"at":"2026-10-12T05:00:00Z"}`}, nil},
		{"one waiting candidate starts nothing", args{1, "", ""}, nil},
		{"immediate of an earlier release starts it like deferred", args{2, "immediate", ""}, started},
		{"manual starts nothing", args{2, "manual", ""}, nil},
		{"off starts nothing", args{2, "off", ""}, nil},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			getenv, records, repo := consolidateHome(t, tc.args.candidates, tc.args.session)
			if tc.args.mark != "" {
				require.NoError(t, os.WriteFile(filepath.Join(records, consolidate.MarkFile), []byte(tc.args.mark), 0o600))
			}
			var got [][]string
			start := func(args []string) error {
				got = append(got, args)
				return nil
			}
			stdin := `{"session_id":"s1","cwd":"` + repo + `","last_assistant_message":"done"}`
			var stderr bytes.Buffer

			code := runHook([]string{"stop"}, getenv, start, func() time.Time { return consolidateNow }, strings.NewReader(stdin), &bytes.Buffer{}, &stderr)

			assert.Equal(t, 0, code)
			assert.Empty(t, stderr.String())
			assert.Equal(t, tc.want, got)
		})
	}
}

// Stops within a day start one run and a stop a day later starts the next
// Each run the stops start records one trace and prints nothing
func TestRunHookStopConsolidatesOncePerDay(t *testing.T) {
	tcs := []struct {
		name string
		// The time of each stop after consolidateNow
		args []time.Duration
		// The consolidate traces and so the model calls
		want int
	}{
		{"two stops within a day run once", []time.Duration{0, time.Hour}, 1},
		{"a stop a day later runs again", []time.Duration{0, time.Hour, consolidate.Interval}, 2},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			getenv, records, repo := consolidateHome(t, 2, "")
			var started [][]string
			start := func(args []string) error {
				started = append(started, args)
				return nil
			}
			stdin := `{"session_id":"s1","cwd":"` + repo + `","last_assistant_message":"done"}`
			client := llmmock.NewMockClient(gomock.NewController(t))
			client.EXPECT().Complete(gomock.Any(), gomock.Any()).Return(llm.Response{Output: json.RawMessage(`{"groups":[]}`)}, nil).Times(tc.want)
			var stdout, stderr bytes.Buffer

			for _, after := range tc.args {
				now := func() time.Time { return consolidateNow.Add(after) }
				require.Equal(t, 0, runHook([]string{"stop"}, getenv, start, now, strings.NewReader(stdin), &bytes.Buffer{}, &stderr))
				for _, args := range started {
					require.Equal(t, 0, runKnowledge(args[1:], getenv, client, now, &stdout, &stderr), stderr.String())
				}
				started = nil
			}
			store, err := tracefile.New(records)
			require.NoError(t, err)
			traces, err := store.List(context.Background(), record.TraceFilter{Name: string(trace.NameConsolidate)})
			require.NoError(t, err)

			assert.Empty(t, stderr.String())
			assert.Empty(t, stdout.String(), "the daily run prints nothing")
			assert.Len(t, traces, tc.want)
		})
	}
}

func TestRunKnowledgeConsolidate(t *testing.T) {
	type args struct {
		flags []string
		// consolidate.json before the run and none when empty
		mark   string
		output string
		calls  int
	}
	type want struct {
		code   int
		stdout string
		stderr string
	}
	pair := `{"groups":[{"members":[{"id":"lesson-0","version":1},{"id":"lesson-1","version":1}],"why":"both state one fact"}]}`
	fresh := `{"at":"2026-10-12T05:00:00Z"}`
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a group prints one line", args{nil, "", pair, 1}, want{0, "group\tsession\tlesson-0 v1, lesson-1 v1\tboth state one fact\n", ""}},
		{"a manual run runs whatever the mark says", args{nil, fresh, `{"groups":[]}`, 1}, want{0, "", ""}},
		{"a daily run under a fresh mark asks nothing", args{[]string{"--daily"}, fresh, "", 0}, want{0, "", ""}},
		{"a daily run prints nothing", args{[]string{"--daily", "--model", "haiku"}, "", pair, 1}, want{0, "", ""}},
		{"an answer outside the schema fails", args{nil, "", `{"groups":1}`, 1}, want{1, "", "nodloop knowledge: cluster of "}},
		{"a daily run that fails says why in the log", args{[]string{"--daily"}, "", `{"groups":1}`, 1}, want{1, "", "consolidate: answer does not match the schema"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			getenv, records, _ := consolidateHome(t, 2, "")
			if tc.args.mark != "" {
				require.NoError(t, os.WriteFile(filepath.Join(records, consolidate.MarkFile), []byte(tc.args.mark), 0o600))
			}
			client := llmmock.NewMockClient(gomock.NewController(t))
			client.EXPECT().Complete(gomock.Any(), gomock.Any()).Return(llm.Response{Output: json.RawMessage(tc.args.output)}, nil).Times(tc.args.calls)
			var stdout, stderr bytes.Buffer

			code := runKnowledge(append([]string{"consolidate"}, tc.args.flags...), getenv, client, func() time.Time { return consolidateNow }, &stdout, &stderr)

			assert.Equal(t, tc.want.code, code, stderr.String())
			assert.Equal(t, tc.want.stdout, stdout.String())
			assert.Contains(t, stderr.String(), tc.want.stderr)
		})
	}
}

// A mark that cannot open or a record dir that cannot be made stops the command before any call
func TestRunKnowledgeConsolidateMarkFails(t *testing.T) {
	type args struct {
		// A directory made under the records
		dir string
		// The record dir under the records where regular is a file
		recordDir string
	}
	tcs := []struct {
		name string
		args args
		// The error on stderr
		want string
	}{
		{"a mark that is a directory fails", args{consolidate.MarkFile, "."}, consolidate.ErrMark.Error()},
		{"a record dir that is a file fails", args{"unused", "regular"}, "regular"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			getenv, records, _ := consolidateHome(t, 2, "")
			require.NoError(t, os.Mkdir(filepath.Join(records, tc.args.dir), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(records, "regular"), nil, 0o600))
			client := llmmock.NewMockClient(gomock.NewController(t))
			flags := []string{"consolidate", "--record-dir", filepath.Join(records, tc.args.recordDir)}
			var stderr bytes.Buffer

			code := runKnowledge(flags, getenv, client, func() time.Time { return consolidateNow }, &bytes.Buffer{}, &stderr)

			assert.Equal(t, 1, code)
			assert.Contains(t, stderr.String(), tc.want)
		})
	}
}
