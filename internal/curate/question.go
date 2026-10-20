package curate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

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

// The two choices the question of the decide offers with the one curate prepared first
// 1. an audit of a change carried out: same or undo
// 2. a widening, the activation and a proposal of an agent: yes or notyet
// 3. drift: mine or review
// 4. a compaction or a change that needs room: merge or separate
// 5. any other change: keep or drop
func (d Decided) Choices() [2]Choice {
	switch {
	case d.Decision.carries():
		return [2]Choice{ChoiceSame, ChoiceUndo}
	case d.Change.Kind.offered() || d.Change.Kind == ChangeIntervention:
		return [2]Choice{ChoiceYes, ChoiceNotYet}
	case d.Decision.RuleCode == RuleDrift:
		return [2]Choice{ChoiceMine, ChoiceReview}
	case d.Change.Kind == ChangeCompaction || d.Decision.RuleCode == RuleRoom:
		return [2]Choice{ChoiceMerge, ChoiceSeparate}
	}
	return [2]Choice{ChoiceKeep, ChoiceDrop}
}

// Whether the choice reverses what curate carried out or differs from what the judge predicted
func (d Decided) overturned(c Choice) bool {
	switch {
	case d.Decision.carries():
		return c == ChoiceUndo
	case d.Decision.Outcome == OutcomeConform:
		return c == ChoiceDrop
	case d.Decision.Outcome == OutcomeAgainst:
		return c == ChoiceKeep
	}
	return false
}

// The kind of question the decide leaves and false for a decide that asks nothing
// A change carried out asks only once the audit drew it
func (d Decided) asks() (QuestionKind, bool) {
	switch {
	case d.Decision.carries():
		return QuestionAudit, d.Drawn
	case d.Decision.Action == ActionAsk:
		return QuestionDirection, true
	case d.Decision.Action == ActionShadow:
		return QuestionShadow, true
	}
	return "", false
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

// One question as the stop asks it with no id, version or number the person cannot see
type Question struct {
	Kind QuestionKind
	// 4 lowercase letters and digits that the AskUserQuestion header names
	Key     string
	Choices [2]Choice
	// The change in one line with a %s for each lesson it quotes
	Change  string
	Lessons []string
	// Why the change needs the person
	Why string
	// The person's own words that started the change
	Words []string
	// What else was prepared, each a short phrase
	Prepared []string
}

const (
	// The line before the question
	// It is short since a question through additionalContext carries no error label to explain
	questionHeading = "nodloop question for you"
	// Runes a quoted text starts at and the step it shrinks by until the question fits MaxRunes
	quoteRunes = 200
	quoteStep  = 20
)

// The question in the lines of SPEC-curate Questions within MaxRunes
// The quoted lessons and words shrink together until the text fits so the reply line always shows
func (q Question) Text() string {
	limit := quoteRunes
	text := q.render(limit)
	for utf8.RuneCountInString(text) > MaxRunes && limit > quoteStep {
		limit -= quoteStep
		text = q.render(limit)
	}
	return string([]rune(text)[:min(utf8.RuneCountInString(text), MaxRunes)])
}

func (q Question) render(limit int) string {
	lessons := make([]any, 0, len(q.Lessons))
	for _, l := range q.Lessons {
		lessons = append(lessons, quoted(l, limit))
	}
	prepared := make([]string, 0, len(q.Words)+len(q.Prepared))
	for _, w := range q.Words {
		prepared = append(prepared, "your words "+quoted(w, limit))
	}
	prepared = append(prepared, q.Prepared...)
	return fmt.Sprintf("One decision for you: %s\nWhy you: %s\nPrepared: %s\nReply %s or %s",
		fmt.Sprintf(q.Change, lessons...), q.Why, strings.Join(prepared, " · "), q.Choices[0], q.Choices[1])
}

// The text on one line in quotes cut at the runes with the cut marked
func quoted(text string, limit int) string {
	r := []rune(strings.Join(strings.Fields(text), " "))
	if len(r) > limit {
		r = append(r[:limit-1], '…')
	}
	return `"` + string(r) + `"`
}

// The question between its heading and the instruction to ask it as Stop additionalContext carries it
// The lines up to Prepared go into the AskUserQuestion call word for word
// A model that worded the question itself left out the person's words on 10 of 10 Haiku and 3 of 10 Sonnet runs
func (q Question) Block() string {
	first, second := q.Choices[0], q.Choices[1]
	return fmt.Sprintf("%s\n%s\nAsk this in one AskUserQuestion call with header \"nodloop %s\", the two choices %s and %s, "+
		"and as its question the lines from One decision to Prepared copied word for word, keeping every quote. "+
		"Where that tool does not exist, end your answer with those lines and the reply keys %s or %s. "+
		"Record nothing and decide nothing yourself.", questionHeading, q.Text(), q.Key, first, second, first, second)
}

// The characters of a question key
const keyAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

// The key of a question about the decide that no key taken holds
// The first 4 bytes of SHA-256 of the decide and a counter pick its characters
// So the AskUserQuestion header nodloop and the key stays within the 12 characters the tool allows
func questionKey(decide string, taken []string) string {
	for n := 0; ; n++ {
		sum := sha256.Sum256(fmt.Appendf(nil, "%s:%d", decide, n))
		key := make([]byte, 4)
		for i := range key {
			key[i] = keyAlphabet[int(sum[i])%len(keyAlphabet)]
		}
		if !slices.Contains(taken, string(key)) {
			return string(key)
		}
	}
}

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
	// The versions nodloop approved in the place that a mine took into the anchor
	Refs []knowledge.Ref `json:"refs,omitempty"`
}

// What an answer wrote
type AnswerOutput struct {
	// The knowledge records it wrote
	Records []trace.Written `json:"records"`
	// The intervention a yes to a proposal carried out under the person's name
	Carried string `json:"carried,omitempty"`
}

// The answer event of the expose or the decide the event and the answer name with what it wrote
// 1. the choice and the proof are known and the answer names the expose it answers or the decide of a question the person picked
// 2. an answer of the person has a proof of a person and names the person
// 3. no answer is recorded by code with no proof and wrote nothing
func (e Event) Answer(a Answer, out AnswerOutput, now time.Time) (trace.Trace, error) {
	unanswered := a.Choice == ChoiceNoAnswer
	switch {
	case !slices.Contains(Choices(), a.Choice) || !a.Proof.Valid() || e.Ref == "" && a.Decide == "":
		return trace.Trace{}, fmt.Errorf("%w: choice %q and proof %q of the expose %q", ErrReplyInvalid, a.Choice, a.Proof, e.Ref)
	case unanswered && (a.Proof != trace.ProofNone || a.Person != "" || len(out.Records) > 0 || out.Carried != ""):
		return trace.Trace{}, fmt.Errorf("%w: no answer has no proof, names no person and writes nothing", ErrReplyInvalid)
	case !unanswered && (a.Proof.Authority() != trace.AuthorityPerson || a.Person == ""):
		return trace.Trace{}, fmt.Errorf("%w: an answer of the person has the proof of a person and names the person", ErrReplyInvalid)
	}
	a.Authority = a.Proof.Authority()
	if out.Records == nil {
		out.Records = []trace.Written{}
	}
	tr, err := e.trace(trace.NameAnswer, string(a.Choice), a, out, now)
	if err != nil {
		return trace.Trace{}, fmt.Errorf("%w: %w", ErrReplyInvalid, err)
	}
	return tr, nil
}
