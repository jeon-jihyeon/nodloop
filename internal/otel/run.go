package otel

import (
	"encoding/json"
	"time"
)

// One run as a span carries it with the verdict and the outcome in effect on it
type Run struct {
	ID        string
	Time      time.Time
	Producer  string
	SessionID string
	Subject   string
	Labels    map[string][]string
	// The knowledge versions the run applied
	Applied []Ref
	// The knowledge versions a holdout kept from the run
	Withheld []Ref
	// The run went through a holdout draw
	Drawn  bool
	Output json.RawMessage
	// Nil when no verdict is in effect
	Verdict *Verdict
	// Nil when no check was recorded
	Outcome *Outcome
}

// One knowledge version
type Ref struct {
	ID      string
	Version int
}

type Verdict struct {
	Time       time.Time
	Verdict    string
	ReasonCode string
	Reason     string
	Reviewer   string
}

type Outcome struct {
	Time           time.Time
	Result         string
	ConfirmedCause string
	Note           string
	Reviewer       string
}
