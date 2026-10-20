package otel

import (
	"encoding/json"
	"time"
)

// What one export sends
// Each record goes as its own span once
type Batch struct {
	Runs        []Run
	Reviews     []Review
	Evaluations []Evaluation
	Calls       []Call
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

// One review question a stop asked about a waiting candidate
type Review struct {
	Time      time.Time
	SessionID string
	// The run of the turn that asked and empty when the records hold none
	Run       string
	Candidate Ref
	// The candidate text sent only with content
	Content string
}

// What an evaluation judged
type EvaluationName string

const (
	EvaluationVerdict EvaluationName = "nodloop.verdict" // a verdict on a run
	EvaluationOutcome EvaluationName = "nodloop.outcome" // what a real check found for a run
	EvaluationReview  EvaluationName = "nodloop.review"  // a person approved or retired a candidate
)

// One verdict or outcome or decision as an evaluation carrier span and a log record
type Evaluation struct {
	Name      EvaluationName
	Time      time.Time
	SessionID string
	// The verdict, the result or the decision status
	Label string
	// A reason, a note or a confirmed cause sent only with content
	Explanation string
	// The edited output of a verdict sent only with content
	Edited     string
	ReasonCode string
	// The reviewer of a verdict or an outcome or the approver of a decision
	Reviewer string
	// A person judged and not the conversation that inferred it
	Human bool
	// The span of the evaluation itself from its key
	Span SpanRef
	// The span it evaluates
	// The carrier links it and the log record names it as its parent
	// Zero when the records name none
	Target SpanRef
	// The candidate a decision is about
	Candidate *Ref
	// The candidate text sent only with content
	Content string
}

// One trace of a model step such as an extraction or a coverage check
type Call struct {
	// The trace id in the records
	ID string
	// The trace name such as extract or check
	Name     string
	Time     time.Time
	Duration time.Duration
	// The session of the run it judged
	SessionID string
	Subject   string
	// The model the backend answered with
	Model string
	Usage Usage
	Error string
	// The conclusion and the candidate of an extraction
	Conclusion string
	Candidate  *Ref
	// The decision point of a classify trace
	Point string
	// The compaction of a check or a compact trace
	Compaction string
	// The run it judged and the correction that caused it
	Links []SpanRef
}

// The usage of a model step
// Input leaves out the cache read and write as the records keep it
type Usage struct {
	Input      int
	Output     int
	Thinking   int
	CacheRead  int
	CacheWrite int
	CostUSD    float64
}

func (u Usage) empty() bool {
	return u == Usage{}
}
