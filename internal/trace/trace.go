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
	// A Claude Code session id or the id of a compaction its check belongs to
	SessionID string `json:"session_id,omitempty"`
	// What the run was about in a few words
	Subject string `json:"subject,omitempty"`
	// A trace this one belongs to
	// Written by the data review before 0.8.0 and read as recorded
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
	NameRun   Name = "run"   // output any producer recorded through the core tools
	NameCheck Name = "check" // coverage check of a compaction
)

// Every name in a fixed order for messages
func Names() []Name {
	return []Name{NameRun, NameCheck}
}

func (n Name) Valid() bool {
	return slices.Contains(Names(), n)
}

// Feedback and outcomes and knowledge cite a recorded run
// 1. ErrNotRun naming the trace when it is not one
// 2. ErrFailedRun naming the failure when it failed
func (t Trace) CheckRun() error {
	if t.Name != NameRun {
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
	labels, err := labels.Normalized()
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
