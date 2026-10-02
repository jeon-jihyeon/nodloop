// Package knowledge is what corrections taught as versioned scoped items applied only after a named approval
package knowledge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/veto"
)

type Kind string

const (
	KindMeaning  Kind = "meaning"  // what the data means such as units and relations and comparison conditions
	KindJudgment Kind = "judgment" // when a cause applies with its check order and hold conditions and response
)

func (k Kind) valid() bool {
	switch k {
	case KindMeaning, KindJudgment:
		return true
	}
	return false
}

type Status string

const (
	StatusCandidate  Status = "candidate"  // extracted and never applied
	StatusApproved   Status = "approved"   // active for reviews whose event fits the scope
	StatusRetired    Status = "retired"    // withdrawn by a person
	StatusSuperseded Status = "superseded" // replaced by a newer approved version of the same id
)

func (s Status) valid() bool {
	switch s {
	case StatusCandidate, StatusApproved, StatusRetired, StatusSuperseded:
		return true
	}
	return false
}

// Whether a version in this status can be the current one of its id
func (s Status) active() bool {
	return s == StatusApproved || s == StatusCandidate
}

// Allowed status changes of one version
// Nothing leaves retired or superseded
var transitions = map[Status][]Status{
	StatusCandidate: {StatusApproved, StatusRetired},
	StatusApproved:  {StatusRetired, StatusSuperseded},
}

type Basis string

const (
	BasisStated   Basis = "stated"   // the reviewer said so
	BasisVerified Basis = "verified" // an outcome confirmed it
)

func (b Basis) valid() bool {
	switch b {
	case BasisStated, BasisVerified:
		return true
	}
	return false
}

type Knowledge struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
	Kind    Kind   `json:"kind"`
	Content string `json:"content"`
	// Where the item applies among the runs of one producer
	// Nil on a record of the data review written before 0.6.0, which reaches no run and can only be retired
	Run *RunScope `json:"run,omitempty"`
	// At least one reference is required
	Evidence Evidence `json:"evidence"`
	Basis    Basis    `json:"basis"`
	Status   Status   `json:"status"`
	// Required once the status leaves candidate
	// The person who approved or retired or last reaffirmed the version
	Approver   string    `json:"approver,omitempty"`
	ApprovedAt time.Time `json:"approved_at,omitzero"`
	// Set by a reaffirm on an approved record
	ReviewedAt time.Time `json:"reviewed_at,omitzero"`
	// Previous version of the same id
	Supersedes int `json:"supersedes,omitempty"`
	// The current version of the id when this version was proposed
	// Zero when the id had none or the record predates the field
	Base int `json:"base,omitempty"`
	// When this record set its status
	// Every status change is a new record so this is not the creation time of the version
	Time   time.Time `json:"time"`
	Author string    `json:"author"`
	// A model wrote the content from a correction and no person rewrote it
	// Kept through approval so an approved item still shows where its text came from
	Drafted bool `json:"drafted,omitempty"`
	// A tool call the judgment forbids
	// Approval exports it as a guard veto whose id is the knowledge id and whose reason is the content
	Veto *Veto `json:"veto,omitempty"`
	// The compaction that appended this record
	Compaction string `json:"compaction,omitempty"`
	// Drafts of the compaction proposal on each of its candidates
	// Fewer candidates than this mean the proposal was cut between two appends
	CompactionSize int `json:"compaction_size,omitempty"`
}

// Runes of approved knowledge one review carries
// 1. the smallest supported model context is 200 thousand tokens or about 700 thousand characters at 3.5 characters per token
// 2. knowledge gets one tenth because the rest of the window carries what nodloop does not control
// A safety cap and not a tuning knob since the compaction trigger keeps folders far smaller
const ReviewChars = 70_000

// Approved items one run receives
// One list Claude Code can show on one screen
// Approval never grows a folder past it unless the ledger held more before the cap
const ReviewItems = 10

// Approved items a folder holds before it is crowded
// Half of ReviewItems so a compaction is offered while approval still has room
const FolderItems = 5

// Folders are measured with the text a review sees so the budget and the review cap count the same characters
func (k Knowledge) Text() string {
	return fmt.Sprintf("\n[%s v%d %s] %s\nScope: %s\n", k.ID, k.Version, k.Kind, k.Content, k.reachText())
}

// Tool call rule of a judgment
type Veto struct {
	// One tool name or a list such as `Edit|Write`
	Tool string          `json:"tool"`
	When []VetoCondition `json:"when"`
	// A tool input the rule must block so a pattern that blocks nothing is refused before anyone approves it
	Example map[string]any `json:"example"`
}

// Regexp condition on one `tool_input` field
type VetoCondition struct {
	Field  string `json:"field"`
	Match  string `json:"match"`
	Unless string `json:"unless,omitempty"`
}

// The veto of the item id whose reason is the content
func (v Veto) spec(id, reason string) veto.Spec {
	when := make([]veto.When, 0, len(v.When))
	for _, c := range v.When {
		when = append(when, veto.When{Field: c.Field, Match: c.Match, Unless: c.Unless})
	}
	return veto.Spec{ID: id, Tool: v.Tool, When: when, Reason: reason}
}

func (v Veto) check(id, reason string) error {
	compiled, err := v.spec(id, reason).Veto()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrVetoInvalid, err)
	}
	if unknown := compiled.UnknownTools(); len(unknown) > 0 {
		return fmt.Errorf("%w: %w: %q", ErrVetoInvalid, veto.ErrToolUnknown, unknown)
	}
	if !compiled.Blocks(v.Example) {
		return ErrVetoExample
	}
	return nil
}

func (k Knowledge) validate() error {
	if k.ID == "" {
		return ErrIDRequired
	}
	if err := k.checkVersion(); err != nil {
		return err
	}
	switch {
	case !k.Kind.valid():
		return fmt.Errorf("%w: %q", ErrKindUnknown, k.Kind)
	case k.Content == "":
		return ErrContentRequired
	case k.Evidence.empty():
		return ErrEvidenceRequired
	case !k.Basis.valid():
		return fmt.Errorf("%w: %q", ErrBasisUnknown, k.Basis)
	case !k.Status.valid():
		return fmt.Errorf("%w: %q", ErrStatusUnknown, k.Status)
	case k.Status != StatusCandidate && k.Approver == "":
		return fmt.Errorf("%w: %s needs one", ErrApproverRequired, k.Status)
	case k.Author == "":
		return ErrAuthorRequired
	}
	if err := k.checkScope(); err != nil {
		return err
	}
	if k.Veto == nil {
		return nil
	}
	if k.Kind != KindJudgment {
		return ErrVetoKind
	}
	return k.Veto.check(k.ID, k.Content)
}

// A base names an earlier version so a walk down the bases always ends
func (k Knowledge) checkVersion() error {
	if k.Version <= 0 {
		return ErrVersionInvalid
	}
	if k.Base < 0 || k.Base >= k.Version {
		return fmt.Errorf("%w: base %d of version %d", ErrVersionInvalid, k.Base, k.Version)
	}
	return nil
}

// Fails with ErrScopeRequired on an item without a run scope
// A record of the data review written before 0.6.0 is only read, never proposed or approved again
func (k Knowledge) checkScope() error {
	if k.Run == nil {
		return fmt.Errorf("%w: %s", ErrScopeRequired, k.ID)
	}
	return k.Run.check()
}

// Whether the veto of the item still blocks the example of the old veto
func (k Knowledge) keepsVeto(old Veto) bool {
	return k.Veto != nil && k.Veto.preserves(k.ID, k.Content, old)
}

// Whether both are one record as the store keeps it
// Compared as JSON because a record read back from a file carries its times and examples in decoded form
func (k Knowledge) same(other Knowledge) bool {
	a, errA := json.Marshal(k)
	b, errB := json.Marshal(other)
	return errA == nil && errB == nil && bytes.Equal(a, b)
}

// Same id and version with a new status as the next append only record
func (k Knowledge) transition(status Status, approver string, now time.Time) (Knowledge, error) {
	if approver == "" {
		return Knowledge{}, fmt.Errorf("%w: %s needs one", ErrApproverRequired, status)
	}
	if !slices.Contains(transitions[k.Status], status) {
		return Knowledge{}, fmt.Errorf("%w: %s cannot become %s", ErrTransitionInvalid, k.Status, status)
	}
	return k.changed(status, approver, now), nil
}

// The record of a status change without the transition checks
// Only for a change the caller already knows is allowed
func (k Knowledge) changed(status Status, approver string, now time.Time) Knowledge {
	k.Status, k.Approver, k.Time = status, approver, now
	if status == StatusApproved {
		k.ApprovedAt = now
	}
	return k
}

// The scope in one line so a refusal can quote what a new version must keep
func (k Knowledge) reachText() string {
	if k.Run == nil {
		return "no run, a record of the data review"
	}
	return k.Run.String()
}

// Whether this version stands in for the id instead of the current one with status and version
// 1. an approved version beats anything but a newer approved version
// 2. a candidate beats a retired or superseded one and an older candidate
func (k Knowledge) outranks(status Status, version int) bool {
	switch k.Status {
	case StatusApproved:
		return status != StatusApproved || k.Version > version
	case StatusCandidate:
		return status != StatusApproved && (status != StatusCandidate || k.Version > version)
	}
	return false
}

type Evidence struct {
	FeedbackTraceIDs []string `json:"feedback_trace_ids,omitempty"`
	OutcomeTraceIDs  []string `json:"outcome_trace_ids,omitempty"`
	ParagraphIDs     []string `json:"paragraph_ids,omitempty"`
	// The old versions a compacted item replaces
	Knowledge []Ref `json:"knowledge,omitempty"`
}

// Every trace id the evidence names
func (e Evidence) TraceIDs() []string {
	return slices.Concat(e.FeedbackTraceIDs, e.OutcomeTraceIDs)
}

func (e Evidence) empty() bool {
	return len(e.FeedbackTraceIDs) == 0 && len(e.OutcomeTraceIDs) == 0 && len(e.ParagraphIDs) == 0 && len(e.Knowledge) == 0
}
