package mcp_test

import (
	"os"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/mcp"
)

// Each skill calls only served tools and at least the ones it is for
// A backticked lower case word after the word call is read as a tool name
func TestSkillTools(t *testing.T) {
	called := regexp.MustCompile("[Cc]all `([a-z_]+)`")
	// Tools of the first 0.5 build that the server no longer serves
	removed := regexp.MustCompile("`(setup|infer_policy)`")
	type want struct {
		// Tools the skill must call
		tools []string
		// Commands the skill must run through Bash
		commands []string
	}
	tcs := []struct {
		name string
		args string
		want want
	}{
		{
			"the setup skill runs check and setup and calls events",
			"../../plugin/skills/setup/SKILL.md",
			want{tools: []string{"events"}, commands: []string{"nodloop check --data-dir", "nodloop setup --data-dir"}},
		},
		{"the review skill calls the review tools", "../../plugin/skills/review/SKILL.md", want{tools: []string{"context", "observe", "record", "select"}}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b, err := os.ReadFile(tc.args)
			require.NoError(t, err)
			text := string(b)
			named := map[string]bool{}
			for _, m := range called.FindAllStringSubmatch(text, -1) {
				named[m[1]] = true
			}

			for name := range named {
				assert.Contains(t, mcp.Tools(), name)
			}
			for _, name := range tc.want.tools {
				assert.True(t, named[name], name)
			}
			for _, command := range tc.want.commands {
				assert.Contains(t, text, "${CLAUDE_PLUGIN_ROOT}/bin/"+command)
			}
			assert.Empty(t, removed.FindAllString(text, -1))
		})
	}
}
