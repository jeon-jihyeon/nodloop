package mcp_test

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/mcp"
)

// Each skill calls only tools the agent server of the plugin serves and at least the ones it is for
// A backticked lower case word after the word call is read as a tool name
func TestSkillTools(t *testing.T) {
	called := regexp.MustCompile("[Cc]all `([a-z_]+)`")
	// Tools of the data review that the server no longer serves
	// The tools that decide for a person since the agent server never lists them
	// The commands, the mode and the handover of the draft questions the session no longer pushes
	removed := regexp.MustCompile("`(setup|infer_policy|events|observe|context|select|record|detail|pending|approve|reaffirm|approve_compaction)`|--approver|" +
		"knowledge ask|knowledge defer|hook wake|`immediate`|nodloop review question")
	type want struct {
		// Tools the skill must call
		tools []string
		// Commands the skill must run through Bash
		commands []string
		// Phrases the skill must hold
		phrases []string
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
				tools:    []string{"feedback", "propose"},
				commands: []string{"nodloop feedback list --trace", "nodloop knowledge export", "nodloop trace list --name run --session", "nodloop knowledge approve"},
				phrases:  []string{"never run it, since from here it records only a proposal"},
			},
		},
		{
			"the nod skill records verdicts on runs, proposes knowledge from them and runs the manual path over the raw records",
			"../../plugin/skills/nod/SKILL.md",
			want{
				tools: []string{
					"feedback", "propose", "extraction", "propose_extraction", "run", "compaction", "propose_compaction", "check_compaction", "intervene", "records",
				},
				commands: []string{"nodloop trace list --name run", "nodloop trace show", "nodloop knowledge for --producer session"},
				phrases: []string{
					"not a correction", "verdict `withdraw`", "action `digest`", "add no question of your own", "Never answer a nodloop question for the user",
					"never run such a command yourself", "once per thing they named",
				},
			},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b, err := os.ReadFile(tc.args)
			require.NoError(t, err)
			text := string(b)

			named := calledTools(called, text)

			assert.Subset(t, mcp.AgentTools(), named, "every called tool is served by the agent server")
			assert.Subset(t, named, tc.want.tools)
			assert.Empty(t, absent(text, "`~/.nodloop/bin/", tc.want.commands))
			assert.Empty(t, absent(text, "", tc.want.phrases))
			assert.Empty(t, removed.FindAllString(text, -1))
		})
	}
}

// The tool names the pattern captures in the text
func calledTools(called *regexp.Regexp, text string) []string {
	var out []string
	for _, m := range called.FindAllStringSubmatch(text, -1) {
		out = append(out, m[1])
	}
	return out
}

// The wanted strings the text does not hold after the prefix
func absent(text, prefix string, wants []string) []string {
	var out []string
	for _, w := range wants {
		if !strings.Contains(text, prefix+w) {
			out = append(out, w)
		}
	}
	return out
}
