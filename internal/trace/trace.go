// Package trace is the record of one AI run and its store
package trace

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"slices"
	"time"
)

// One AI run
// Field names follow Langfuse and LangSmith traces so readers need no glossary
type Trace struct {
	// Time sortable id built by NewID
	ID string `json:"id"`
	// Pipeline that produced the run
	Name Name `json:"name"`
	// Groups runs
	// An eval run id or a Claude Code session id
	SessionID string `json:"session_id,omitempty"`
	// What the run was about
	// An event id for diagnose
	Subject string `json:"subject,omitempty"`
	// The context trace a select or diagnose trace belongs to
	Ref   string    `json:"ref,omitempty"`
	Time  time.Time `json:"time"`
	Model string    `json:"model,omitempty"`
	// Structured request
	// Not the rendered prompt
	Input json.RawMessage `json:"input"`
	// Structured result
	// Empty when Error is set
	Output     json.RawMessage `json:"output,omitempty"`
	Error      string          `json:"error,omitempty"`
	Usage      Usage           `json:"usage"`
	DurationMS int64           `json:"duration_ms"`
	// Conditions such as feedback:on
	Tags []string `json:"tags,omitempty"`
	// The producer that recorded a run trace
	// Empty on every other name
	Producer string `json:"producer,omitempty"`
	// The situation of a run trace that knowledge scopes match
	// Empty on every other name
	Labels Labels `json:"labels,omitempty"`
}

type Name string

const (
	NameContext  Name = "context"  // review context built for one event
	NameSelect   Name = "select"   // knowledge and example candidates chosen for a context
	NameDiagnose Name = "diagnose" // review recorded from a context
	NameRevise   Name = "revise"   // first submission of a context sent back with its defects
	NameRun      Name = "run"      // output any producer recorded through the core tools
)

// Every name in a fixed order for messages
func Names() []Name {
	return []Name{NameContext, NameSelect, NameDiagnose, NameRevise, NameRun}
}

func (n Name) Valid() bool {
	return slices.Contains(Names(), n)
}

// A diagnose trace is a review of the data diagnosis
func (t Trace) IsReview() bool {
	return t.Name == NameDiagnose
}

// A correction of a review reads it as a review
// 1. ErrNotReview naming the trace when it is not a diagnose trace
// 2. ErrFailedRun naming the failure when the review failed
// A failed review still closes its context so IsReview keeps it
func (t Trace) CheckReview() error {
	if !t.IsReview() {
		return fmt.Errorf("%w: %s is a %s trace", ErrNotReview, t.ID, t.Name)
	}
	return t.checkSucceeded()
}

// Feedback and outcomes and knowledge cite a recorded review or a recorded run
// 1. ErrNotRun naming the trace when it is neither
// 2. ErrFailedRun naming the failure when it failed
func (t Trace) CheckRun() error {
	if !t.IsReview() && t.Name != NameRun {
		return fmt.Errorf("%w: %s is a %s trace", ErrNotRun, t.ID, t.Name)
	}
	return t.checkSucceeded()
}

// A failed run holds no output so a correction or an outcome has nothing to apply to
func (t Trace) checkSucceeded() error {
	if t.Error != "" {
		return fmt.Errorf("%w: %s failed: %s", ErrFailedRun, t.ID, t.Error)
	}
	return nil
}

// A run trace of what a producer made
// 1. output that is not JSON is kept as a JSON string so any text can be recorded
// 2. the input holds what the run applied as the producer reports it
func NewRun(producer, subject string, labels Labels, input json.RawMessage, output []byte, now time.Time) (Trace, error) {
	if producer == "" {
		return Trace{}, ErrProducerRequired
	}
	labels, err := labels.normalized()
	if err != nil {
		return Trace{}, err
	}
	out := json.RawMessage(output)
	if !json.Valid(output) {
		if out, err = json.Marshal(string(output)); err != nil {
			return Trace{}, err
		}
	}
	if len(input) == 0 {
		input = json.RawMessage("{}")
	}
	return Trace{
		ID: NewID(now), Name: NameRun, Producer: producer, Subject: subject, Labels: labels, Time: now,
		Input: input, Output: out,
	}, nil
}

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	// The share of OutputTokens spent thinking
	// Absent in traces written before it was recorded
	ThinkingTokens    int     `json:"thinking_tokens,omitempty"`
	CacheReadTokens   int     `json:"cache_read"`
	CacheCreateTokens int     `json:"cache_create"`
	CostUSD           float64 `json:"cost_usd"`
}

func (u Usage) Add(other Usage) Usage {
	return Usage{
		InputTokens:       u.InputTokens + other.InputTokens,
		OutputTokens:      u.OutputTokens + other.OutputTokens,
		ThinkingTokens:    u.ThinkingTokens + other.ThinkingTokens,
		CacheReadTokens:   u.CacheReadTokens + other.CacheReadTokens,
		CacheCreateTokens: u.CacheCreateTokens + other.CacheCreateTokens,
		CostUSD:           u.CostUSD + other.CostUSD,
	}
}

// Whole prompt both cached and uncached
// claude -p reports cached prompt tokens apart from the rest
func (u Usage) PromptTokens() int {
	return u.InputTokens + u.CacheReadTokens + u.CacheCreateTokens
}

// Empty fields mean all
type Filter struct {
	ID        string
	Name      Name
	SessionID string
	Subject   string
	Ref       string
	// Every tag must be present
	Tags []string
	// Zero means all
	Limit int
}

func (f Filter) Matches(t Trace) bool {
	if f.ID != "" && t.ID != f.ID {
		return false
	}
	if f.Name != "" && t.Name != f.Name {
		return false
	}
	if f.SessionID != "" && t.SessionID != f.SessionID {
		return false
	}
	if f.Subject != "" && t.Subject != f.Subject {
		return false
	}
	if f.Ref != "" && t.Ref != f.Ref {
		return false
	}
	for _, want := range f.Tags {
		if !slices.Contains(t.Tags, want) {
			return false
		}
	}
	return true
}

// Time sortable id
// 1. unix milliseconds as 12 hex digits so lexical order is time order
// 2. 8 random hex chars so two runs in the same millisecond differ
func NewID(now time.Time) string {
	var suffix [4]byte
	// crypto rand Read never returns an error
	_, _ = rand.Read(suffix[:])
	return fmt.Sprintf("%012x%x", now.UnixMilli(), suffix)
}

// Traces in the order the store lists them
type Traces []Trace

// Context traces that no diagnose trace refers to in the input order
// Computed from the pairs so no extra store is needed
func (ts Traces) Pending() Traces {
	recorded := map[string]struct{}{}
	for _, t := range ts {
		if t.IsReview() && t.Ref != "" {
			recorded[t.Ref] = struct{}{}
		}
	}
	var out Traces
	for _, t := range ts {
		if _, ok := recorded[t.ID]; t.Name == NameContext && !ok {
			out = append(out, t)
		}
	}
	return out
}
