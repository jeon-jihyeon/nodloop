package trace

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"
)

// Who a write counts as
type Authority string

const (
	AuthorityPerson Authority = "person" // a proof shows the person made the write so it decides at once
	AuthorityAgent  Authority = "agent"  // no proof so the write is a proposal the person is asked about
)

func Authorities() []Authority {
	return []Authority{AuthorityPerson, AuthorityAgent}
}

// What shows that the caller of a write is a person
type Proof string

const (
	ProofKey         Proof = "key"         // a nodloop-server key of role approver added with --person
	ProofTerminal    Proof = "terminal"    // a CLI call from an interactive terminal outside an agent session
	ProofElicitation Proof = "elicitation" // an MCP form the person accepted for the one action it named
	ProofReply       Proof = "reply"       // the person's own click or reply word the hooks read
	ProofNone        Proof = "none"        // any other call such as a model's tool call or a program in process
)

func Proofs() []Proof {
	return []Proof{ProofKey, ProofTerminal, ProofElicitation, ProofReply, ProofNone}
}

func (p Proof) Valid() bool {
	return slices.Contains(Proofs(), p)
}

// A person for every proof but none
func (p Proof) Authority() Authority {
	if p == ProofNone {
		return AuthorityAgent
	}
	return AuthorityPerson
}

// One action of the write side on what nodloop prepared or carried out
type Action string

const (
	ActionJudge    Action = "judge"    // keep, undo or scope one item version or a compaction
	ActionAnswer   Action = "answer"   // one choice of an open question
	ActionDismiss  Action = "dismiss"  // close an open question with no decision
	ActionPause    Action = "pause"    // send every matching change of a kind or a scope to the person
	ActionResume   Action = "resume"   // end a pause
	ActionKnob     Action = "knob"     // set one knob within its range
	ActionReanchor Action = "reanchor" // take the versions nodloop approved in a place as the person's own
	ActionDigest   Action = "digest"   // the cursor since the person's last judgment and nothing changed
	ActionReverse  Action = "reverse"  // the inverse of an earlier intervention
)

func Actions() []Action {
	return []Action{
		ActionJudge, ActionAnswer, ActionDismiss, ActionPause, ActionResume, ActionKnob, ActionReanchor, ActionDigest, ActionReverse,
	}
}

func (a Action) Valid() bool {
	return slices.Contains(Actions(), a)
}

// What a judgment does to its target or which choice of a question an answer takes
type Choice string

const (
	ChoiceKeep     Choice = "keep"     // approves a candidate or a compaction, reaffirms an approved version or restores a retired one
	ChoiceUndo     Choice = "undo"     // retires an approved version, drops a candidate or reverses what curate carried out
	ChoiceScope    Choice = "scope"    // approves a candidate in the labels picked
	ChoiceDrop     Choice = "drop"     // retires the candidate a question names
	ChoiceMerge    Choice = "merge"    // applies the compaction a question prepared and then the change
	ChoiceSeparate Choice = "separate" // leaves the items and the change as they are
	ChoiceMine     Choice = "mine"     // takes the versions nodloop approved in the place into the anchor
	ChoiceReview   Choice = "review"   // opens the digest instead
	ChoiceYes      Choice = "yes"      // opens the kind, ends the shadow or carries out a proposal
	ChoiceNotYet   Choice = "notyet"   // keeps it as it is
	ChoiceSame     Choice = "same"     // keeps what curate carried out
)

func Choices() []Choice {
	return []Choice{
		ChoiceKeep, ChoiceUndo, ChoiceScope, ChoiceDrop, ChoiceMerge, ChoiceSeparate, ChoiceMine, ChoiceReview, ChoiceYes, ChoiceNotYet,
		ChoiceSame,
	}
}

func (c Choice) Valid() bool {
	return slices.Contains(Choices(), c)
}

// The choices of a judgment
// Every other choice belongs to a question
func (c Choice) Judges() bool {
	return c == ChoiceKeep || c == ChoiceUndo || c == ChoiceScope
}

// One item version an intervention names
type ItemVersion struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
}

// The input of an intervention trace
type Intervention struct {
	Action Action `json:"action"`
	// What the caller named: an item version as id:version, a compaction, a decide, an intervention, a knob, a change kind or a producer
	Target string `json:"target,omitempty"`
	// The item version a judgment acts on or the one the question of an answer names
	ID      string `json:"id,omitempty"`
	Version int    `json:"version,omitempty"`
	// The compaction a judgment acts on in place of an item version
	Compaction string `json:"compaction,omitempty"`
	// The decide of the question an answer or a dismiss closes
	Decide string `json:"decide,omitempty"`
	Choice Choice `json:"choice,omitempty"`
	// Where a scope choice approves the candidate, the scope a pause covers or the place of a reanchor or a digest
	// None reaches every run of its producer
	Labels Labels `json:"labels,omitempty"`
	// The knob a knob intervention or its reverse sets with the value it takes and the one it had so a reverse returns it
	Knob   string   `json:"knob,omitempty"`
	Value  *float64 `json:"value,omitempty"`
	Before *float64 `json:"before,omitempty"`
	// The versions nodloop approved in the place that a reanchor takes into the anchor
	Refs []ItemVersion `json:"refs,omitempty"`
	// The intervention a reverse or a resume takes back
	Reverses string `json:"reverses,omitempty"`
	// Set from the proof so no record claims a person without one
	Authority Authority `json:"authority"`
	Proof     Proof     `json:"proof"`
	// The name the write is recorded under
	// Empty on a proposal since no person made it
	Person string `json:"person,omitempty"`
}

// One record an intervention wrote
type Written struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
	Status  string `json:"status"`
}

// What an intervention trace holds as its output
type InterventionOutput struct {
	// The knowledge records it wrote
	Records []Written `json:"records"`
	// The answer event an answer wrote
	Answer string `json:"answer,omitempty"`
	// The cursor a digest answered
	Since string `json:"since,omitempty"`
}

// Whether the intervention wrote a record
func (o InterventionOutput) wrote() bool {
	return len(o.Records) > 0 || o.Answer != ""
}

// The trace of the intervention with what it wrote under the id, or a new id when empty
// 1. the action and the proof are known and the action names what it acts on
// 2. the authority follows the proof
// 3. a person's names the person and a proposal names none and wrote nothing
// 4. the person chose what to act on so the source is manual or command and no draw stands behind it
func (in Intervention) Trace(id, session string, source Source, out InterventionOutput, now time.Time) (Trace, error) {
	if err := in.check(out); err != nil {
		return Trace{}, err
	}
	if !source.SelfSelected() {
		return Trace{}, fmt.Errorf("%w: source %q is not one the person chose", ErrInterventionInvalid, source)
	}
	in.Authority = in.Proof.Authority()
	input, err := json.Marshal(in)
	if err != nil {
		return Trace{}, err
	}
	if out.Records == nil {
		out.Records = []Written{}
	}
	output, err := json.Marshal(out)
	if err != nil {
		return Trace{}, err
	}
	if id == "" {
		id = NewID(now)
	}
	return Trace{
		ID: id, Name: NameIntervention, SessionID: session, Subject: string(in.Action), Time: now, Input: input, Output: output, Source: source,
	}, nil
}

func (in Intervention) check(out InterventionOutput) error {
	switch {
	case !in.Action.Valid():
		return fmt.Errorf("%w: unknown action %q", ErrInterventionInvalid, in.Action)
	case in.Choice != "" && !in.Choice.Valid():
		return fmt.Errorf("%w: unknown choice %q", ErrInterventionInvalid, in.Choice)
	case !in.Proof.Valid():
		return fmt.Errorf("%w: unknown proof %q", ErrInterventionInvalid, in.Proof)
	case in.Proof.Authority() == AuthorityPerson && in.Person == "":
		return fmt.Errorf("%w: a person's intervention names the person", ErrInterventionInvalid)
	case in.Proof.Authority() == AuthorityAgent && (in.Person != "" || out.wrote()):
		return fmt.Errorf("%w: a proposal names no person and writes nothing", ErrInterventionInvalid)
	case !in.names():
		return fmt.Errorf("%w: a %s names %s", ErrInterventionInvalid, in.Action, in.Action.needs())
	}
	return nil
}

// Whether the intervention names what its action acts on
func (in Intervention) names() bool {
	switch in.Action {
	case ActionJudge:
		return in.Choice.Judges() && (in.ID != "" && in.Version > 0 || in.Compaction != "" && in.Choice == ChoiceKeep)
	case ActionAnswer, ActionDismiss:
		return in.closes()
	case ActionPause:
		return in.Target != "" || len(in.Labels) > 0
	case ActionResume, ActionReverse:
		return in.Reverses != ""
	case ActionKnob:
		return in.Knob != "" && in.Value != nil
	case ActionReanchor:
		return in.Target != ""
	}
	return true
}

// Whether an answer or a dismiss names its question, an answer with a choice of a question and a dismiss with none
func (in Intervention) closes() bool {
	if in.Action == ActionDismiss {
		return in.Decide != "" && in.Choice == ""
	}
	return in.Decide != "" && in.Choice != "" && in.Choice != ChoiceScope
}

// What an action names in plain words
func (a Action) needs() string {
	switch a {
	case ActionJudge:
		return "keep, undo or scope with an item version, or keep with a compaction"
	case ActionAnswer:
		return "the question it answers and one of its choices"
	case ActionDismiss:
		return "the question it closes and no choice"
	case ActionPause:
		return "a change kind, a scope of labels or both"
	case ActionResume:
		return "the pause it ends"
	case ActionReverse:
		return "the intervention it reverses"
	case ActionKnob:
		return "the knob and its value"
	}
	return "the producer of its place"
}
