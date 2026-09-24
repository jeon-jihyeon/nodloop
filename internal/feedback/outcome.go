package feedback

import (
	"fmt"
	"time"
)

type Result string

const (
	ResultConfirmed    Result = "confirmed"    // the cause was checked and held
	ResultRefuted      Result = "refuted"      // the cause was checked and did not hold
	ResultInconclusive Result = "inconclusive" // checked without a clear answer
)

var validResults = map[Result]struct{}{ResultConfirmed: {}, ResultRefuted: {}, ResultInconclusive: {}}

func (r Result) Valid() bool {
	_, ok := validResults[r]
	return ok
}

// What turned out to be true after someone checked
// A different fact from the verdict on the review
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

func NewOutcome(traceID string, result Result, confirmedCause, note, reviewer string, now time.Time) (Outcome, error) {
	if traceID == "" {
		return Outcome{}, ErrTraceIDRequired
	}
	if !result.Valid() {
		return Outcome{}, fmt.Errorf("%w: %q", ErrResultUnknown, result)
	}
	if result != ResultConfirmed && confirmedCause != "" {
		return Outcome{}, fmt.Errorf("%w: %s", ErrCauseUnexpected, result)
	}
	if reviewer == "" {
		reviewer = DefaultReviewer
	}
	return Outcome{
		TraceID: traceID, Time: now.UTC(), Result: result, ConfirmedCause: confirmedCause, Note: note,
		Reviewer: reviewer,
	}, nil
}

// An empty trace id means all
type OutcomeFilter struct {
	TraceID string
}

func (f OutcomeFilter) Matches(o Outcome) bool {
	return f.TraceID == "" || o.TraceID == f.TraceID
}
