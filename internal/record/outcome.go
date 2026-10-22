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
	Actor          string    `json:"actor,omitempty"`
	Proof          string    `json:"proof,omitempty"`
	// The version of the published schema outcome
	// A line of an earlier release lacks it and reads as version 1
	SchemaVersion int `json:"schema_version,omitempty"`
}

// An empty trace id means all
type OutcomeFilter struct {
	TraceID string
}

func (f OutcomeFilter) Matches(o Outcome) bool {
	return f.TraceID == "" || o.TraceID == f.TraceID
}
