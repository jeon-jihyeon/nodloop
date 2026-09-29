package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

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
