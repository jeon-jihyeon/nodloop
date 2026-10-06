package extract

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// Who drafted the lesson of an extraction
type Path string

const (
	PathModel        Path = "model"        // knowledge extract drafted through claude -p
	PathConversation Path = "conversation" // the conversation drafted it and proposed it over MCP
)

// How one extraction ended
type Conclusion string

const (
	ConclusionProposed  Conclusion = "proposed"  // an add or an update became a candidate
	ConclusionDuplicate Conclusion = "duplicate" // an approved item already says it
	ConclusionConflict  Conclusion = "conflict"  // an approved item says the opposite
	ConclusionRefused   Conclusion = "refused"   // the code checks or the critic or the ledger refused the last draft
	ConclusionFailed    Conclusion = "failed"    // a model call failed so no draft was judged
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
	Refusal  Refusal   `json:"refusal,omitempty"`
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
	Attempts   []Attempt      `json:"attempts"`
	Conclusion Conclusion     `json:"conclusion"`
	Candidate  *knowledge.Ref `json:"candidate,omitempty"`
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
	case res.Candidate != nil:
		rec.Conclusion = ConclusionProposed
		rec.Candidate = &knowledge.Ref{ID: res.Candidate.ID, Version: res.Candidate.Version}
	case res.Relation == RelationConflict:
		rec.Conclusion = ConclusionConflict
	default:
		rec.Conclusion = ConclusionDuplicate
	}
	return rec
}

// The input of an extract trace
// The verdict and its reviewer say whether a person or the conversation judged the run
type recordInput struct {
	Verdict    string `json:"verdict"`
	ReasonCode string `json:"reason_code,omitempty"`
	Reviewer   string `json:"reviewer,omitempty"`
}

// Appends the extract trace of the reaction and returns the error of the extraction joined with any failure to record it
func (e *Extractor) record(ctx context.Context, r Reaction, path Path, rec Record, failed error) error {
	input, err := json.Marshal(recordInput{Verdict: string(r.Verdict.Verdict), ReasonCode: string(r.Verdict.ReasonCode), Reviewer: r.Verdict.Reviewer})
	if err != nil {
		return errors.Join(failed, err)
	}
	output, err := json.Marshal(rec)
	if err != nil {
		return errors.Join(failed, err)
	}
	now := e.now()
	tr := trace.Trace{
		ID: trace.NewID(now), Name: trace.NameExtract, Subject: string(path), Ref: r.Run.ID, Time: now, Input: input, Output: output,
	}
	if failed != nil {
		tr.Error = failed.Error()
	}
	return errors.Join(failed, e.traces.Append(ctx, tr))
}
