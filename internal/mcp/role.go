package mcp

import "slices"

// What a server key may do
type Role string

const (
	RoleProducer Role = "producer" // record runs and verdicts and read the items a run receives
	RoleReviewer Role = "reviewer" // also draft and propose knowledge and read the queue, the health and the reports
	RoleApprover Role = "approver" // also approve and reaffirm under the name of its key
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
