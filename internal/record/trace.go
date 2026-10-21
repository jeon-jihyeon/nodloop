package record

import (
	"encoding/json"
	"slices"
	"time"
)

// One line of the trace store
type Trace struct {
	ID         string              `json:"id"`
	Name       string              `json:"name"`
	SessionID  string              `json:"session_id,omitempty"`
	Subject    string              `json:"subject,omitempty"`
	Ref        string              `json:"ref,omitempty"`
	Time       time.Time           `json:"time"`
	Model      string              `json:"model,omitempty"`
	Input      json.RawMessage     `json:"input"`
	Output     json.RawMessage     `json:"output,omitempty"`
	Error      string              `json:"error,omitempty"`
	Usage      Usage               `json:"usage"`
	DurationMS int64               `json:"duration_ms"`
	Tags       []string            `json:"tags,omitempty"`
	Producer   string              `json:"producer,omitempty"`
	Labels     map[string][]string `json:"labels,omitempty"`
	Draw       *Draw               `json:"draw,omitempty"`
	Source     string              `json:"source,omitempty"`
	// The version of the published schema of the name
	// A line of an earlier release lacks it and reads as version 1
	SchemaVersion int `json:"schema_version,omitempty"`
}

// The draw envelope of a line
type Draw struct {
	CycleID       string   `json:"cycle_id"`
	PolicyVersion string   `json:"policy_version"`
	Unit          string   `json:"unit,omitempty"`
	Seed          string   `json:"seed,omitempty"`
	DrawID        string   `json:"draw_id,omitempty"`
	P             *float64 `json:"p,omitempty"`
	Drawn         bool     `json:"drawn"`
}

type Usage struct {
	InputTokens       int     `json:"input_tokens"`
	OutputTokens      int     `json:"output_tokens"`
	ThinkingTokens    int     `json:"thinking_tokens,omitempty"`
	CacheReadTokens   int     `json:"cache_read"`
	CacheCreateTokens int     `json:"cache_create"`
	CostUSD           float64 `json:"cost_usd"`
}

// Empty fields mean all
type TraceFilter struct {
	ID   string
	Name string
	// Any of them so a reader of several names reads the store once
	Names     []string
	SessionID string
	Subject   string
	Ref       string
	// Every tag must be present
	Tags []string
	// Traces before it are left out
	// Traces are appended in time order
	Since time.Time
	// Zero means all
	Limit int
	// Leaves Output out of every trace a List returns so a reader of counts never copies a run answer
	WithoutOutput bool
}

func (f TraceFilter) Matches(t Trace) bool {
	if f.Older(t) {
		return false
	}
	if f.ID != "" && t.ID != f.ID {
		return false
	}
	if !f.named(t.Name) {
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

// Whether the name is the one Name sets and one of Names when either is set
func (f TraceFilter) named(name string) bool {
	return (f.Name == "" || name == f.Name) && (len(f.Names) == 0 || slices.Contains(f.Names, name))
}

// Whether the trace is from before Since
func (f TraceFilter) Older(t Trace) bool {
	return t.Time.Before(f.Since)
}
