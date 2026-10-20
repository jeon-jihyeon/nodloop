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
	ActionJudge Action = "judge" // keep, undo or scope one item version or a compaction
)

func (a Action) valid() bool {
	return a == ActionJudge
}

// What a judgment does to its target
type Choice string

const (
	ChoiceKeep  Choice = "keep"  // approves a candidate or a compaction or reaffirms an approved version
	ChoiceUndo  Choice = "undo"  // retires a version
	ChoiceScope Choice = "scope" // approves a candidate in the labels picked
)

func (c Choice) valid() bool {
	return slices.Contains([]Choice{ChoiceKeep, ChoiceUndo, ChoiceScope}, c)
}

// The input of an intervention trace
type Intervention struct {
	Action Action `json:"action"`
	// The item version a judgment acts on
	ID      string `json:"id,omitempty"`
	Version int    `json:"version,omitempty"`
	// The compaction a judgment acts on in place of an item version
	Compaction string `json:"compaction,omitempty"`
	Choice     Choice `json:"choice"`
	// Where a scope choice approves the candidate
	// None reaches every run of its producer
	Labels Labels `json:"labels,omitempty"`
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
type interventionOutput struct {
	Records []Written `json:"records"`
}

// The trace of the intervention with the records it wrote
// 1. the action, the choice and the proof are known and a judgment names an item version or a compaction
// 2. the authority follows the proof
// 3. a person's names the person and a proposal names none and wrote nothing
func (in Intervention) Trace(wrote []Written, session string, now time.Time) (Trace, error) {
	if err := in.check(len(wrote)); err != nil {
		return Trace{}, err
	}
	in.Authority = in.Proof.Authority()
	input, err := json.Marshal(in)
	if err != nil {
		return Trace{}, err
	}
	output, err := json.Marshal(interventionOutput{Records: append([]Written{}, wrote...)})
	if err != nil {
		return Trace{}, err
	}
	return Trace{
		ID: NewID(now), Name: NameIntervention, SessionID: session, Subject: string(in.Action), Time: now, Input: input, Output: output,
	}, nil
}

func (in Intervention) check(wrote int) error {
	switch {
	case !in.Action.valid():
		return fmt.Errorf("%w: unknown action %q", ErrInterventionInvalid, in.Action)
	case !in.Choice.valid():
		return fmt.Errorf("%w: unknown choice %q", ErrInterventionInvalid, in.Choice)
	case !in.Proof.Valid():
		return fmt.Errorf("%w: unknown proof %q", ErrInterventionInvalid, in.Proof)
	case (in.ID == "" || in.Version <= 0) && in.Compaction == "":
		return fmt.Errorf("%w: a judgment names an item version or a compaction", ErrInterventionInvalid)
	case in.Proof.Authority() == AuthorityPerson && in.Person == "":
		return fmt.Errorf("%w: a person's intervention names the person", ErrInterventionInvalid)
	case in.Proof.Authority() == AuthorityAgent && (in.Person != "" || wrote > 0):
		return fmt.Errorf("%w: a proposal names no person and writes nothing", ErrInterventionInvalid)
	}
	return nil
}
