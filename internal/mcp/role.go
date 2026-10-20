package mcp

import "slices"

// What a server key may do
type Role string

const (
	RoleProducer Role = "producer" // record runs and verdicts and read the items a run receives
	RoleReviewer Role = "reviewer" // also draft and propose knowledge and read the queue, the health and the reports
	RoleApprover Role = "approver" // also approve and reaffirm that decide only under a key proving a person
)

func Roles() []Role {
	return []Role{RoleProducer, RoleReviewer, RoleApprover}
}

func (r Role) Valid() bool {
	return slices.Contains(Roles(), r)
}

var (
	producerTools = []string{"run", "knowledge_for", "feedback", "outcome", "check_call"}
	reviewerTools = []string{
		"queue", "knowledge_health", "report", "propose", "extraction", "propose_extraction", "compaction", "propose_compaction", "check_compaction",
	}
	approverTools = []string{"approve", "reaffirm", "approve_compaction"}
)

// The tools of the role, each role holding those of the role before it
func (r Role) Tools() []string {
	switch r {
	case RoleProducer:
		return producerTools
	case RoleReviewer:
		return slices.Concat(producerTools, reviewerTools)
	case RoleApprover:
		return slices.Concat(producerTools, reviewerTools, approverTools)
	}
	return nil
}

// Whether a key of the role added with --person proves a person
// Only an approver key does so a reviewer key never decides what its role leaves out
func (r Role) Proves(person bool) bool {
	return person && r == RoleApprover
}

// The tools of the agent server the plugin starts
// The reviewer role so the model of a session finds no tool that decides for a person
func AgentTools() []string {
	return RoleReviewer.Tools()
}

// Who calls the tools of one server
type Caller struct {
	// The name of the server key and empty over stdio
	// A judgment under the key is recorded under it whatever the call says and a verdict without a reviewer takes it
	Person string
	// Whether the key proves a person so a judgment under it decides at once
	Proves bool
	// Whether the model of a session calls the agent server so every verdict and outcome is recorded with reviewer session
	Agent bool
}
