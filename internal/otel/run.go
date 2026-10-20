package otel

import (
	"encoding/json"
	"time"
)

// What one export sends
// Each record goes as its own span once
type Batch struct {
	Runs        []Run
	Evaluations []Evaluation
}

// One run as the span of its turn
type Run struct {
	ID   string
	Time time.Time
	// How long the turn took up to Time and zero when it is unknown
	Duration  time.Duration
	Producer  string
	SessionID string
	Subject   string
	// The one model the producer runs and empty when it may switch models
	Model  string
	Labels map[string][]string
	// The knowledge versions the run applied
	Applied []Ref
	// The knowledge versions a holdout kept from the run
	Withheld []Ref
	// The run went through a holdout draw
	Drawn bool
	// The draw unit and the share of a drawn run
	Unit  string
	Share float64
	// The plugin version a conversation hook ran under
	Plugin string
	Output json.RawMessage
	// Why the run failed and empty when it did not
	Error string
}

// One knowledge version
type Ref struct {
	ID      string
	Version int
}

// What an evaluation judged
type EvaluationName string

const (
	EvaluationVerdict EvaluationName = "nodloop.verdict" // a verdict on a run
	EvaluationOutcome EvaluationName = "nodloop.outcome" // what a real check found for a run
)

// One verdict or outcome as an evaluation carrier span and a log record
type Evaluation struct {
	Name      EvaluationName
	Time      time.Time
	SessionID string
	// The verdict or the result
	Label string
	// A reason, a note or a confirmed cause sent only with content
	Explanation string
	// The edited output of a verdict sent only with content
	Edited     string
	ReasonCode string
	// The reviewer of a verdict or an outcome
	Reviewer string
	// A person judged and not the conversation that inferred it
	Human bool
	// The span of the evaluation itself from its key
	Span SpanRef
	// The span it evaluates
	// The carrier links it and the log record names it as its parent
	// Zero when the records name none
	Target SpanRef
}
