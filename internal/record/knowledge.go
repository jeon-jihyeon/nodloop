package record

import "time"

// One line of the knowledge store
// Field order follows the line as written so a record encodes to the same bytes it was read from
type Knowledge struct {
	ID             string    `json:"id"`
	Version        int       `json:"version"`
	Kind           string    `json:"kind"`
	Content        string    `json:"content"`
	Run            *RunScope `json:"run,omitempty"`
	ConditionalRun *RunScope `json:"conditional_run,omitempty"`
	Evidence       Evidence  `json:"evidence"`
	Basis          string    `json:"basis"`
	Status         string    `json:"status"`
	Approver       string    `json:"approver,omitempty"`
	ApprovedAt     time.Time `json:"approved_at,omitzero"`
	ReviewedAt     time.Time `json:"reviewed_at,omitzero"`
	Reason         string    `json:"reason,omitempty"`
	Decision       string    `json:"decision,omitempty"`
	Extract        string    `json:"extract,omitempty"`
	Supersedes     int       `json:"supersedes,omitempty"`
	Base           int       `json:"base,omitempty"`
	Time           time.Time `json:"time"`
	Author         string    `json:"author"`
	Drafted        bool      `json:"drafted,omitempty"`
	NewLabels      bool      `json:"new_labels,omitempty"`
	Veto           *Veto     `json:"veto,omitempty"`
	Cue            *Cue      `json:"cue,omitempty"`
	Compaction     string    `json:"compaction,omitempty"`
	CompactionSize int       `json:"compaction_size,omitempty"`
	// The version of the published schema knowledge
	// A line of an earlier release lacks it and reads as version 1
	SchemaVersion int `json:"schema_version,omitempty"`
}

type RunScope struct {
	Producer   string              `json:"producer"`
	Labels     map[string][]string `json:"labels,omitempty"`
	Except     map[string][]string `json:"except,omitempty"`
	ValidFrom  time.Time           `json:"valid_from,omitzero"`
	ValidUntil time.Time           `json:"valid_until,omitzero"`
}

type Evidence struct {
	FeedbackTraceIDs []string          `json:"feedback_trace_ids,omitempty"`
	FeedbackUnits    map[string]string `json:"feedback_units,omitempty"`
	OutcomeTraceIDs  []string          `json:"outcome_trace_ids,omitempty"`
	ParagraphIDs     []string          `json:"paragraph_ids,omitempty"`
	Knowledge        []Ref             `json:"knowledge,omitempty"`
}

type Ref struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
}

type Veto struct {
	Tool    string          `json:"tool"`
	When    []VetoCondition `json:"when"`
	Example map[string]any  `json:"example"`
}

// The tool call a judgment arrives beside instead of the prompt
type Cue struct {
	Tool    string `json:"tool"`
	Command string `json:"command,omitempty"`
	File    string `json:"file,omitempty"`
}

type VetoCondition struct {
	Field  string `json:"field"`
	Match  string `json:"match"`
	Unless string `json:"unless,omitempty"`
}
