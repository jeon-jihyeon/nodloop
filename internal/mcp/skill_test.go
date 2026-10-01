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
		{
			"the sessions skill records verdicts and proposes knowledge and skips recorded traces through the CLI",
			"../../plugin/skills/sessions/SKILL.md",
			want{tools: []string{"feedback", "propose", "approve"}, commands: []string{"nodloop feedback list --trace"}},
		},
		{
			"the review skill calls the review tools and reads past traces and the report through the CLI",
			"../../plugin/skills/review/SKILL.md",
			want{
				tools:    []string{"context", "observe", "record", "select"},
				commands: []string{"nodloop trace list --subject", "nodloop trace show", "nodloop report online", "nodloop check --data-dir"},
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

// Every Bash allow rule README offers covers a command the setup skill runs
// 1. the rule starts with the command text so no wildcard comes before the subcommand
// 2. the command text is the stable link and not the versioned plugin path that changes on update
func TestReadmeAllowRules(t *testing.T) {
	readme, err := os.ReadFile("../../README.md")
	require.NoError(t, err)
	skill, err := os.ReadFile("../../plugin/skills/setup/SKILL.md")
	require.NoError(t, err)
	rules := regexp.MustCompile(`"Bash\(([^)]*) \*\)"`).FindAllStringSubmatch(string(readme), -1)
	require.Len(t, rules, 2)
	for _, rule := range rules {
		t.Run(rule[1], func(t *testing.T) {
			assert.NotContains(t, rule[1], "*")
			assert.Contains(t, string(skill), "`"+rule[1]+" ")
		})
	}
}
