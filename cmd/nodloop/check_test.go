package main

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/analysis"
	"github.com/jeon-jihyeon/nodloop/internal/evidence"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
)

func TestRunCheck(t *testing.T) {
	demoProfile := &analysis.Profile{
		Points: 48, Cadence: "1h0m0s",
		Metrics:    []analysis.MetricKind{{Name: "click_count", Count: true}, {Name: "conversion_count", Count: true}},
		Dimensions: []analysis.DimensionKind{{Name: "source", Values: 3}, {Name: "topic", Values: 1}},
	}
	type args struct {
		// Files written over a copy of the demo keyed by their relative path
		// An empty content removes the file
		files map[string]string
		// Relative to the copy when it names a file of it
		policy string
	}
	type want struct {
		code    int
		profile *analysis.Profile
		policy  *policySummary
		// The declared contexts by name
		contexts []evidence.Context
		// A fragment of the error or empty
		err string
	}
	defaults := evidence.DefaultContexts().Names()
	draft := "version: draft-1\ncontexts:\n  - name: deploy\n" + analyzersOf(t)
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"the demo prints its profile and policy",
			args{},
			want{profile: demoProfile, policy: &policySummary{Version: "demo-1", Analyzers: 4}, contexts: defaults},
		},
		{
			"a draft at another path is checked in place of policy.yaml",
			args{files: map[string]string{"policy.yaml": "", "contexts.csv": "event_id,change_context\ntq-001,deploy\n", "draft.yaml": draft}, policy: "draft.yaml"},
			want{profile: demoProfile, policy: &policySummary{Version: "draft-1", Analyzers: 4}, contexts: []evidence.Context{"deploy", evidence.ContextUnknown}},
		},
		{
			"no policy still prints the profile",
			args{files: map[string]string{"policy.yaml": ""}},
			want{code: 1, profile: demoProfile, contexts: defaults, err: errPolicyMissing.Error()},
		},
		{
			"a metric no event carries fails",
			args{files: map[string]string{"policy.yaml": strings.Replace(demoPolicy(t), "[click_count]", "[clicks]", 1)}},
			want{code: 1, profile: demoProfile, policy: &policySummary{Version: "demo-1", Analyzers: 4}, contexts: defaults, err: analysis.ErrPolicyUnobserved.Error()},
		},
		{
			"a contexts.csv value outside the declared set fails naming the set",
			args{files: map[string]string{"contexts.csv": "event_id,change_context\ntq-001,deploy\n"}},
			want{code: 1, contexts: defaults, err: evidence.ErrUnknownContext.Error() + `: contexts.csv line 2: "deploy" is not one of [no_known_change`},
		},
		{
			"a procedure scope naming an undeclared context fails",
			args{files: map[string]string{"procedures/deploy.md": "---\nchange_contexts: [deploy]\n---\n# Deploy\n\n## Check\n\nLook.\n"}},
			want{code: 1, profile: demoProfile, contexts: defaults, err: evidence.ErrUnknownContext.Error()},
		},
		{
			"a procedure scope naming a metric no event carries fails",
			args{files: map[string]string{"procedures/latency.md": "---\nmetrics: [latency_ms]\n---\n# Latency\n\n## Check\n\nLook.\n"}},
			want{code: 1, profile: demoProfile, contexts: defaults, err: errScopeUnobserved.Error() + ": latency.md names latency_ms"},
		},
		{
			"a bad labels line fails",
			args{files: map[string]string{"labels.jsonl": "{\n"}},
			want{code: 1, profile: demoProfile, contexts: defaults, err: "labels.jsonl"},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := filepath.Join(t.TempDir(), "data")
			require.NoError(t, os.CopyFS(dir, os.DirFS(testkit.DemoDir(t))))
			for rel, content := range tc.args.files {
				path := filepath.Join(dir, rel)
				if content == "" {
					require.NoError(t, os.Remove(path))
					continue
				}
				require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
			}
			args := []string{"--data-dir", dir}
			if tc.args.policy != "" {
				args = append(args, "--policy", filepath.Join(dir, tc.args.policy))
			}
			before := snapshotDir(t, dir)
			var stdout, stderr bytes.Buffer

			code := runCheck(args, &stdout, &stderr)

			assert.Equal(t, tc.want.code, code)
			assert.Empty(t, stderr.String())
			var got dataReport
			require.NoError(t, json.Unmarshal(stdout.Bytes(), &got), stdout.String())
			assert.Equal(t, dir, got.DataDir)
			assert.Equal(t, tc.want.profile, got.Profile)
			assert.Equal(t, tc.want.policy, got.Policy)
			assert.Equal(t, tc.want.contexts, got.Contexts.Names())
			if tc.want.err == "" {
				assert.Empty(t, got.Error)
			} else {
				assert.Contains(t, got.Error, tc.want.err)
			}
			assert.Equal(t, before, snapshotDir(t, dir), "check wrote into the data dir")
		})
	}
}

func TestRunCheckDemoReport(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := runCheck([]string{"--data-dir", testkit.DemoDir(t)}, &stdout, &stderr)

	require.Equal(t, 0, code, stderr.String())
	var got dataReport
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &got))
	assert.Equal(t, 24, got.Events)
	assert.Len(t, got.Procedures, 4)
	assert.Equal(t, procedureSummary{Slug: "outcome-rate-degradation"}, got.Procedures[2])
	assert.Equal(t, []string{}, got.Warnings)
}

func TestRunCheckUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := runCheck(nil, &stdout, &stderr)

	assert.Equal(t, 1, code)
	assert.Empty(t, stdout.String())
	assert.Contains(t, stderr.String(), "nodloop check: --data-dir is required")
}

func demoPolicy(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(testkit.DemoDir(t), policyFile))
	require.NoError(t, err)
	return string(b)
}

// The analyzers of the demo policy
func analyzersOf(t *testing.T) string {
	t.Helper()
	_, analyzers, ok := strings.Cut(demoPolicy(t), "analyzers:")
	require.True(t, ok)
	return "analyzers:" + analyzers
}

// Every file of a dir with its content
func snapshotDir(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	require.NoError(t, filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		out[path] = string(b)
		return err
	}))
	return out
}
