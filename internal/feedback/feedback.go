// Package feedback is the human verdict on one trace and what a real check found afterwards
package feedback

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

const (
	// The author of a proposal whose caller names none
	// Records of earlier releases also carry it as the reviewer a call left out and read as a person
	ReviewerAuthor = "author"
	// Reviewer of a record the conversation of a session inferred and not given by a person
	ReviewerSession = "session"
	// Reviewer of a record the capture read from the person's words in the transcript
	// Only NewCaptured writes it since a forged one could turn a dispute of the inferred readers
	ReviewerNodloop = "nodloop"
)

// Whether the conversation or the capture inferred a record a reviewer gave
func inferred(reviewer string) bool {
	return reviewer == ReviewerSession || reviewer == ReviewerNodloop
}

type Verdict string

const (
	VerdictApprove  Verdict = "approve"
	VerdictEdit     Verdict = "edit"
	VerdictReject   Verdict = "reject"
	VerdictWithdraw Verdict = "withdraw" // takes back the verdict before it so the trace has none
)

func Verdicts() []Verdict {
	return []Verdict{VerdictApprove, VerdictEdit, VerdictReject, VerdictWithdraw}
}

func (v Verdict) Valid() bool {
	return slices.Contains(Verdicts(), v)
}

// What the corrected output got wrong
type ReasonCode string

const (
	ReasonFact     ReasonCode = "fact"     // a fact or a claim was wrong
	ReasonApproach ReasonCode = "approach" // the way it went about the task was wrong
	ReasonScope    ReasonCode = "scope"    // it did more or less than asked
	ReasonForm     ReasonCode = "form"     // the content was right and the form was not
	ReasonOther    ReasonCode = "other"    // none of the above
)

// In the order a person is asked
// Four codes and other so one question with four options and its free answer covers them
// Records written before 0.6.0 may hold the codes of the data review and still read
var reasonCodes = []ReasonCode{ReasonFact, ReasonApproach, ReasonScope, ReasonForm, ReasonOther}

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
	case verdict != VerdictEdit && verdict != VerdictReject:
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
	// The corrected output in full so a report can diff it against the run output
	Edited json.RawMessage `json:"edited,omitempty"`
	// A person's name or session or nodloop for a verdict the conversation or the capture inferred
	// Records of earlier releases may carry author for a call that named no one
	Reviewer string `json:"reviewer"`
	// What proves a person gave a named verdict
	// Left out on an inferred verdict and on a named one of an earlier release
	// A named verdict without it reads as none
	Proof trace.Proof `json:"proof,omitempty"`
	// Set when the run was a random audit sample of the queue
	Audit bool `json:"audit,omitempty"`
}

// The verdict of every writer but the capture
// The reserved name of the capture fails with ErrReviewerReserved before any other check
// so no argument, flag or key of a person or a program writes it
func New(
	traceID string, verdict Verdict, code ReasonCode, reason string, edited json.RawMessage, reviewer string, now time.Time,
) (Feedback, error) {
	if reviewer == ReviewerNodloop {
		return Feedback{}, fmt.Errorf("%w: %s", ErrReviewerReserved, reviewer)
	}
	return newVerdict(traceID, verdict, code, reason, edited, reviewer, now)
}

// The verdict the capture read from the person's words in the transcript under the reserved name
// No public surface calls it since the capture runs in a process of its own over the transcript
func NewCaptured(traceID string, verdict Verdict, code ReasonCode, reason string, now time.Time) (Feedback, error) {
	return newVerdict(traceID, verdict, code, reason, nil, ReviewerNodloop, now)
}

// 1. a reason code is optional and only a correction carries one because an approval or a withdraw corrects nothing
// 2. the reviewer is required so no verdict without a name reads as a person's
// 3. an inferred record has the secrets of its reason and edited output redacted before it is checked
// A person's own record is kept as written
// 4. an edit verdict carries a valid JSON edited output and no other verdict carries one
func newVerdict(
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
	if reviewer == "" {
		return Feedback{}, ErrReviewerRequired
	}
	if inferred(reviewer) {
		reason = Redact(reason)
		if len(edited) > 0 {
			edited = json.RawMessage(Redact(string(edited)))
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
	return Feedback{
		TraceID: traceID, Time: now.UTC(), Verdict: verdict, ReasonCode: code, Reason: reason, Edited: edited, Reviewer: reviewer,
	}, nil
}

// Whether the verdict says the output was wrong so it can teach the next run
func (f Feedback) Corrects() bool {
	return f.Verdict == VerdictEdit || f.Verdict == VerdictReject
}

// Whether the conversation or the capture inferred the verdict instead of a person giving it
func (f Feedback) Implicit() bool {
	return inferred(f.Reviewer)
}

// The verdict with the proof of the call that recorded it
// An inferred verdict carries none since no call of a person made it
func (f Feedback) WithProof(p trace.Proof) Feedback {
	if !f.Implicit() {
		f.Proof = p
	}
	return f
}

// Whether a proof shows that the person the verdict names gave it
// So every report counts a named verdict of a script or a program apart from a proven person's
func (f Feedback) Proven() bool {
	return !f.Implicit() && f.Proof.Valid() && f.Proof.Authority() == trace.AuthorityPerson
}

func (f Feedback) withdraws() bool {
	return f.Verdict == VerdictWithdraw
}

// How many top level fields of the recorded output the edit changed added or removed
// 1. any verdict but edit changed nothing
// 2. an output or an edit that is not a JSON object counts as no change because no field can be compared
// Every report reads edit size by this one rule
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

func (f Feedback) trace() string {
	return f.TraceID
}

func (f Feedback) at() time.Time {
	return f.Time
}

// Records in the order the store lists them
// Human and Latest follow the rule of every listing and only a verdict has two inferred readers to reconcile
type Records []Feedback

// The records a person gave in the order the input has them
func (rs Records) Human() Records {
	return Records(listing[Feedback](rs).Human())
}

// Newest record per trace id in the order the input has them
func (rs Records) Latest() Records {
	return Records(listing[Feedback](rs).Latest())
}

// The verdict per trace the conversation and the capture leave together in the order the input has them
// 1. the conversation records with reviewer session and the capture with reviewer nodloop
// 2. a withdraw of anyone takes back the inferred verdicts before it so a withdrawn run has none until a later verdict
// 3. readers that agree on whether the run corrects leave the record of the conversation
// 4. readers that disagree leave the run out as disputed so it teaches nothing
// 5. a reader that judged alone leaves its own record
// A person's record is no reader and the caller lets it win over this one
func (rs Records) Inferred() Records {
	conversation, capture := rs.reader(ReviewerSession), rs.reader(ReviewerNodloop)
	seen := map[string]bool{}
	var out Records
	for _, r := range rs {
		if seen[r.TraceID] || !r.Implicit() {
			continue
		}
		seen[r.TraceID] = true
		said, spoke := conversation[r.TraceID]
		heard, read := capture[r.TraceID]
		switch {
		case spoke && read && said.Corrects() != heard.Corrects():
		case spoke:
			out = append(out, said)
		case read:
			out = append(out, heard)
		}
	}
	return out
}

// The newest record per trace of one inferred reader
// Every withdraw counts as the reader's own so a withdraw of anyone takes back what the reader inferred before it
func (rs Records) reader(reviewer string) map[string]Feedback {
	var own Records
	for _, r := range rs {
		if r.Reviewer == reviewer || r.withdraws() {
			own = append(own, r)
		}
	}
	out := map[string]Feedback{}
	for _, r := range own.Latest() {
		out[r.TraceID] = r
	}
	return out
}

// A record that a later record on the same trace replaces
type stamped interface {
	trace() string
	at() time.Time
	Implicit() bool
	withdraws() bool
}

// Records or outcomes in the order the store lists them
type listing[T stamped] []T

// The records a person gave in the order the input has them
// An inferred record is never a person's word so it neither teaches a run nor replaces a person's record
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
// 1. a trace later approved stops counting as a correction
// 2. a later check replaces an earlier outcome
// 3. on a tie in time the record listed first wins
// 4. that is the later append when the input comes newest first from a store
// 5. a trace whose newest record withdraws is left out as one without a verdict
func (l listing[T]) Latest() listing[T] {
	index := map[string]int{}
	var newest listing[T]
	for _, r := range l {
		i, ok := index[r.trace()]
		switch {
		case !ok:
			index[r.trace()] = len(newest)
			newest = append(newest, r)
		case r.at().After(newest[i].at()):
			newest[i] = r
		}
	}
	out := newest[:0]
	for _, r := range newest {
		if !r.withdraws() {
			out = append(out, r)
		}
	}
	return out
}
