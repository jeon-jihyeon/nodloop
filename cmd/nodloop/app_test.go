package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/analysis"
	"github.com/jeon-jihyeon/nodloop/internal/diagnose"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
)

func TestAppPolicy(t *testing.T) {
	demo, err := os.ReadFile(filepath.Join(testkit.DemoDir(t), "policy.yaml"))
	require.NoError(t, err)
	type want struct {
		policy analysis.Policy
		limits diagnose.Limits
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
			name: "one file feeds the analyzers to analysis and the limits to diagnose",
			args: map[string]string{
				"policy.yaml": string(demo) + "limits:\n  knowledge_chars: 100\n" +
					"  example_chars: 200\n  candidates: 3\n",
			},
			want: want{
				policy: testkit.Policy(t),
				limits: diagnose.Limits{KnowledgeChars: 100, ExampleChars: 200, Candidates: 3},
			},
		},
		{
			name: "broken file is refused",
			args: map[string]string{"policy.yaml": "version: ["},
			want: want{err: analysis.ErrMalformedPolicy},
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

			policy, limits, err := app{cfg: config{dataDir: dir}}.policy()

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.policy, policy)
			assert.Equal(t, tc.want.limits, limits)
		})
	}
}

// Knowledge commands read only the limits and run on a data directory without a policy
func TestAppLimits(t *testing.T) {
	type want struct {
		limits diagnose.Limits
		err    error
	}
	tcs := []struct {
		name string
		// Files written to the data directory keyed by name
		args map[string]string
		want want
	}{
		{name: "absent file gives default limits", want: want{}},
		{
			name: "limits section is read without analyzers",
			args: map[string]string{"policy.yaml": "limits:\n  knowledge_chars: 100\n"},
			want: want{limits: diagnose.Limits{KnowledgeChars: 100}},
		},
		{
			name: "broken file is refused",
			args: map[string]string{"policy.yaml": "limits: ["},
			want: want{err: diagnose.ErrBadLimits},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			for name, content := range tc.args {
				require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600))
			}

			limits, err := app{cfg: config{dataDir: dir}}.limits()

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.limits, limits)
		})
	}
}
