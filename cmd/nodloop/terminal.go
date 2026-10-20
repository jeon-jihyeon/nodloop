package main

import (
	"github.com/jeon-jihyeon/nodloop/internal/terminal"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// The name a decision of a person at the terminal is recorded under when neither --approver nor config approver names one
const unnamedPerson = "person"

// The terminal proof of a person for the commands that decide
// nodloop-server reads the same rule of a person at a terminal for a key a person holds
func terminalProof(stdin, stdout any, getenv func(string) string) trace.Proof {
	if !terminal.Person(stdin, stdout, getenv) {
		return trace.ProofNone
	}
	return trace.ProofTerminal
}
