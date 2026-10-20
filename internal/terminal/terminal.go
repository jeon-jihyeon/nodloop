// Package terminal tells a command a person typed at a terminal of their own from one an agent or a program ran
package terminal

import (
	"slices"

	"golang.org/x/term"
)

// The variables an agent session sets in the environment of every command it runs
type agentVariables []string

// One per host whose agent runs commands
// 1. Claude Code sets CLAUDECODE for its Bash calls and its hooks
// 2. Codex sets CODEX_THREAD_ID for the commands its agent runs and may run them under a pseudo terminal
// 3. Cursor sets CURSOR_AGENT for the commands of its agent terminal
// 4. Copilot CLI sets COPILOT_AGENT_SESSION_ID for its shell commands and MCP servers since 1.0.29
var agentSessions = agentVariables{"CLAUDECODE", "CODEX_THREAD_ID", "CURSOR_AGENT", "COPILOT_AGENT_SESSION_ID"}

// Whether the environment holds one of the variables
func (vs agentVariables) set(getenv func(string) string) bool {
	return slices.ContainsFunc(vs, func(name string) bool { return getenv(name) != "" })
}

// A stream a terminal may stand behind
type descriptor interface {
	Fd() uintptr
}

// Whether a person typed the command and reads its answer
// 1. stdin and stdout are an interactive terminal
// 2. no variable of an agent session is set since an agent may run commands in a terminal of its own
// A Bash call of Claude Code runs with no terminal on stdin and CLAUDECODE set
func Person(stdin, stdout any, getenv func(string) string) bool {
	return isTerminal(stdin) && isTerminal(stdout) && !agentSessions.set(getenv)
}

func isTerminal(stream any) bool {
	f, ok := stream.(descriptor)
	return ok && term.IsTerminal(int(f.Fd()))
}
