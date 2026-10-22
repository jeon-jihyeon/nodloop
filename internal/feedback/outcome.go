package feedback

import (
	"fmt"
	"slices"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

type Actor string

const (
	ActorPerson  Actor = "person"  // a person's check
	ActorCode    Actor = "code"    // a deterministic check
	ActorModel   Actor = "model"   // a model's evaluation
	ActorUnknown Actor = "unknown" // the evaluator was not recorded
)

func Actors() []Actor {
	return []Actor{ActorPerson, ActorCode, ActorModel, ActorUnknown}
}

// Empty is valid since a caller may name no evaluator, see Evaluator
func (a Actor) Valid() bool {
	return a == "" || slices.Contains(Actors(), a)
}

// The actor a call records
// A call that names none reads as a person only from a caller the proof shows as a person under a reviewer no reader inferred
func (a Actor) Declared(proof trace.Proof, reviewer string) Actor {
	if a == "" && proof.Authority() == trace.AuthorityPerson && !inferred(reviewer) {
		return ActorPerson
	}
	return a
}

type Result string

const (
	ResultConfirmed    Result = "confirmed"    // the cause was checked and held
	ResultRefuted      Result = "refuted"      // the cause was checked and did not hold
	ResultInconclusive Result = "inconclusive" // checked without a clear answer
)

func Results() []Result {
	return []Result{ResultConfirmed, ResultRefuted, ResultInconclusive}
}

func (r Result) Valid() bool {
	return slices.Contains(Results(), r)
}

// What turned out to be true after someone checked
// A different fact from the verdict on the output
type Outcome struct {
	TraceID string    `json:"trace_id"`
	Time    time.Time `json:"time"`
	Result  Result    `json:"result"`
	// The cause that was confirmed in the reviewer's words
	// Empty when refuted or inconclusive
	ConfirmedCause string `json:"confirmed_cause,omitempty"`
	Note           string `json:"note,omitempty"`
	Reviewer       string `json:"reviewer"`
	// The evaluator the call declared and empty on a record of an earlier release
	Actor Actor `json:"actor,omitempty"`
	// What proved the caller a person apart from the declared actor
	Proof trace.Proof `json:"proof,omitempty"`
}

// 1. the reviewer is required like on a verdict
// 2. the reserved name of the capture fails with ErrReviewerReserved since the capture records no outcome
// 3. only a declared person evaluator keeps the cause and note as written
func NewOutcome(
	traceID string, result Result, confirmedCause, note, reviewer string, actor Actor, now time.Time,
) (Outcome, error) {
	if traceID == "" {
		return Outcome{}, ErrTraceIDRequired
	}
	if reviewer == "" {
		return Outcome{}, ErrReviewerRequired
	}
	if reviewer == ReviewerNodloop {
		return Outcome{}, fmt.Errorf("%w: %s", ErrReviewerReserved, reviewer)
	}
	if !result.Valid() {
		return Outcome{}, fmt.Errorf("%w: %q", ErrResultUnknown, result)
	}
	if result != ResultConfirmed && confirmedCause != "" {
		return Outcome{}, fmt.Errorf("%w: %s", ErrCauseUnexpected, result)
	}
	if !actor.Valid() {
		return Outcome{}, fmt.Errorf("%w: %q", ErrActorUnknown, actor)
	}
	if actor == ActorPerson && inferred(reviewer) {
		return Outcome{}, ErrActorUnexpected
	}
	if actor != ActorPerson {
		confirmedCause, note = Redact(confirmedCause), Redact(note)
	}
	return Outcome{
		TraceID: traceID, Time: now.UTC(), Result: result, ConfirmedCause: confirmedCause, Note: note,
		Reviewer: reviewer, Actor: actor,
	}, nil
}

// Whether the check refuted the run at or after at such as the time of a verdict on it
// A person who judged the run again after the refutation stands behind it once more
func (o Outcome) RefutedSince(at time.Time) bool {
	return o.Result == ResultRefuted && !o.Time.Before(at)
}

// Whether no person evaluated the check so Human leaves it out
func (o Outcome) Implicit() bool {
	return o.Evaluator() != ActorPerson
}

// Who evaluated the check as the record declares it
// 1. an inferred reviewer without an actor or with a person claim reads as model since a conversation wrote it
// 2. any other record without an actor reads as unknown since a name alone proves no person
// 3. an actor outside the set reads as unknown so a future value grants nothing
func (o Outcome) Evaluator() Actor {
	switch {
	case inferred(o.Reviewer) && (o.Actor == "" || o.Actor == ActorPerson):
		return ActorModel
	case o.Actor == "" || !o.Actor.Valid():
		return ActorUnknown
	}
	return o.Actor
}

// A proof is kept only on a person evaluator
func (o Outcome) WithProof(proof trace.Proof) Outcome {
	if o.Evaluator() == ActorPerson {
		o.Proof = proof
	}
	return o
}

// Whether a person evaluator and a proof of a person stand behind the check
func (o Outcome) Proven() bool {
	return o.Evaluator() == ActorPerson && o.Proof.Authority() == trace.AuthorityPerson
}

// An outcome is a check that happened and is never taken back
func (o Outcome) withdraws() bool {
	return false
}

func (o Outcome) trace() string {
	return o.TraceID
}

func (o Outcome) at() time.Time {
	return o.Time
}

// Outcomes in the order the store lists them
type Outcomes = listing[Outcome]
