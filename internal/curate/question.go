package curate

import (
	"encoding/hex"
	"fmt"
	"slices"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// What a question asks of the person
type QuestionKind string

const (
	QuestionDirection QuestionKind = "direction" // a tier 2 change, a reanchor, a widening or the activation the person decides
	QuestionShadow    QuestionKind = "shadow"    // a tier 1 decision in shadow drawn among the due ones
	QuestionAudit     QuestionKind = "audit"     // a change carried out and drawn at its audit p
	QuestionNotice    QuestionKind = "notice"    // what curate approved or withdrew in the place since the last notice
)

func QuestionKinds() []QuestionKind {
	return []QuestionKind{QuestionDirection, QuestionShadow, QuestionAudit, QuestionNotice}
}

// Where the person saw a question or a notice
type Surface string

const (
	SurfaceContext Surface = "additional_context" // the Stop additionalContext the answer of the turn asks
	SurfaceMessage Surface = "system_message"     // the Stop systemMessage line the host shows with no model turn
)

func Surfaces() []Surface {
	return []Surface{SurfaceContext, SurfaceMessage}
}

// A choice a question offers and the record of a question nobody answered
type Choice string

const (
	ChoiceKeep     Choice = "keep"      // apply an add or an update
	ChoiceDrop     Choice = "drop"      // retire an add or an update
	ChoiceMerge    Choice = "merge"     // apply the compaction and then the change
	ChoiceSeparate Choice = "separate"  // leave the items and the change as they are
	ChoiceMine     Choice = "mine"      // take the versions nodloop approved in the place into the anchor
	ChoiceReview   Choice = "review"    // open the digest of the manual path
	ChoiceYes      Choice = "yes"       // open the kind or end the shadow
	ChoiceNotYet   Choice = "notyet"    // keep the kind or the shadow as it is
	ChoiceSame     Choice = "same"      // keep what curate carried out
	ChoiceUndo     Choice = "undo"      // reverse what curate carried out under the name of the person
	ChoiceNoAnswer Choice = "no_answer" // the question went unanswered after its second round
)

func Choices() []Choice {
	return []Choice{
		ChoiceKeep, ChoiceDrop, ChoiceMerge, ChoiceSeparate, ChoiceMine, ChoiceReview, ChoiceYes, ChoiceNotYet, ChoiceSame, ChoiceUndo,
		ChoiceNoAnswer,
	}
}

// Whether a question may offer the choice
func (c Choice) offered() bool {
	return c != ChoiceNoAnswer && slices.Contains(Choices(), c)
}

// The version of the question rules and wording an expose records
// A changed wording is measured again before it ships so each one gets a version
const QuestionRules = "1"

// A question or a notice shown as its expose event records it
type Exposure struct {
	Kind QuestionKind `json:"kind"`
	// The 4 lowercase letters and digits a reply names
	// Empty on a notice
	Key string `json:"key,omitempty"`
	// The version the question asks about
	Change  *knowledge.Ref `json:"change,omitempty"`
	Surface Surface        `json:"surface"`
	Rules   string         `json:"rules"`
	Choices []Choice       `json:"choices,omitempty"`
	// HMAC-SHA256 of the text shown under the key of the record directory
	Text string `json:"text"`
	// 1 when first asked and 2 when a later session asks it again
	Round int `json:"round"`
	// The run of the answer the question followed
	Run string `json:"run,omitempty"`
	// The runes of the text shown so the touchpoints read the size of what was asked without the text
	Runes int `json:"runes,omitempty"`
}

// The runes a question or a notice may show at most
const MaxRunes = 600

// A notice asks nothing and a question names its key, its two choices and what it asks about
func (x Exposure) check(asks string) error {
	b, err := hex.DecodeString(x.Text)
	switch {
	case !slices.Contains(QuestionKinds(), x.Kind) || !slices.Contains(Surfaces(), x.Surface):
		return fmt.Errorf("%w: kind %q on surface %q", ErrExposeInvalid, x.Kind, x.Surface)
	case x.Rules == "" || x.Round < 1 || x.Round > 2 || err != nil || len(b) != 32:
		return fmt.Errorf("%w: rules %q, round %d and the HMAC of the text", ErrExposeInvalid, x.Rules, x.Round)
	case x.Runes < 0 || x.Runes > MaxRunes:
		return fmt.Errorf("%w: a text of %d runes", ErrExposeInvalid, x.Runes)
	case x.Kind == QuestionNotice && (x.Surface != SurfaceMessage || x.Key != "" || len(x.Choices) > 0):
		return fmt.Errorf("%w: a notice is one line that asks nothing", ErrExposeInvalid)
	case x.Kind == QuestionNotice:
		return nil
	}
	return x.question(asks)
}

// A question goes into the answer of the turn with a key of 4 characters and two choices and names what it asks about
// So the AskUserQuestion header nodloop and the key stays within the 12 characters the tool allows
func (x Exposure) question(asks string) error {
	key := len(x.Key) == 4 && !slices.ContainsFunc([]byte(x.Key), func(c byte) bool { return (c < 'a' || c > 'z') && (c < '0' || c > '9') })
	switch {
	case x.Surface != SurfaceContext || !key || asks == "":
		return fmt.Errorf("%w: a question names its key %q and what it asks about", ErrExposeInvalid, x.Key)
	case len(x.Choices) != 2 || !x.Choices[0].offered() || !x.Choices[1].offered() || x.Choices[0] == x.Choices[1]:
		return fmt.Errorf("%w: a question offers two choices and not %v", ErrExposeInvalid, x.Choices)
	}
	return nil
}

// The expose event of a question or a notice shown in the session of the event
// The event names the decide or the proposal a question asks about and the draw it was shown under
func (e Event) Expose(x Exposure, now time.Time) (trace.Trace, error) {
	if e.Session == "" {
		return trace.Trace{}, fmt.Errorf("%w: an expose names its session", ErrExposeInvalid)
	}
	if err := x.check(e.Ref); err != nil {
		return trace.Trace{}, err
	}
	tr, err := e.trace(trace.NameExpose, string(x.Kind), x, nil, now)
	if err != nil {
		return trace.Trace{}, fmt.Errorf("%w: %w", ErrExposeInvalid, err)
	}
	return tr, nil
}

// The person's answer to a question as its answer event records it
type Answer struct {
	Choice Choice `json:"choice"`
	// The decide the question asked about and the version it named
	Decide string         `json:"decide,omitempty"`
	Change *knowledge.Ref `json:"change,omitempty"`
	// Whether the outcome of the judge was kept from the person until the answer
	Hidden bool `json:"hidden"`
	// From the expose to the answer
	DelayMS int64 `json:"delay_ms"`
	// Whether the choice reverses what curate carried out or differs from what the judge predicted
	Overturn bool `json:"overturn"`
	// Set from the proof so no answer claims a person without one
	Authority trace.Authority `json:"authority"`
	Proof     trace.Proof     `json:"proof"`
	// The name the decision is recorded under
	// Empty on no answer
	Person string `json:"person,omitempty"`
}

// The knowledge records an answer wrote
type AnswerOutput struct {
	Records []trace.Written `json:"records"`
}

// The answer event of the expose the event names with the records it wrote
// 1. the choice and the proof are known and the answer names the expose it answers
// 2. an answer of the person has a proof of a person and names the person
// 3. no answer is recorded by code with no proof and wrote nothing
func (e Event) Answer(a Answer, wrote []trace.Written, now time.Time) (trace.Trace, error) {
	unanswered := a.Choice == ChoiceNoAnswer
	switch {
	case !slices.Contains(Choices(), a.Choice) || !a.Proof.Valid() || e.Ref == "":
		return trace.Trace{}, fmt.Errorf("%w: choice %q and proof %q of the expose %q", ErrReplyInvalid, a.Choice, a.Proof, e.Ref)
	case unanswered && (a.Proof != trace.ProofNone || a.Person != "" || len(wrote) > 0):
		return trace.Trace{}, fmt.Errorf("%w: no answer has no proof, names no person and writes nothing", ErrReplyInvalid)
	case !unanswered && (a.Proof.Authority() != trace.AuthorityPerson || a.Person == ""):
		return trace.Trace{}, fmt.Errorf("%w: an answer of the person has the proof of a person and names the person", ErrReplyInvalid)
	}
	a.Authority = a.Proof.Authority()
	tr, err := e.trace(trace.NameAnswer, string(a.Choice), a, AnswerOutput{Records: append([]trace.Written{}, wrote...)}, now)
	if err != nil {
		return trace.Trace{}, fmt.Errorf("%w: %w", ErrReplyInvalid, err)
	}
	return tr, nil
}
