package extract

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// Who drafted the lesson of an extraction
type Path string

const (
	PathModel        Path = "model"        // knowledge extract drafted through the configured backend
	PathConversation Path = "conversation" // the conversation drafted it and proposed it over MCP
)

// How one extraction ended
type Conclusion string

const (
	ConclusionProposed     Conclusion = "proposed"     // an add, an update or a retired version proposed again became a candidate
	ConclusionCorroborated Conclusion = "corroborated" // a waiting candidate already says it and the run joined its evidence
	ConclusionDuplicate    Conclusion = "duplicate"    // an approved item already says it
	ConclusionConflict     Conclusion = "conflict"     // an approved item or a waiting candidate says the opposite and the draft became its next version
	ConclusionRefused      Conclusion = "refused"      // the code checks or the critic or the ledger refused the last draft
	ConclusionFailed       Conclusion = "failed"       // a draft or critic call failed or did not decode so the critic never judged the last draft
)

// What refused a draft
type Refusal string

const (
	RefusalCode   Refusal = "code"   // the code checks of the lesson
	RefusalCritic Refusal = "critic" // a critic question answered false
	RefusalLedger Refusal = "ledger" // the ledger such as a widened scope
	RefusalModel  Refusal = "model"  // the draft or the critic call failed
)

// One draft and what was said of it
type Attempt struct {
	Draft    Draft     `json:"draft"`
	Critique *Critique `json:"critique,omitempty"`
	// The classify trace of the critic point that judged the draft
	// Empty when no endpoint was asked since only a plan records its answers
	Classify string  `json:"classify,omitempty"`
	Refusal  Refusal `json:"refusal,omitempty"`
	// Critic questions answered false
	Questions []string `json:"questions,omitempty"`
	Error     string   `json:"error,omitempty"`
}

func (a Attempt) refused(r Refusal, err error) Attempt {
	a.Refusal, a.Error = r, err.Error()
	return a
}

// The output of an extract trace
type Record struct {
	Attempts   []Attempt  `json:"attempts"`
	Conclusion Conclusion `json:"conclusion"`
	// The proposed or corroborated candidate version
	Candidate *knowledge.Ref `json:"candidate,omitempty"`
	// What a duplicate or a conflict or an update names
	// The status tells an item from a candidate so the next prompt can name a conflict
	Related *Related `json:"related,omitempty"`
}

// The version an extraction named as it stood then
type Related struct {
	ID      string           `json:"id"`
	Version int              `json:"version"`
	Status  knowledge.Status `json:"status"`
}

func (rec *Record) add(a Attempt) {
	rec.Attempts = append(rec.Attempts, a)
}

// The last attempt passed the checks and the ledger refused it
func (rec *Record) refuseLast(err error) {
	if n := len(rec.Attempts); n > 0 {
		rec.Attempts[n-1] = rec.Attempts[n-1].refused(RefusalLedger, err)
	}
}

// The record closed with the conclusion the result and the error make
// 1. an error after a model failure or before any draft fails the extraction
// 2. any other error refuses it
func (rec Record) end(res Result, err error) Record {
	switch {
	case err != nil && (len(rec.Attempts) == 0 || rec.Attempts[len(rec.Attempts)-1].Refusal == RefusalModel):
		rec.Conclusion = ConclusionFailed
	case err != nil:
		rec.Conclusion = ConclusionRefused
	case res.Corroborated():
		rec.Conclusion = ConclusionCorroborated
	case res.Relation == RelationConflict:
		rec.Conclusion = ConclusionConflict
	case res.Candidate != nil:
		rec.Conclusion = ConclusionProposed
	default:
		rec.Conclusion = ConclusionDuplicate
	}
	if res.Candidate != nil {
		rec.Candidate = &knowledge.Ref{ID: res.Candidate.ID, Version: res.Candidate.Version}
	}
	if res.Related != nil {
		rec.Related = &Related{ID: res.Related.ID, Version: res.Related.Version, Status: res.Related.Status}
	}
	return rec
}

// The input of an extract trace
// The verdict and its reviewer say whether a person or the conversation judged the run
type TraceInput struct {
	Verdict    string `json:"verdict"`
	ReasonCode string `json:"reason_code,omitempty"`
	Reviewer   string `json:"reviewer,omitempty"`
	// The record_id of the verdict so a reader joins it as every transport names it
	// Absent on traces of earlier releases
	VerdictID string `json:"verdict_id,omitempty"`
}

// One extraction under the id of its extract trace with what its model calls cost
type extraction struct {
	id string
	// Who proposes its candidate
	author string
	start  time.Time
	usage  trace.Usage
	// The model the drafter answered with and empty when no model was called
	model string
}

// Appends the extract trace of the reaction and returns the error of the extraction joined with any failure to record it
func (e *Extractor) record(ctx context.Context, r Reaction, path Path, rec Record, x extraction, failed error) error {
	// Only strings and numbers and bools so encoding cannot fail
	input, _ := json.Marshal(TraceInput{
		Verdict: string(r.Verdict.Verdict), ReasonCode: string(r.Verdict.ReasonCode), Reviewer: r.Verdict.Reviewer, VerdictID: r.Verdict.RecordID(),
	})
	output, _ := json.Marshal(rec)
	now := e.now()
	tr := trace.Trace{
		ID: x.id, Name: trace.NameExtract, Subject: string(path), Ref: r.Run.ID, Time: now, Model: x.model,
		Input: input, Output: output, Usage: x.usage, DurationMS: now.Sub(x.start).Milliseconds(),
	}
	if failed != nil {
		tr.Error = failed.Error()
	}
	return errors.Join(failed, e.traces.Append(ctx, tr.Record()))
}
