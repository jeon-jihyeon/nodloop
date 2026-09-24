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
}

type Name string

const (
	NameContext  Name = "context"  // review context built for one event
	NameSelect   Name = "select"   // knowledge and example candidates chosen for a context
	NameDiagnose Name = "diagnose" // review recorded from a context
	NameRevise   Name = "revise"   // first submission of a context sent back with its defects
)

// Every name in a fixed order for messages
func Names() []Name {
	return []Name{NameContext, NameSelect, NameDiagnose, NameRevise}
}

func (n Name) Valid() bool {
	return slices.Contains(Names(), n)
}

// Only a diagnose trace is a review that feedback and outcomes and knowledge may cite
func (t Trace) isReview() bool {
	return t.Name == NameDiagnose
}

// ErrNotReview naming the trace when feedback or an outcome or knowledge cites something that is not a review
func (t Trace) CheckReview() error {
	if !t.isReview() {
		return fmt.Errorf("%w: %s is a %s trace", ErrNotReview, t.ID, t.Name)
	}
	return nil
}

type Usage struct {
	InputTokens       int     `json:"input_tokens"`
	OutputTokens      int     `json:"output_tokens"`
	CacheReadTokens   int     `json:"cache_read"`
	CacheCreateTokens int     `json:"cache_create"`
	CostUSD           float64 `json:"cost_usd"`
}

func (u Usage) Add(other Usage) Usage {
	return Usage{
		InputTokens:       u.InputTokens + other.InputTokens,
		OutputTokens:      u.OutputTokens + other.OutputTokens,
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
		if t.isReview() && t.Ref != "" {
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
