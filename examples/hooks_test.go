package examples_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The hook files a user copies run the session start and the stop of nodloop under their host
// 1. Cursor and Copilot carry no context on a prompt so neither file has a prompt hook
// 2. Cursor names the command command and Copilot names it bash
func TestHookExamples(t *testing.T) {
	type hook struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Bash    string `json:"bash"`
	}
	tcs := []struct {
		name string
		args string
		want map[string][]hook
	}{
		{"the Cursor file runs start and stop as a command", "cursor.json", map[string][]hook{
			"sessionStart": {{Command: "nodloop hook start --host cursor"}},
			"stop":         {{Command: "nodloop hook stop --host cursor"}},
		}},
		{"the Copilot file runs start and agent stop through bash", "copilot.json", map[string][]hook{
			"sessionStart": {{Type: "command", Bash: "nodloop hook start --host copilot"}},
			"agentStop":    {{Type: "command", Bash: "nodloop hook stop --host copilot"}},
		}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b, err := os.ReadFile(filepath.Join("hooks", tc.args))
			require.NoError(t, err)
			var got struct {
				Version int               `json:"version"`
				Hooks   map[string][]hook `json:"hooks"`
			}
			require.NoError(t, json.Unmarshal(b, &got))

			assert.Equal(t, 1, got.Version)
			assert.Equal(t, tc.want, got.Hooks)
		})
	}
}
