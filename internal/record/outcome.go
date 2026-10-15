package record

import "time"

// One line of the outcome store
type Outcome struct {
	TraceID        string    `json:"trace_id"`
	Time           time.Time `json:"time"`
	Result         string    `json:"result"`
	ConfirmedCause string    `json:"confirmed_cause,omitempty"`
	Note           string    `json:"note,omitempty"`
	Reviewer       string    `json:"reviewer"`
}

// An empty trace id means all
type OutcomeFilter struct {
	TraceID string
}

func (f OutcomeFilter) Matches(o Outcome) bool {
	return f.TraceID == "" || o.TraceID == f.TraceID
}
