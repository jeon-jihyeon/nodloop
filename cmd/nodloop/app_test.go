package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/analysis"
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
			assert.Equal(t, tc.want.policy, policy)
		})
	}
}

// A copy of the demo data set whose export lost every row of metric
// Like an export taken while conversion tracking was down
func lostMetricDir(t *testing.T, metric string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "lost")
	require.NoError(t, os.CopyFS(dir, os.DirFS(testkit.DemoDir(t))))
	b, err := os.ReadFile(filepath.Join(dir, "events.csv"))
	require.NoError(t, err)
	var kept []string
	for _, line := range strings.SplitAfter(string(b), "\n") {
		if !strings.Contains(line, ","+metric+",") {
			kept = append(kept, line)
		}
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "events.csv"), []byte(strings.Join(kept, "")), 0o600))
	return dir
}

func TestAppServerLostMetric(t *testing.T) {
	at := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	cfg := config{dataDir: lostMetricDir(t, "conversion_count"), recordDir: t.TempDir()}
	s, err := app{cfg: cfg, now: func() time.Time { return at }}.server()
	require.NoError(t, err)
	c := testkit.Connect(t, s.ServeTransport)
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
