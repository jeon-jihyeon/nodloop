// Package trace is the record of one AI run and its store
package trace

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"time"
)

// One AI run
// Field names follow Langfuse and LangSmith traces so readers need no glossary
type Trace struct {
	// Time sortable id built by NewID
	ID string `json:"id"`
	// run, check, classify or extract
	Name Name `json:"name"`
	// Groups runs
	// A Claude Code session id or the id of a compaction its check belongs to
	SessionID string `json:"session_id,omitempty"`
	// What the run was about in a few words
	Subject string `json:"subject,omitempty"`
	// A trace this one belongs to
	// A classify or extract trace names the run it judged
	Ref   string    `json:"ref,omitempty"`
	Time  time.Time `json:"time"`
	Model string    `json:"model,omitempty"`
	// Structured request
	// Not the rendered prompt
	Input json.RawMessage `json:"input"`
	// Structured result
	// A run that failed leaves it empty and a classify or extract trace keeps what was tried beside the error
	Output     json.RawMessage `json:"output,omitempty"`
	Error      string          `json:"error,omitempty"`
	Usage      Usage           `json:"usage"`
	DurationMS int64           `json:"duration_ms"`
	// Written before 0.6.0 and read as recorded
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
	NameRun         Name = "run"         // output any producer recorded through the core tools
	NameCheck       Name = "check"       // coverage check of a compaction
	NameClassify    Name = "classify"    // answers of the classifiers a decision point asked
	NameExtract     Name = "extract"     // one extraction of a lesson from a corrected run with its drafts and conclusion
	NameReplay      Name = "replay"      // a lesson judged against the recorded outputs it should and should not catch
	NameConsolidate Name = "consolidate" // groups of waiting candidates of one producer that may say the same lesson
	NameRefill      Name = "refill"      // the items a session start added again after the context of the session was emptied
	NameTurn        Name = "turn"        // the start of a turn of a session that the stop reads for the duration of its run
	NameCompact     Name = "compact"     // one drafting of a compaction through a model
)

// Every name in a fixed order for messages
func Names() []Name {
	return []Name{NameRun, NameCheck, NameClassify, NameExtract, NameReplay, NameConsolidate, NameRefill, NameTurn, NameCompact}
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
	labels, err := labels.runLabels()
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

// The trace with how long it took up to its time
// A negative duration fails with ErrDurationNegative since the span would end before it starts
func (t Trace) Took(d time.Duration) (Trace, error) {
	if d < 0 {
		return Trace{}, fmt.Errorf("%w: %s", ErrDurationNegative, d)
	}
	t.DurationMS = d.Milliseconds()
	return t, nil
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

// Time sortable id
// 1. unix milliseconds as 12 hex digits so lexical order is time order
// 2. 8 random hex chars so two runs in the same millisecond differ
func NewID(now time.Time) string {
	var suffix [4]byte
	// crypto rand Read never returns an error
	_, _ = rand.Read(suffix[:])
	return fmt.Sprintf("%012x%x", now.UnixMilli(), suffix)
}

// The millisecond NewID wrote at the start of the id
// False for an id of another shape such as one a caller chose
func IDTime(id string) (time.Time, bool) {
	if len(id) != 20 {
		return time.Time{}, false
	}
	// ParseUint refuses a sign that ParseInt would take
	// 12 hex digits hold 48 bits so the bound keeps the int64 conversion exact
	ms, err := strconv.ParseUint(id[:12], 16, 48)
	if err != nil {
		return time.Time{}, false
	}
	if _, err := strconv.ParseUint(id[12:], 16, 32); err != nil {
		return time.Time{}, false
	}
	return time.UnixMilli(int64(ms)).UTC(), true
}

// Traces in the order the store lists them
type Traces []Trace

// The first trace as a store lists them newest first and the zero trace when there is none
func (ts Traces) Newest() Trace {
	if len(ts) == 0 {
		return Trace{}
	}
	return ts[0]
}
