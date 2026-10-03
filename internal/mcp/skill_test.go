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
	// Tools of the data review that the server no longer serves
	removed := regexp.MustCompile("`(setup|infer_policy|events|observe|context|select|record|detail|pending)`")
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
			"the sessions skill records verdicts and proposes knowledge and runs the list and the export through the CLI",
			"../../plugin/skills/sessions/SKILL.md",
			want{
				tools:    []string{"feedback", "propose", "approve"},
				commands: []string{"nodloop feedback list --trace", "nodloop knowledge export", "nodloop trace list --name run --session"},
			},
		},
		{
			"the nod skill records verdicts on runs, proposes knowledge from them and reviews the waiting drafts",
			"../../plugin/skills/nod/SKILL.md",
			want{
				tools: []string{"feedback", "propose", "extraction", "propose_extraction", "approve", "run", "compaction", "propose_compaction", "check_compaction", "approve_compaction"},
				commands: []string{"nodloop trace list --name run", "nodloop trace show", "nodloop knowledge for --producer session",
					"nodloop knowledge waiting --producer session", "nodloop feedback list --trace", "nodloop knowledge retire"},
			},
		},
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
				assert.Contains(t, text, "`~/.nodloop/bin/"+command)
			}
			assert.Empty(t, removed.FindAllString(text, -1))
		})
	}
}
