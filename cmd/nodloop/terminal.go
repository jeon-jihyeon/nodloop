package main

import (
	"golang.org/x/term"

	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// The variables an agent session sets in the environment of every command it runs
// Claude Code sets CLAUDECODE for its Bash calls and its hooks
var agentSessions = []string{"CLAUDECODE"}

// The name a decision of a person at the terminal is recorded under when neither --approver nor config approver names one
const unnamedPerson = "person"

// A stream a terminal may stand behind
type descriptor interface {
	Fd() uintptr
}

// The terminal proof of a person for the commands that decide
// 1. stdin and stdout are an interactive terminal so a person types the command and reads its answer
// 2. no variable of an agent session is set since an agent may run commands in a terminal of its own
// A Bash call of Claude Code runs with no terminal on stdin and CLAUDECODE set
func terminalProof(stdin, stdout any, getenv func(string) string) trace.Proof {
	if !isTerminal(stdin) || !isTerminal(stdout) {
		return trace.ProofNone
	}
	for _, name := range agentSessions {
		if getenv(name) != "" {
			return trace.ProofNone
		}
	}
	return trace.ProofTerminal
}

func isTerminal(stream any) bool {
	f, ok := stream.(descriptor)
	return ok && term.IsTerminal(int(f.Fd()))
}
