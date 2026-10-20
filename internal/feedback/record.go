package feedback

import (
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// The verdict a store line holds as it was recorded
func Restore(r record.Feedback) Feedback {
	return Feedback{
		TraceID: r.TraceID, Time: r.Time, Verdict: Verdict(r.Verdict), ReasonCode: ReasonCode(r.ReasonCode), Reason: r.Reason,
		Edited: r.Edited, Reviewer: r.Reviewer, Proof: trace.Proof(r.Proof), Audit: r.Audit,
	}
}

// The verdicts of store lines in the order the store listed them
func RestoreAll(rs []record.Feedback) Records {
	if rs == nil {
		return nil
	}
	out := make(Records, 0, len(rs))
	for _, r := range rs {
		out = append(out, Restore(r))
	}
	return out
}

// The versions of the published schemas verdict and outcome
const (
	VerdictVersion = 1
	OutcomeVersion = 1
)

// The line a store writes for the verdict under the current version of its schema
func (f Feedback) Record() record.Feedback {
	return record.Feedback{
		TraceID: f.TraceID, Time: f.Time, Verdict: string(f.Verdict), ReasonCode: string(f.ReasonCode), Reason: f.Reason,
		Edited: f.Edited, Reviewer: f.Reviewer, Proof: string(f.Proof), Audit: f.Audit, SchemaVersion: VerdictVersion,
	}
}

// The outcome a store line holds as it was recorded
func RestoreOutcome(r record.Outcome) Outcome {
	return Outcome{
		TraceID: r.TraceID, Time: r.Time, Result: Result(r.Result), ConfirmedCause: r.ConfirmedCause, Note: r.Note,
		Reviewer: r.Reviewer,
	}
}

// The outcomes of store lines in the order the store listed them
func RestoreOutcomes(rs []record.Outcome) Outcomes {
	if rs == nil {
		return nil
	}
	out := make(Outcomes, 0, len(rs))
	for _, r := range rs {
		out = append(out, RestoreOutcome(r))
	}
	return out
}

// The line a store writes for the outcome under the current version of its schema
func (o Outcome) Record() record.Outcome {
	return record.Outcome{
		TraceID: o.TraceID, Time: o.Time, Result: string(o.Result), ConfirmedCause: o.ConfirmedCause, Note: o.Note,
		Reviewer: o.Reviewer, SchemaVersion: OutcomeVersion,
	}
}
