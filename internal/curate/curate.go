package curate

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// One write the background prepared for one item version or one signal
type ChangeKind string

const (
	ChangeAdd        ChangeKind = "add"        // approve a new candidate
	ChangeUpdate     ChangeKind = "update"     // approve the next version of an id
	ChangeJoin       ChangeKind = "join"       // add the evidence of a candidate to an older candidate with the same text and drop it
	ChangeDrop       ChangeKind = "drop"       // retire a candidate that cannot teach
	ChangeWithdraw   ChangeKind = "withdraw"   // retire a version nodloop approved on a signal that names it
	ChangeCompaction ChangeKind = "compaction" // approve a prepared compaction of approved items
	ChangeReanchor   ChangeKind = "reanchor"   // ask the person to take the versions nodloop approved in a place as their own
	ChangeWiden      ChangeKind = "widen"      // ask the person to let curate carry out one more kind or bin
	ChangeActivate   ChangeKind = "activate"   // ask the person to end the shadow
)

func ChangeKinds() []ChangeKind {
	return []ChangeKind{
		ChangeAdd, ChangeUpdate, ChangeJoin, ChangeDrop, ChangeWithdraw, ChangeCompaction, ChangeReanchor, ChangeWiden, ChangeActivate,
	}
}

func (k ChangeKind) Valid() bool {
	return slices.Contains(ChangeKinds(), k)
}

// Whether the change acts on one item version
func (k ChangeKind) versioned() bool {
	return slices.Contains([]ChangeKind{ChangeAdd, ChangeUpdate, ChangeJoin, ChangeDrop, ChangeWithdraw}, k)
}

// Whether the knobs offer the change from the numbers of a cycle so it cites no run
func (k ChangeKind) offered() bool {
	return k == ChangeWiden || k == ChangeActivate
}

// The rule that sent a change to its tier
type RuleCode string

const (
	RuleSameAsItem      RuleCode = "same_as_item"      // the candidate text equals an approved item of its scope
	RuleSameAsCandidate RuleCode = "same_as_candidate" // the candidate text equals an older waiting candidate
	RuleNoEvidence      RuleCode = "no_evidence"       // no run its evidence cites still carries a correction
	RuleTempScope       RuleCode = "temp_scope"        // its dir label lies where no later session works
	RulePersonItem      RuleCode = "person_item"       // it touches a version a person approved
	RuleScopeWider      RuleCode = "scope_wider"       // no version a person approved reaches every run its scope reaches
	RuleVeto            RuleCode = "veto"              // it carries a veto or changes one
	RuleRoom            RuleCode = "room"              // its approval needs a compaction
	RuleKindClosed      RuleCode = "kind_closed"       // its kind or the judge bin is not open for tier 1
	RulePaused          RuleCode = "paused"            // a pause of the person covers its kind or its scope
	RuleProposal        RuleCode = "proposal"          // an agent proposal the answer of the person decides
	RuleDrift           RuleCode = "drift"             // versions nodloop approved in the place reach the anchor items there
	RuleJudgeAgainst    RuleCode = "judge_against"     // every judge member found it against an anchor line
	RuleJudgeUndecided  RuleCode = "judge_undecided"   // every judge member found the anchor silent
	RuleJudgeSplit      RuleCode = "judge_split"       // the judge members differ or one gave no answer
)

func RuleCodes() []RuleCode {
	return []RuleCode{
		RuleSameAsItem, RuleSameAsCandidate, RuleNoEvidence, RuleTempScope, RulePersonItem, RuleScopeWider, RuleVeto, RuleRoom,
		RuleKindClosed, RulePaused, RuleProposal, RuleDrift, RuleJudgeAgainst, RuleJudgeUndecided, RuleJudgeSplit,
	}
}

// Empty or one of the set
// A tier 1 decision and an offer of the knobs reach their tier by no rule
func (r RuleCode) known() bool {
	return r == "" || slices.Contains(RuleCodes(), r)
}

// What a decision does
type Action string

const (
	ActionApply    Action = "apply"    // the ledger approves the change as nodloop
	ActionWithdraw Action = "withdraw" // the ledger retires a version nodloop approved
	ActionDrop     Action = "drop"     // the ledger retires a candidate
	ActionJoin     Action = "join"     // the evidence joins an older candidate
	ActionAsk      Action = "ask"      // a question is prepared for the person
	ActionShadow   Action = "shadow"   // decided in shadow and left waiting
	ActionRetry    Action = "retry"    // a judge call failed so the next pass decides the change
)

func Actions() []Action {
	return []Action{ActionApply, ActionWithdraw, ActionDrop, ActionJoin, ActionAsk, ActionShadow, ActionRetry}
}

// Why a change curate refused may still not teach
type FailureClass string

const (
	FailureHard          FailureClass = "hard"          // a scope no session reaches
	FailureRepairable    FailureClass = "repairable"    // a judge failure
	FailureUnderexplored FailureClass = "underexplored" // a single signal
	FailureUnpromising   FailureClass = "unpromising"   // a lesson the person dropped twice from corrections of two sessions
)

func FailureClasses() []FailureClass {
	return []FailureClass{FailureHard, FailureRepairable, FailureUnderexplored, FailureUnpromising}
}

// Empty for a change curate did not refuse or one of the set
func (f FailureClass) known() bool {
	return f == "" || slices.Contains(FailureClasses(), f)
}

// What brings a refused change back
type Reopen string

const (
	ReopenOtherSession   Reopen = "other_session"   // a correction in the same direction from another session
	ReopenPermanentPlace Reopen = "permanent_place" // the same correction outside a temporary path
	ReopenNextPass       Reopen = "next_pass"       // the next pass
	ReopenPersonOnly     Reopen = "person_only"     // only a person
)

func Reopens() []Reopen {
	return []Reopen{ReopenOtherSession, ReopenPermanentPlace, ReopenNextPass, ReopenPersonOnly}
}

// Empty for a change curate did not refuse or one of the set
func (r Reopen) known() bool {
	return r == "" || slices.Contains(Reopens(), r)
}

// The highest tier
// Tier 0 is code and tier 1 the judge and tier 2 the person
const topTier = 2

// The change a decide judged as its input records it
type Change struct {
	Kind ChangeKind `json:"kind"`
	// The item version the change acts on
	ID      string `json:"id,omitempty"`
	Version int    `json:"version,omitempty"`
	// The prepared compaction a change of kind compaction approves with the old items it merges
	// A join names in old the older candidate it adds the evidence to
	Compaction string          `json:"compaction,omitempty"`
	Old        []knowledge.Ref `json:"old,omitempty"`
	// The extract trace that drafted the change
	Extract string `json:"extract,omitempty"`
	// The extract or capture trace whose signal named the version a withdrawal retires
	Signal string `json:"signal,omitempty"`
	// The anchor version the change was judged against
	Anchor int `json:"anchor"`
	// The HMAC of the cumulative diff the judge read
	Diff string `json:"diff,omitempty"`
	// The kind and bin a widening asks to open
	Opens *Opening `json:"opens,omitempty"`
}

// 1. a change of one version names it
// 2. a compaction names the compaction it approves
// 3. a join names the one older candidate it adds the evidence to
// 4. only a widening names the kind and bin it opens
func (c Change) check() error {
	switch {
	case !c.Kind.Valid():
		return fmt.Errorf("%w: unknown change kind %q", ErrDecideInvalid, c.Kind)
	case c.Kind.versioned() && (c.ID == "" || c.Version <= 0):
		return fmt.Errorf("%w: a %s names its item version", ErrDecideInvalid, c.Kind)
	case c.Kind == ChangeCompaction && c.Compaction == "":
		return fmt.Errorf("%w: a compaction names the compaction it approves", ErrDecideInvalid)
	case c.Kind == ChangeJoin && len(c.Old) != 1:
		return fmt.Errorf("%w: a join names the one older candidate it adds the evidence to", ErrDecideInvalid)
	case (c.Kind == ChangeWiden) != (c.Opens != nil) || c.Opens != nil && !c.Opens.Valid():
		return fmt.Errorf("%w: a widening names the kind and bin it opens and no other change names one", ErrDecideInvalid)
	}
	return nil
}

// What a decide found and does as its output records it
type Decision struct {
	Tier     int      `json:"tier"`
	RuleCode RuleCode `json:"rule_code,omitempty"`
	// The classify trace of the judge and empty where no judge ran
	Judge   string  `json:"judge,omitempty"`
	Outcome Outcome `json:"outcome,omitempty"`
	// The share of the judge members that conform
	Vote *float64 `json:"vote,omitempty"`
	Bin  Bin      `json:"bin,omitempty"`
	// The mean confidence the bin reads and nil where no member conforms
	// The Brier score of the judge reads it whole
	Confidence *float64 `json:"confidence,omitempty"`
	Action     Action   `json:"action"`
	// Why a drop or a withdrawal retires with what may still teach and what brings it back
	Reason  knowledge.Reason `json:"reason,omitempty"`
	Failure FailureClass     `json:"failure,omitempty"`
	Reopen  Reopen           `json:"reopen,omitempty"`
}

// A drop or a withdrawal is never closed on one piece of evidence so it names its reason, failure class and reopen condition
func (d Decision) check() error {
	if !d.known() {
		return fmt.Errorf("%w: tier %d, action %q, rule %q, outcome %q, bin %q, reason %q, failure %q, reopen %q",
			ErrDecideInvalid, d.Tier, d.Action, d.RuleCode, d.Outcome, d.Bin, d.Reason, d.Failure, d.Reopen)
	}
	retires := d.Action == ActionDrop || d.Action == ActionWithdraw
	if retires && (d.Reason == "" || d.Failure == "" || d.Reopen == "") {
		return fmt.Errorf("%w: a %s names its reason, failure class and reopen condition", ErrDecideInvalid, d.Action)
	}
	return nil
}

// Whether the decision writes the ledger
func (d Decision) carries() bool {
	return slices.Contains([]Action{ActionApply, ActionWithdraw, ActionDrop, ActionJoin}, d.Action)
}

// 1. the tier is 0, 1 or 2 and the action one of the set
// 2. every other value is empty or one of its set and a confidence lies from 0 to 1
func (d Decision) known() bool {
	return d.Tier >= 0 && d.Tier <= topTier && slices.Contains(Actions(), d.Action) && d.RuleCode.known() &&
		(d.Outcome == "" || d.Outcome.Valid()) && (d.Bin == "" || d.Bin.Valid()) && (d.Reason == "" || d.Reason.Valid()) &&
		d.Failure.known() && d.Reopen.known() && (d.Confidence == nil || *d.Confidence >= 0 && *d.Confidence <= 1)
}

// Where a curate event belongs and the draw it was recorded under
type Event struct {
	// The trace id a decide draws its audit on so curate makes it before the draw
	// Empty for a new id
	ID string
	// The session of an expose or an answer
	Session string
	// The run a decide cites, the decide or proposal an expose asks about and the expose an answer answers
	Ref    string
	Source trace.Source
	Draw   trace.Draw
}

// The trace of the event with its input and output under the draw of its source
// The inputs and outputs of curate events hold strings, numbers, times and refs so they always encode
func (e Event) trace(name trace.Name, subject string, input, output any, now time.Time) (trace.Trace, error) {
	if err := e.Draw.Check(e.Source); err != nil {
		return trace.Trace{}, err
	}
	in, _ := json.Marshal(input)
	tr := trace.Trace{
		ID: e.ID, Name: name, SessionID: e.Session, Subject: subject, Ref: e.Ref, Time: now, Input: in, Draw: &e.Draw, Source: e.Source,
	}
	if tr.ID == "" {
		tr.ID = trace.NewID(now)
	}
	if output != nil {
		tr.Output, _ = json.Marshal(output)
	}
	return tr, nil
}

// The decide event of one change cited by the run of the event
// 1. the change and the decision pass their checks and the decide names its run unless the knobs offered the change
// 2. the audit draws on the id of the decide and a decision of curate is pushed
// So no drafter or proposer can pick an id that avoids the audit
// 3. a retry carries the error of the judge call that failed and no other action carries one
func (e Event) Decide(c Change, d Decision, failed error, now time.Time) (trace.Trace, error) {
	switch {
	case e.ID == "" || e.Ref == "" && !c.Kind.offered():
		return trace.Trace{}, fmt.Errorf("%w: a decide names its id and the run of the change", ErrDecideInvalid)
	case e.Source != trace.SourcePush || e.Draw.Unit != e.ID:
		return trace.Trace{}, fmt.Errorf("%w: a decision of curate is pushed and drawn on its own id", ErrDecideInvalid)
	case (d.Action == ActionRetry) != (failed != nil):
		return trace.Trace{}, fmt.Errorf("%w: only a retry carries the error of its judge call", ErrDecideInvalid)
	}
	if err := errors.Join(c.check(), d.check()); err != nil {
		return trace.Trace{}, err
	}
	tr, err := e.trace(trace.NameDecide, string(c.Kind), c, d, now)
	if err != nil {
		return trace.Trace{}, fmt.Errorf("%w: %w", ErrDecideInvalid, err)
	}
	if failed != nil {
		tr.Error = failed.Error()
	}
	return tr, nil
}
