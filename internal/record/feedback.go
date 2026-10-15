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
	Audit      bool            `json:"audit,omitempty"`
}

// Empty fields mean all
type FeedbackFilter struct {
	TraceID  string
	Verdicts []string
	Reviewer string
	// Verdicts before it are left out
	// Verdicts are appended in time order
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
