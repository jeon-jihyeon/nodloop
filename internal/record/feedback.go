package record

import (
	"encoding/json"
	"slices"
	"time"
)

// One line of the feedback store
type Feedback struct {
	TraceID    string          `json:"trace_id"`
	Time       time.Time       `json:"time"`
	Verdict    string          `json:"verdict"`
	ReasonCode string          `json:"reason_code,omitempty"`
	Reason     string          `json:"reason,omitempty"`
	Edited     json.RawMessage `json:"edited,omitempty"`
	Reviewer   string          `json:"reviewer"`
	Proof      string          `json:"proof,omitempty"`
	Audit      bool            `json:"audit,omitempty"`
	// The version of the published schema verdict
	// A line of an earlier release lacks it and reads as version 1
	SchemaVersion int `json:"schema_version,omitempty"`
}

// Empty fields mean all
type FeedbackFilter struct {
	TraceID  string
	Verdicts []string
	Reviewer string
	// Verdicts before it are left out
	Since time.Time
	// Zero means all
	Limit int
}

// Whether the verdict is from before Since
func (f FeedbackFilter) Older(fb Feedback) bool {
	return fb.Time.Before(f.Since)
}

func (f FeedbackFilter) Matches(fb Feedback) bool {
	if f.Older(fb) {
		return false
	}
	if f.TraceID != "" && fb.TraceID != f.TraceID {
		return false
	}
	if len(f.Verdicts) > 0 && !slices.Contains(f.Verdicts, fb.Verdict) {
		return false
	}
	if f.Reviewer != "" && fb.Reviewer != f.Reviewer {
		return false
	}
	return true
}
