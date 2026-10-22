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

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/llm"
	"github.com/jeon-jihyeon/nodloop/internal/llm/llmmock"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
)

// replay prints a line per case and a summary and knowledge waiting shows how the candidate fared
func TestRunKnowledgeReplay(t *testing.T) {
	at := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	type args struct {
		// The judge says the lesson changes the approved output
		breaks bool
	}
	type want struct {
		stdout  []string
		waiting []string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a lesson that catches the correction alone passes", args{false}, want{
			[]string{"\tbreaks\tbreaks true\tok\t", "\tkeeps\tbreaks false\tok\t", "replay\tgit-c\tv1\tpassed\tmissed 0 of 1\toverreach 0 of 1\n"},
			[]string{"\treplay passed missed 0 of 1 overreach 0 of 1\n"},
		}},
		{"a lesson that changes an approved output fails", args{true}, want{
			[]string{"\tkeeps\tbreaks true\twrong\t", "replay\tgit-c\tv1\tfailed\tmissed 0 of 1\toverreach 1 of 1\n"},
			[]string{"\treplay failed missed 0 of 1 overreach 1 of 1\n"},
		}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			getenv, now, corrected, approved := replaySetup(t, at)
			client := llmmock.NewMockClient(gomock.NewController(t))
			client.EXPECT().Complete(gomock.Any(), gomock.Any()).DoAndReturn(func(context.Context, llm.Request) (llm.Response, error) {
				return llm.Response{Output: json.RawMessage(fmt.Sprintf(`{"cases":[{"run":%q,"breaks":true,"why":"it ran cd"},{"run":%q,"breaks":%t,"why":"w"}]}`,
					corrected, approved, tc.args.breaks))}, nil
			})
			var stdout, waiting, stderr bytes.Buffer

			code := runKnowledge([]string{"replay", "git-c"}, getenv, client, now, atTerminal, &stdout, &stderr)
			require.Equal(t, 0, runKnowledge([]string{"waiting", "--producer", "session", "--label", "repo=nodloop"}, getenv, nil, now, atTerminal, &waiting, &stderr), stderr.String())

			require.Equal(t, 0, code, stderr.String())
			for _, want := range tc.want.stdout {
				assert.Contains(t, stdout.String(), want)
			}
			for _, want := range tc.want.waiting {
				assert.Contains(t, waiting.String(), want)
			}
		})
	}
}

// A corrected run and an approved run of the repo and the candidate git-c taught by the first
// Each record a second after the one before so the runs come before the candidate
func replaySetup(t *testing.T, at time.Time) (getenv func(string) string, now func() time.Time, corrected, approved string) {
	t.Helper()
	dir, records := t.TempDir(), t.TempDir()
	getenv = func(k string) string { return map[string]string{"HOME": dir, envRecordDir: records}[k] }
	tick := 0
	now = func() time.Time {
		tick++
		return at.Add(time.Duration(tick) * time.Second)
	}
	record := func(output, verdict string) string {
		out := filepath.Join(dir, "out.txt")
		require.NoError(t, os.WriteFile(out, []byte(output), 0o600))
		var id bytes.Buffer
		require.Equal(t, 0, runRun([]string{"record", "--producer", "session", "--label", "repo=nodloop", "--output", out}, getenv, now, &id, &bytes.Buffer{}))
		run := strings.TrimSpace(id.String())
		require.Equal(t, 0, runFeedback([]string{"add", "--trace", run, "--verdict", verdict, "--reason", "r", "--reviewer", "ann"}, getenv, now, trace.ProofNone, &bytes.Buffer{}, &bytes.Buffer{}))
		return run
	}
	corrected, approved = record("cd repo && git status", "reject"), record("git -C repo status", "approve")
	var stderr bytes.Buffer
	require.Equal(t, 0, runKnowledge([]string{"propose", "--id", "git-c", "--kind", "judgment", "--content", "Prefer git -C", "--from", corrected},
		getenv, nil, now, atTerminal, &bytes.Buffer{}, &stderr), stderr.String())
	return getenv, now, corrected, approved
}

// waiting reads the newest replay that judged and fails on a replay trace it cannot read
func TestRunKnowledgeWaitingReplays(t *testing.T) {
	at := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	type args struct {
		// Replay traces appended oldest first after the candidate
		replays []trace.Trace
	}
	type want struct {
		code    int
		waiting string
		stderr  string
	}
	passed := json.RawMessage(`{"id":"git-c","version":1,` +
		`"cases":[{"expect":"breaks"},{"expect":"keeps"}],"missed":0,"overreach":0}`)
	partial := json.RawMessage(`{"id":"git-c","version":1,"cases":[{"expect":"breaks"}],"missed":0,"overreach":0}`)
	failed := json.RawMessage(`{"id":"git-c","version":1,"cases":[{"expect":"breaks"}],"missed":1,"overreach":0}`)
	other := json.RawMessage(`{"id":"git-c","version":2,"cases":[{"expect":"breaks"}],"missed":1,"overreach":0}`)
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"no replay shows none", args{nil}, want{0, "\treplay none\n", ""}},
		{"the newest replay of the version wins", args{[]trace.Trace{{Output: failed}, {Output: passed}}},
			want{0, "\treplay passed missed 0 of 1 overreach 0 of 1\n", ""}},
		{"a replay with no approved output to judge is partial", args{[]trace.Trace{{Output: partial}}},
			want{0, "\treplay partial missed 0 of 1 overreach 0 of 0\n", ""}},
		{"a replay of another version is not this one", args{[]trace.Trace{{Output: other}}}, want{0, "\treplay none\n", ""}},
		{"a replay that failed to judge is no judgment", args{[]trace.Trace{{Output: failed},
			{Error: "replay: the judgment is incomplete"}}},
			want{0, "\treplay failed missed 1 of 1 overreach 0 of 0\n", ""}},
		{"a replay result that does not decode fails", args{[]trace.Trace{{Output: json.RawMessage(`"cut"`)}}}, want{1, "", "replay trace does not decode"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			getenv, now, _, _ := replaySetup(t, at)
			store, err := tracefile.New(getenv(envRecordDir))
			require.NoError(t, err)
			for i, tr := range tc.args.replays {
				tr.ID, tr.Name, tr.Subject, tr.Time = fmt.Sprintf("replay-%d", i), trace.NameReplay, "git-c", now()
				require.NoError(t, store.Append(context.Background(), tr.Record()))
			}
			var stdout, stderr bytes.Buffer

			code := runKnowledge([]string{"waiting", "--producer", "session", "--label", "repo=nodloop"}, getenv, nil, now, atTerminal, &stdout, &stderr)

			assert.Equal(t, tc.want.code, code, stderr.String())
			assert.Contains(t, stdout.String(), tc.want.waiting)
			assert.Contains(t, stderr.String(), tc.want.stderr)
		})
	}
}

// check --replay replays the new item of a compaction against the corrections of the items it replaces
func TestRunKnowledgeCheckReplay(t *testing.T) {
	dir, records := t.TempDir(), t.TempDir()
	getenv := func(k string) string { return map[string]string{"HOME": dir, envRecordDir: records}[k] }
	tick := 0
	now := func() time.Time {
		tick++
		return time.Date(2026, 10, 7, 12, 0, tick, 0, time.UTC)
	}
	out := filepath.Join(dir, "out.txt")
	require.NoError(t, os.WriteFile(out, []byte("cd repo && git status"), 0o600))
	var id bytes.Buffer
	require.Equal(t, 0, runRun([]string{"record", "--producer", "session", "--label", "repo=nodloop", "--output", out}, getenv, now, &id, &bytes.Buffer{}))
	run := strings.TrimSpace(id.String())
	require.Equal(t, 0, runFeedback([]string{"add", "--trace", run, "--verdict", "reject", "--reason", "ran cd", "--reviewer", "ann"}, getenv, now, trace.ProofNone, &bytes.Buffer{}, &bytes.Buffer{}))
	var stderr bytes.Buffer
	for item, content := range map[string]string{"a": "Use git -C", "b": "Never cd before git"} {
		require.Equal(t, 0, runKnowledge([]string{"propose", "--id", item, "--kind", "judgment", "--content", content, "--from", run}, getenv, nil, now, atTerminal, &bytes.Buffer{}, &stderr), stderr.String())
		require.Equal(t, 0, runKnowledge([]string{"approve", item, "--version", "1", "--approver", "ann"}, getenv, nil, now, atTerminal, &bytes.Buffer{}, &stderr), stderr.String())
	}
	client := llmmock.NewMockClient(gomock.NewController(t))
	answers := []string{
		`{"items":[{"id":"a","kind":"judgment","content":"Use git -C and never cd before git","from":["a","b"],"producer":"session","labels":{"repo":["nodloop"]}}]}`,
		`{"items":[{"old":"a","covered_by":["a"]},{"old":"b","covered_by":["a"]}]}`,
		fmt.Sprintf(`{"cases":[{"run":%q,"breaks":false,"why":"it misses the cd"}]}`, run),
	}
	client.EXPECT().Complete(gomock.Any(), gomock.Any()).Times(3).DoAndReturn(func(context.Context, llm.Request) (llm.Response, error) {
		next := answers[0]
		answers = answers[1:]
		return llm.Response{Output: json.RawMessage(next)}, nil
	})
	var compacted bytes.Buffer
	require.Equal(t, 0, runKnowledge([]string{"compact", "a"}, getenv, client, now, atTerminal, &compacted, &stderr), stderr.String())
	compaction := ""
	for line := range strings.Lines(compacted.String()) {
		if rest, ok := strings.CutPrefix(line, "compaction\t"); ok {
			compaction = strings.TrimSpace(rest)
		}
	}
	require.NotEmpty(t, compaction)
	var stdout bytes.Buffer

	code := runKnowledge([]string{"check", compaction, "--replay"}, getenv, client, now, atTerminal, &stdout, &stderr)

	require.Equal(t, 0, code, stderr.String())
	assert.Contains(t, stdout.String(), "covered\ta v1\tby a v2\tlost -\n")
	assert.Contains(t, stdout.String(), "replay\ta\tv2\tfailed\tmissed 1 of 1\toverreach 0 of 0\n")
	assert.Contains(t, stdout.String(), "replay\tcompaction "+compaction+"\tmissed 1\toverreach 0\n")
}

// A compaction replay skips a judgment with a veto and names an item with no output to judge
// The client expects no call so neither item reaches the model
func TestCompactionCommandReplayItems(t *testing.T) {
	type want struct {
		stdout string
	}
	tcs := []struct {
		name string
		// The id of the item replayed
		args string
		want want
	}{
		{"a judgment with a veto acts through the guard and is skipped", "guard", want{""}},
		{"an item whose correction was withdrawn has no output to judge", "lone", want{"replay\tlone\tv1\tno recorded output to judge\n"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, records := t.TempDir(), t.TempDir()
			getenv := func(k string) string { return map[string]string{"HOME": dir, envRecordDir: records}[k] }
			at := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
			now := func() time.Time { return at }
			out := filepath.Join(dir, "out.txt")
			require.NoError(t, os.WriteFile(out, []byte("cd repo && git status"), 0o600))
			var id, stderr bytes.Buffer
			require.Equal(t, 0, runRun([]string{"record", "--producer", "session", "--label", "repo=nodloop", "--output", out}, getenv, now, &id, &stderr), stderr.String())
			run := strings.TrimSpace(id.String())
			require.Equal(t, 0, runFeedback([]string{"add", "--trace", run, "--verdict", "reject", "--reason", "ran cd", "--reviewer", "ann"}, getenv, now, trace.ProofNone, &bytes.Buffer{}, &stderr), stderr.String())
			for _, args := range [][]string{
				{"propose", "--id", "lone", "--kind", "judgment", "--content", "Use git -C", "--from", run},
				{"propose", "--id", "guard", "--kind", "judgment", "--content", "Never cd", "--from", run,
					"--veto-tool", "Bash", "--veto-field", "command", "--veto-match", `^cd\b`, "--veto-example", `{"command":"cd repo"}`},
			} {
				require.Equal(t, 0, runKnowledge(args, getenv, nil, now, atTerminal, &bytes.Buffer{}, &stderr), stderr.String())
			}
			require.Equal(t, 0, runFeedback([]string{"add", "--trace", run, "--verdict", "withdraw", "--reviewer", "ann"}, getenv, now, trace.ProofNone, &bytes.Buffer{}, &stderr), stderr.String())
			a, err := recordFlags{}.app(getenv, now)
			require.NoError(t, err)
			ledger, err := a.ledger()
			require.NoError(t, err)
			r, err := a.replayer(ledger, llmmock.NewMockClient(gomock.NewController(t)), "")
			require.NoError(t, err)
			all, err := ledger.All(context.Background())
			require.NoError(t, err)
			item, err := all.Version(tc.args, 1)
			require.NoError(t, err)
			var stdout bytes.Buffer

			missed, overreach, err := compactionCommand{ledger: ledger, out: &stdout}.replayItems(context.Background(), r, knowledge.Set{item})

			require.NoError(t, err)
			assert.Zero(t, missed)
			assert.Zero(t, overreach)
			assert.Equal(t, tc.want.stdout, stdout.String())
		})
	}
}

func TestRunKnowledgeReplayFailures(t *testing.T) {
	t.Parallel()
	type args struct {
		id      string
		blocked string
		model   bool
	}
	tcs := []struct {
		name string
		args args
		want string
	}{
		{name: "id required", want: "replay: an id is required"},
		{name: "ledger cannot open", args: args{id: "git-c", blocked: "knowledge.jsonl"}, want: "knowledge file"},
		{name: "trace store cannot open", args: args{id: "git-c", blocked: "traces.jsonl"}, want: "trace file"},
		{name: "feedback store cannot open", args: args{id: "git-c", blocked: "feedback.jsonl"}, want: "feedback file"},
		{name: "item does not exist", args: args{id: "missing"}, want: "no current"},
		{name: "model refuses the judgment", args: args{id: "git-c", model: true}, want: llm.ErrClaudeFailed.Error()},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			at := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
			getenv, now, _, _ := replaySetup(t, at)
			if tc.args.blocked != "" {
				path := filepath.Join(getenv(envRecordDir), tc.args.blocked)
				require.NoError(t, os.WriteFile(path+".tmp", nil, 0o600))
				require.NoError(t, os.Remove(path+".lock"))
				require.NoError(t, os.Mkdir(path+".lock", 0o700))
			}
			client := llmmock.NewMockClient(gomock.NewController(t))
			if tc.args.model {
				client.EXPECT().Complete(gomock.Any(), gomock.Any()).Return(llm.Response{}, llm.ErrClaudeFailed)
			}
			args := []string{"replay"}
			if tc.args.id != "" {
				args = append(args, tc.args.id)
			}
			var stdout, stderr bytes.Buffer
			code := runKnowledge(args, getenv, client, now, atTerminal, &stdout, &stderr)
			assert.Equal(t, 1, code)
			assert.Empty(t, stdout.String())
			assert.Contains(t, stderr.String(), tc.want)
		})
	}
}

func TestRunKnowledgeWaitingReplayFailures(t *testing.T) {
	t.Parallel()
	tcs := []struct {
		name string
		open bool
	}{
		{name: "trace store cannot open", open: true},
		{name: "trace records cannot be read"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			at := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
			getenv, now, _, _ := replaySetup(t, at)
			path := filepath.Join(getenv(envRecordDir), "traces.jsonl")
			if tc.open {
				require.NoError(t, os.WriteFile(path+".tmp", nil, 0o600))
				require.NoError(t, os.Remove(path+".lock"))
				require.NoError(t, os.Mkdir(path+".lock", 0o700))
			} else {
				require.NoError(t, os.Remove(path))
				require.NoError(t, os.Mkdir(path, 0o700))
			}
			var stdout, stderr bytes.Buffer
			code := runKnowledge([]string{"waiting", "--producer", "session", "--label", "repo=nodloop"},
				getenv, nil, now, atTerminal, &stdout, &stderr)
			assert.Equal(t, 1, code)
			assert.Empty(t, stdout.String())
			assert.Contains(t, stderr.String(), "trace file")
		})
	}
}

func TestRunKnowledgeCheckReplayFailures(t *testing.T) {
	t.Parallel()
	tcs := []struct {
		name string
		err  error
	}{
		{name: "model failed", err: llm.ErrClaudeFailed},
		{name: "model timed out", err: context.DeadlineExceeded},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			at := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
			getenv, now, corrected, _ := replaySetup(t, at)
			var stderr bytes.Buffer
			commands := [][]string{
				{"propose", "--id", "git-second", "--kind", "judgment", "--content", "Avoid changing folders", "--from", corrected},
				{"approve", "git-c", "--version", "1", "--approver", "ann"},
				{"approve", "git-second", "--version", "1", "--approver", "ann"},
			}
			for _, args := range commands {
				require.Equal(t, 0, runKnowledge(args, getenv, nil, now, atTerminal, &bytes.Buffer{}, &stderr), stderr.String())
			}
			client := llmmock.NewMockClient(gomock.NewController(t))
			draft := `{"items":[{"id":"git-c","kind":"judgment","content":"Use git -C without changing folders",` +
				`"from":["git-c","git-second"],"producer":"session","labels":{"repo":["nodloop"]}}]}`
			coverage := `{"items":[{"old":"git-c","covered_by":["git-c"]},` +
				`{"old":"git-second","covered_by":["git-c"]}]}`
			gomock.InOrder(
				client.EXPECT().Complete(gomock.Any(), gomock.Any()).Return(llm.Response{Output: json.RawMessage(draft)}, nil),
				client.EXPECT().Complete(gomock.Any(), gomock.Any()).Return(llm.Response{Output: json.RawMessage(coverage)}, nil),
				client.EXPECT().Complete(gomock.Any(), gomock.Any()).Return(llm.Response{}, tc.err),
			)
			var compacted bytes.Buffer
			require.Equal(t, 0, runKnowledge([]string{"compact", "git-c"}, getenv, client, now,
				atTerminal, &compacted, &stderr), stderr.String())
			lines := strings.Split(compacted.String(), "\n")
			require.GreaterOrEqual(t, len(lines), 2)
			compaction, found := strings.CutPrefix(lines[1], "compaction\t")
			require.True(t, found, compacted.String())
			var stdout bytes.Buffer
			code := runKnowledge([]string{"check", compaction, "--replay"}, getenv, client, now,
				atTerminal, &stdout, &stderr)
			assert.Equal(t, 1, code)
			assert.Contains(t, stderr.String(), tc.err.Error())
			assert.NotContains(t, stdout.String(), "replay\tcompaction")
		})
	}
}
