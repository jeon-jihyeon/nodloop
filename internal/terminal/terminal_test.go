package terminal

import (
	"bytes"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A stream that is no terminal or the environment of an agent session is no person
// A terminal itself needs a person to stand behind it and a test cannot be one
func TestPerson(t *testing.T) {
	r, w, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
	type args struct {
		stdin, stdout any
		env           map[string]string
	}
	tcs := []struct {
		name string
		args args
		want bool
	}{
		{"buffers are no terminal", args{&bytes.Buffer{}, &bytes.Buffer{}, nil}, false},
		{"a pipe is no terminal", args{r, w, nil}, false},
		{"nothing is no terminal", args{nil, nil, nil}, false},
		{"an agent session is no person", args{r, w, map[string]string{"CLAUDECODE": "1"}}, false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := Person(tc.args.stdin, tc.args.stdout, func(k string) string { return tc.args.env[k] })

			assert.Equal(t, tc.want, got)
		})
	}
}

// The variable of every host whose agent runs commands marks an agent session
// Person reads it only behind a terminal so the list is checked on its own
func TestAgentVariablesSet(t *testing.T) {
	tcs := []struct {
		name string
		args map[string]string
		want bool
	}{
		{"no variable is a person's shell", map[string]string{"TERM": "xterm"}, false},
		{"an empty variable is unset", map[string]string{"CLAUDECODE": ""}, false},
		{"a Bash call of Claude Code", map[string]string{"CLAUDECODE": "1"}, true},
		{"a command the agent of Codex runs", map[string]string{"CODEX_THREAD_ID": "t1"}, true},
		{"a command of the agent terminal of Cursor", map[string]string{"CURSOR_AGENT": "1"}, true},
		{"a shell command of Copilot CLI", map[string]string{"COPILOT_AGENT_SESSION_ID": "c1"}, true},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := agentSessions.set(func(k string) string { return tc.args[k] })

			assert.Equal(t, tc.want, got)
		})
	}
}
