package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/analysis"
	"github.com/jeon-jihyeon/nodloop/internal/diagnose"
)

func TestAppPolicy(t *testing.T) {
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
			name: "absent file gives the demo policy and default limits",
			want: want{policy: analysis.DefaultPolicy()},
		},
		{
			name: "one file feeds the analyzers to analysis and the limits to diagnose",
			args: map[string]string{
				"policy.yaml": analysis.DefaultPolicyYAML + "limits:\n  knowledge_chars: 100\n" +
					"  example_chars: 200\n  candidates: 3\n",
			},
			want: want{
				policy: analysis.DefaultPolicy(),
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
