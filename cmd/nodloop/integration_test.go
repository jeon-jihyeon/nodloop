package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	vetofile "github.com/jeon-jihyeon/nodloop/internal/veto/file"
)

// Real exit codes and per process latency of the built binary
func TestIntegrationBinary(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH, so the binary cannot be built")
	}
	bin := filepath.Join(t.TempDir(), "nodloop")
	out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput()
	require.NoError(t, err, "build: %s", out)
	home := t.TempDir()
	fifty, err := os.ReadFile("testdata/fifty.yaml")
	require.NoError(t, err)
	path := filepath.Join(home, vetofile.RelPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, fifty, 0o644))
	sed, err := os.ReadFile("testdata/bash_sed.json")
	require.NoError(t, err)
	plain, err := os.ReadFile("testdata/bash_plain.json")
	require.NoError(t, err)
	guard := func(t *testing.T, stdin []byte) (int, string) {
		cmd := exec.Command(bin, "guard")
		cmd.Env = append(os.Environ(), "HOME="+home)
		cmd.Stdin = bytes.NewReader(stdin)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		err := cmd.Run()
		require.NotNil(t, cmd.ProcessState, "run: %v", err)
		return cmd.ProcessState.ExitCode(), stderr.String()
	}
	type want struct {
		code   int
		stderr string
	}
	tcs := []struct {
		name  string
		stdin []byte
		want  want
	}{
		{"sed in place is blocked", sed, want{2, "blocked by veto no-sed-inplace"}},
		{"plain call passes", plain, want{0, ""}},
		{"bad input fails", []byte("nope"), want{1, "failed to parse hook input"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			code, stderr := guard(t, tc.stdin)
			assert.Equal(t, tc.want.code, code, "stderr: %s", stderr)
			assert.Contains(t, stderr, tc.want.stderr)
		})
	}
	t.Run("average run stays within the budget", func(t *testing.T) {
		const runs = 200
		start := time.Now()
		for i := range runs {
			code, stderr := guard(t, sed)
			require.Equal(t, 2, code, "run %d: stderr = %s", i, stderr)
		}
		avg := time.Since(start) / runs
		t.Logf("average process time with 51 vetoes loaded: %s (%d runs)", avg, runs)
		assert.LessOrEqual(t, avg, 50*time.Millisecond, "average exceeds the 50ms budget")
	})
}
