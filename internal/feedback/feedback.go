// Package feedback is the human verdict on one trace and what a real check found afterwards
package feedback

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"
)

// Reviewer when the caller names none
const DefaultReviewer = "author"

type Verdict string

const (
	VerdictApprove Verdict = "approve"
	VerdictEdit    Verdict = "edit"
	VerdictReject  Verdict = "reject"
)

var validVerdicts = map[Verdict]struct{}{VerdictApprove: {}, VerdictEdit: {}, VerdictReject: {}}

func (v Verdict) Valid() bool {
	_, ok := validVerdicts[v]
	return ok
}

// References the trace and never copies its output
// A trace may receive several records and the newest is the current verdict
type Feedback struct {
	TraceID string    `json:"trace_id"`
	Time    time.Time `json:"time"`
	Verdict Verdict   `json:"verdict"`
	Reason  string    `json:"reason,omitempty"`
	// Present when Verdict is edit
	// The corrected output in full so eval can diff it against the trace output
	Edited json.RawMessage `json:"edited,omitempty"`
	// author for the project author
	// session for implicit feedback
	Reviewer string `json:"reviewer"`
}

// The reviewer defaults to author
// An edit verdict carries a valid JSON edited review and no other verdict carries one
func New(
	traceID string, verdict Verdict, reason string, edited json.RawMessage, reviewer string, now time.Time,
) (Feedback, error) {
	if traceID == "" {
		return Feedback{}, ErrTraceIDRequired
	}
	if !verdict.Valid() {
		return Feedback{}, fmt.Errorf("%w: %q", ErrVerdictUnknown, verdict)
	}
	if verdict == VerdictEdit && len(edited) == 0 {
		return Feedback{}, ErrEditedRequired
	}
	if verdict != VerdictEdit && len(edited) > 0 {
		return Feedback{}, fmt.Errorf("%w: %s", ErrEditedUnexpected, verdict)
	}
	if len(edited) > 0 && !json.Valid(edited) {
		return Feedback{}, ErrEditedInvalid
	}
	if reviewer == "" {
		reviewer = DefaultReviewer
	}
	return Feedback{
		TraceID: traceID, Time: now.UTC(), Verdict: verdict, Reason: reason, Edited: edited, Reviewer: reviewer,
	}, nil
}

// Whether the verdict says the review was wrong so it can teach the next one
func (f Feedback) Corrects() bool {
	return f.Verdict == VerdictEdit || f.Verdict == VerdictReject
}

// Empty fields mean all
type Filter struct {
	TraceID  string
	Verdicts []Verdict
	Reviewer string
	// Zero means all
	Limit int
}

func (f Filter) Matches(fb Feedback) bool {
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

// Records in the order the store lists them
type Records []Feedback

// Newest record per trace id in the order the input has them
// A trace later approved stops being an edit or reject example
// On a tie in Time the record listed first wins
// That is the later append when the input comes newest first from a store
func (rs Records) Latest() Records {
	latest := map[string]Feedback{}
	var order []string
	for _, r := range rs {
		cur, ok := latest[r.TraceID]
		if !ok {
			order = append(order, r.TraceID)
		}
		if !ok || r.Time.After(cur.Time) {
			latest[r.TraceID] = r
		}
	}
	var out Records
	for _, id := range order {
		out = append(out, latest[id])
	}
	return out
}
