package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	vetofile "github.com/jeon-jihyeon/nodloop/internal/veto/file"
)

func integrationFixture(t testing.TB) (bin, home string, sed, plain []byte) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH, so the binary cannot be built")
	}
	bin = filepath.Join(t.TempDir(), "nodloop")
	out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput()
	require.NoError(t, err, "build: %s", out)
	home = t.TempDir()
	fifty, err := os.ReadFile("testdata/fifty.yaml")
	require.NoError(t, err)
	path := filepath.Join(home, vetofile.RelPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, fifty, 0o644))
	sed, err = os.ReadFile("testdata/bash_sed.json")
	require.NoError(t, err)
	plain, err = os.ReadFile("testdata/bash_plain.json")
	require.NoError(t, err)
	project := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(project, ".git"), 0o700))
	local := func(data []byte) []byte {
		var input map[string]any
		require.NoError(t, json.Unmarshal(data, &input))
		input["cwd"] = project
		out, err := json.Marshal(input)
		require.NoError(t, err)
		return out
	}
	return bin, home, local(sed), local(plain)
}

func guardProcess(t testing.TB, bin, home string, stdin []byte) (int, string) {
	t.Helper()
	cmd := exec.Command(bin, "guard")
	// Only the fixture home reaches the binary so no host agent or record variable changes its authority
	cmd.Env = []string{"HOME=" + home}
	cmd.Stdin = bytes.NewReader(stdin)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	require.NotNil(t, cmd.ProcessState, "run: %v", err)
	return cmd.ProcessState.ExitCode(), stderr.String()
}

func TestIntegrationBinary(t *testing.T) {
	bin, home, sed, plain := integrationFixture(t)
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
			code, stderr := guardProcess(t, bin, home, tc.stdin)
			assert.Equal(t, tc.want.code, code, "stderr: %s", stderr)
			assert.Contains(t, stderr, tc.want.stderr)
		})
	}
}

func BenchmarkIntegrationGuard(b *testing.B) {
	bin, home, sed, _ := integrationFixture(b)
	for b.Loop() {
		code, stderr := guardProcess(b, bin, home, sed)
		require.Equal(b, 2, code, "stderr: %s", stderr)
	}
}
