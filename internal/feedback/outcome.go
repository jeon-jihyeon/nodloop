package feedback

import (
	"fmt"
	"slices"
	"time"
)

type Result string

const (
	ResultConfirmed    Result = "confirmed"    // the cause was checked and held
	ResultRefuted      Result = "refuted"      // the cause was checked and did not hold
	ResultInconclusive Result = "inconclusive" // checked without a clear answer
)

func Results() []Result {
	return []Result{ResultConfirmed, ResultRefuted, ResultInconclusive}
}

func (r Result) Valid() bool {
	return slices.Contains(Results(), r)
}

// What turned out to be true after someone checked
// A different fact from the verdict on the output
type Outcome struct {
	TraceID string    `json:"trace_id"`
	Time    time.Time `json:"time"`
	Result  Result    `json:"result"`
	// The cause that was confirmed in the reviewer's words
	// Empty when refuted or inconclusive
	ConfirmedCause string `json:"confirmed_cause,omitempty"`
	Note           string `json:"note,omitempty"`
	Reviewer       string `json:"reviewer"`
}

// 1. the reviewer is required like on a verdict
// 2. the reserved name of the capture fails with ErrReviewerReserved since the capture records no outcome
// 3. an inferred outcome has the secrets of its cause and note redacted and a person's own outcome is kept as written
func NewOutcome(traceID string, result Result, confirmedCause, note, reviewer string, now time.Time) (Outcome, error) {
	if traceID == "" {
		return Outcome{}, ErrTraceIDRequired
	}
	if reviewer == "" {
		return Outcome{}, ErrReviewerRequired
	}
	if reviewer == ReviewerNodloop {
		return Outcome{}, fmt.Errorf("%w: %s", ErrReviewerReserved, reviewer)
	}
	if !result.Valid() {
		return Outcome{}, fmt.Errorf("%w: %q", ErrResultUnknown, result)
	}
	if result != ResultConfirmed && confirmedCause != "" {
		return Outcome{}, fmt.Errorf("%w: %s", ErrCauseUnexpected, result)
	}
	if inferred(reviewer) {
		confirmedCause, note = Redact(confirmedCause), Redact(note)
	}
	return Outcome{
		TraceID: traceID, Time: now.UTC(), Result: result, ConfirmedCause: confirmedCause, Note: note,
		Reviewer: reviewer,
	}, nil
}

// Whether the check refuted the run at or after at such as the time of a verdict on it
// A person who judged the run again after the refutation stands behind it once more
func (o Outcome) RefutedSince(at time.Time) bool {
	return o.Result == ResultRefuted && !o.Time.Before(at)
}

// Whether the conversation or the capture recorded the outcome instead of a person
func (o Outcome) Implicit() bool {
	return inferred(o.Reviewer)
}

// An outcome is a check that happened and is never taken back
func (o Outcome) withdraws() bool {
	return false
}

func (o Outcome) trace() string {
	return o.TraceID
}

func (o Outcome) at() time.Time {
	return o.Time
}

// Outcomes in the order the store lists them
type Outcomes = listing[Outcome]
