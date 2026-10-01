package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/analysis"
	"github.com/jeon-jihyeon/nodloop/internal/mcp"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
)

func TestAppPolicy(t *testing.T) {
	demo, err := os.ReadFile(filepath.Join(testkit.DemoDir(t), "policy.yaml"))
	require.NoError(t, err)
	type want struct {
		policy analysis.Policy
		err    error
	}
	tcs := []struct {
		name string
		// Files written to the data directory keyed by name
		args map[string]string
		want want
	}{
		{
			name: "absent file is missing",
			want: want{err: errPolicyMissing},
		},
		{
			name: "the analyzers are read",
			args: map[string]string{"policy.yaml": string(demo)},
			want: want{policy: testkit.Policy(t)},
		},
		{
			name: "a limits section left from before the caps were internal is refused",
			args: map[string]string{
				"policy.yaml": string(demo) + "limits:\n  knowledge_chars: 100\n" +
					"  example_chars: 200\n  candidates: 3\n",
			},
			want: want{err: analysis.ErrLimitsSection},
		},
		{
			name: "broken file is refused",
			args: map[string]string{"policy.yaml": "version: ["},
			want: want{err: analysis.ErrMalformedPolicy},
		},
		{
			name: "a policy path that is a directory fails to read",
			args: map[string]string{"policy.yaml/x": ""},
			want: want{err: syscall.EISDIR},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			for name, content := range tc.args {
				path := filepath.Join(dir, name)
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
				require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
			}

			policy, err := app{cfg: config{dataDir: dir}}.policy()

			assert.ErrorIs(t, err, tc.want.err)
			// The runner of the data directory is checked by TestCommandRunner
			read := analysis.Policy{Version: policy.Version, Contexts: policy.Contexts, Analyzers: policy.Analyzers}
			assert.Equal(t, tc.want.policy, read)
		})
	}
}

func TestCommandRunner(t *testing.T) {
	type args struct {
		argv    []string
		timeout time.Duration
	}
	type want struct {
		stdout string
		err    error
		// Regexp matched against the error text
		msg string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"a relative script resolves against the data directory and reads stdin",
			args{[]string{"./analyzers/echo.sh", "first"}, time.Minute},
			want{stdout: "first {\"event_id\":\"ev-1\"}\n", msg: `^$`},
		},
		{
			"a non zero exit names stderr",
			args{[]string{"./analyzers/fail.sh"}, time.Minute},
			want{err: analysis.ErrCommandFailed, msg: `exit status 3: no data for ev-1$`},
		},
		{
			"a run past its timeout names the timeout",
			args{[]string{"./analyzers/slow.sh"}, 50 * time.Millisecond},
			want{err: analysis.ErrCommandTimeout, msg: `timed out: after 50ms$`},
		},
		{
			"a command that does not exist fails",
			args{[]string{"./analyzers/missing.sh"}, time.Minute},
			want{err: analysis.ErrCommandFailed, msg: `no such file or directory`},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, err := commandRunner{dir: "testdata/commands"}.Run(tc.args.argv, []byte(`{"event_id":"ev-1"}`), tc.args.timeout)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.stdout, string(out))
			msg := ""
			if err != nil {
				msg = err.Error()
			}
			assert.Regexp(t, tc.want.msg, msg)
		})
	}
}

// A copy of the demo data set whose export lost every row of metric
// Like an export taken while conversion tracking was down
func lostMetricDir(t *testing.T, metric string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "lost")
	require.NoError(t, os.CopyFS(dir, os.DirFS(testkit.DemoDir(t))))
	dropMetric(t, dir, metric)
	return dir
}

// Removes every events.csv row of metric
// An empty metric keeps every row
func dropMetric(t *testing.T, dir, metric string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "events.csv"))
	require.NoError(t, err)
	var kept []string
	for _, line := range strings.SplitAfter(string(b), "\n") {
		if metric == "" || !strings.Contains(line, ","+metric+",") {
			kept = append(kept, line)
		}
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "events.csv"), []byte(strings.Join(kept, "")), 0o600))
}

// The server reads the events per call and never at start
func TestAppServerReadsEventsPerCall(t *testing.T) {
	at := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	badEvent := "tq-001,yesterday,source-a,shopping,click_count,1\n"
	type args struct {
		// Rows appended before the server starts keyed by file name
		rows map[string]string
		// The metric an export loses after the server started
		lost  string
		tool  string
		input map[string]any
	}
	type want struct {
		// Regexp matched against the text answer followed by the error
		answer string
		err    error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			name: "a bad events row leaves queue answering",
			args: args{rows: map[string]string{"events.csv": badEvent}, tool: "queue", input: map[string]any{}},
			want: want{answer: `^\{"items"`},
		},
		{
			name: "a bad events row fails events naming its line",
			args: args{rows: map[string]string{"events.csv": badEvent}, tool: "events", input: map[string]any{}},
			want: want{answer: `events.csv line \d+ column timestamp`, err: testkit.ErrTool},
		},
		{
			name: "a bad contexts row leaves queue answering",
			args: args{rows: map[string]string{"contexts.csv": "tq-001,not_a_context\n"}, tool: "queue", input: map[string]any{}},
			want: want{answer: `^\{"items"`},
		},
		{
			name: "a metric lost after the start is named by observe",
			args: args{lost: "conversion_count", tool: "observe", input: map[string]any{"event_id": "tq-017"}},
			want: want{answer: `conversion_count: no event of the data set carries this metric`},
		},
		{
			name: "a metric lost after the start is named in the context",
			args: args{lost: "conversion_count", tool: "context", input: map[string]any{"event_id": "tq-017"}},
			want: want{answer: `conversion_count: no event of the data set carries this metric`},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := filepath.Join(t.TempDir(), "data")
			require.NoError(t, os.CopyFS(dir, os.DirFS(testkit.DemoDir(t))))
			for name, rows := range tc.args.rows {
				b, err := os.ReadFile(filepath.Join(dir, name))
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(dir, name), append(b, rows...), 0o600))
			}
			c := connectApp(t, app{cfg: config{dataDir: dir, recordDir: t.TempDir()}, now: func() time.Time { return at }})
			dropMetric(t, dir, tc.args.lost)

			got, err := c.Text(t, tc.args.tool, tc.args.input)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Regexp(t, tc.want.answer, fmt.Sprint(got, err))
		})
	}
}

func TestAppServerLostMetric(t *testing.T) {
	at := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	cfg := config{dataDir: lostMetricDir(t, "conversion_count"), recordDir: t.TempDir()}
	c := connectApp(t, app{cfg: cfg, now: func() time.Time { return at }})
	event := map[string]any{"event_id": "tq-017"}
	type args struct {
		tool  string
		input map[string]any
	}
	tcs := []struct {
		name string
		args args
		// Regexp matched against the text answer
		want string
	}{
		{"events lists the events", args{"events", map[string]any{}}, `"id":"tq-017"`},
		{"observe names the lost metric", args{"observe", event}, `conversion_count: no event of the data set carries this metric`},
		{
			"context keeps the procedures of the lost metric",
			args{"context", event},
			`"procedures":\["[^"]+","[^"]+","[^"]+","[^"]+"\]`,
		},
		{"context shows the review the lost metric", args{"context", event}, `conversion_count: no event of the data set carries this metric`},
		{"queue answers", args{"queue", map[string]any{}}, `^\{`},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			got, err := c.Text(t, tc.args.tool, tc.args.input)
			require.NoError(t, err)
			assert.Regexp(t, tc.want, got)
		})
	}
}

// The host over one config as runMCP builds it
func connectApp(t *testing.T, a app) testkit.Client {
	t.Helper()
	session := mcp.NewSession(a.now())
	return testkit.Connect(t, mcp.NewHost(func(context.Context) (*mcp.Server, error) { return a.server(session) }, "test").ServeTransport)
}
