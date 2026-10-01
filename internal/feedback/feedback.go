// Package feedback is the human verdict on one trace and what a real check found afterwards
package feedback

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"
)

const (
	// Reviewer when the caller names none
	ReviewerAuthor = "author"
	// Reviewer of a record mined from a Claude Code session and not given by a person
	ReviewerSession = "session"
)

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

// What the corrected review got wrong
type ReasonCode string

const (
	ReasonStatus   ReasonCode = "status"   // the status was wrong
	ReasonCause    ReasonCode = "cause"    // a cause was wrong or missing
	ReasonCitation ReasonCode = "citation" // a cited paragraph does not state the cause
	ReasonChecks   ReasonCode = "checks"   // a check was missing or out of order
	ReasonOther    ReasonCode = "other"    // none of the above
)

// In the order a person is asked
// Four codes and other so one question with four options and its free answer covers them
var reasonCodes = []ReasonCode{ReasonStatus, ReasonCause, ReasonCitation, ReasonChecks, ReasonOther}

func ReasonCodes() []ReasonCode {
	return slices.Clone(reasonCodes)
}

func (c ReasonCode) Valid() bool {
	return slices.Contains(reasonCodes, c)
}

// An empty code passes any verdict and a code passes only a correction
func (c ReasonCode) check(verdict Verdict) error {
	switch {
	case c == "":
		return nil
	case !c.Valid():
		return fmt.Errorf("%w: %q", ErrReasonCodeUnknown, c)
	case verdict == VerdictApprove:
		return fmt.Errorf("%w: %s", ErrReasonCodeUnexpected, c)
	}
	return nil
}

// References the trace and never copies its output
// A trace may receive several records and the newest is the current verdict
type Feedback struct {
	TraceID string    `json:"trace_id"`
	Time    time.Time `json:"time"`
	Verdict Verdict   `json:"verdict"`
	// Empty on an approval and on a correction recorded without one
	ReasonCode ReasonCode `json:"reason_code,omitempty"`
	Reason     string     `json:"reason,omitempty"`
	// Present when Verdict is edit
	// The corrected output in full so eval can diff it against the trace output
	Edited json.RawMessage `json:"edited,omitempty"`
	// author for the project author
	// session for implicit feedback
	Reviewer string `json:"reviewer"`
	// Set when the review was a random audit sample of the queue
	Audit bool `json:"audit,omitempty"`
}

// The reviewer defaults to author
// 1. an edit verdict carries a valid JSON edited review and no other verdict carries one
// 2. a reason code is optional and only a correction carries one because an approval corrects nothing
// 3. a session record has the secrets of its reason and edited review redacted before it is checked
// A person's own record is kept as written
func New(
	traceID string, verdict Verdict, code ReasonCode, reason string, edited json.RawMessage, reviewer string, now time.Time,
) (Feedback, error) {
	if traceID == "" {
		return Feedback{}, ErrTraceIDRequired
	}
	if !verdict.Valid() {
		return Feedback{}, fmt.Errorf("%w: %q", ErrVerdictUnknown, verdict)
	}
	if err := code.check(verdict); err != nil {
		return Feedback{}, err
	}
	if reviewer == ReviewerSession {
		reason = redact(reason)
		if len(edited) > 0 {
			edited = json.RawMessage(redact(string(edited)))
		}
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
		reviewer = ReviewerAuthor
	}
	return Feedback{
		TraceID: traceID, Time: now.UTC(), Verdict: verdict, ReasonCode: code, Reason: reason, Edited: edited, Reviewer: reviewer,
	}, nil
}

// Whether the verdict says the review was wrong so it can teach the next one
func (f Feedback) Corrects() bool {
	return f.Verdict == VerdictEdit || f.Verdict == VerdictReject
}

// Whether a session gave the verdict instead of a person
func (f Feedback) Implicit() bool {
	return f.Reviewer == ReviewerSession
}

// How many top level fields of the recorded review the edit changed added or removed
// 1. any verdict but edit changed nothing
// 2. a review or an edit that is not a JSON object counts as no change because no field can be compared
// eval and the online report read edit size by this one rule
func (f Feedback) EditWidth(original json.RawMessage) int {
	var before, after map[string]json.RawMessage
	if f.Verdict != VerdictEdit || json.Unmarshal(original, &before) != nil || json.Unmarshal(f.Edited, &after) != nil {
		return 0
	}
	width := 0
	for k, v := range before {
		if string(v) != string(after[k]) {
			width++
		}
	}
	for k := range after {
		if _, ok := before[k]; !ok {
			width++
		}
	}
	return width
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

func (f Feedback) trace() string {
	return f.TraceID
}

func (f Feedback) at() time.Time {
	return f.Time
}

// Records in the order the store lists them
type Records = listing[Feedback]

// A record that a later record on the same trace replaces
type stamped interface {
	trace() string
	at() time.Time
	Implicit() bool
}

// Records or outcomes in the order the store lists them
type listing[T stamped] []T

// The records a person gave in the order the input has them
// A session record is never a person's word so it neither teaches a review nor replaces a person's record
func (l listing[T]) Human() listing[T] {
	out := make(listing[T], 0, len(l))
	for _, r := range l {
		if !r.Implicit() {
			out = append(out, r)
		}
	}
	return out
}

// Newest record per trace id in the order the input has them
// 1. a trace later approved stops being an edit or reject example
// 2. a later check replaces an earlier outcome
// 3. on a tie in time the record listed first wins
// 4. that is the later append when the input comes newest first from a store
func (l listing[T]) Latest() listing[T] {
	index := map[string]int{}
	var out listing[T]
	for _, r := range l {
		i, ok := index[r.trace()]
		switch {
		case !ok:
			index[r.trace()] = len(out)
			out = append(out, r)
		case r.at().After(out[i].at()):
			out[i] = r
		}
	}
	return out
}
