package otel

import (
	"encoding/json"
	"time"
)

// What one export sends
// Each record goes as its own span once
type Batch struct {
	Runs        []Run
	Questions   []Question
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

// One question or notice an expose trace records as it was shown
type Question struct {
	// The expose trace
	ID        string
	Time      time.Time
	SessionID string
	// The run of the answer the question followed and empty when the expose names none
	Run string
	// direction, shadow, audit or notice
	Kind string
	// push or manual
	Source string
	// 1 when first asked and 2 when a later session asked it again
	Round int
	// The version it asks about and nil on a notice
	Candidate *Ref
	// The cycle of its draw and the probability it was drawn with
	// P is nil for a question the person asked for
	Cycle string
	P     *float64
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
	// What the members of a judge classify answered and nil on any other trace
	Judge *Judge
	// The run it judged and the correction that caused it
	Links []SpanRef
}

// What the members of the judge found on one change as its classify trace records them
type Judge struct {
	// The model and the effort of each member in the order of the members
	Models  []string
	Efforts []string
	// conform, against, undecided or split
	Outcome string
	// The share of the members that conform
	Votes float64
	// The bin of the mean confidence of the conforming members and empty when none conforms
	Bin string
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
