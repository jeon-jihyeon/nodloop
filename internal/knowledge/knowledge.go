// Package knowledge is what corrections taught as versioned scoped items applied only after a named approval
package knowledge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/evidence"
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
	// Every set field must match the event
	Scope Scope `json:"scope"`
	// Change contexts where it must not apply even when Scope matches
	Exceptions []evidence.Context `json:"exceptions,omitempty"`
	// Where the item applies among the runs of one producer
	// Nil for an item of the data review, which Scope and Exceptions describe
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

// Approved items one review offers as knowledge candidates
// One list Claude Code can show on one screen
// Approval never grows a change context past it so no event offers more items than the list shows
// unless the ledger held more before the cap
const ReviewItems = 10

// Approved items a folder holds before it is crowded
// Half of ReviewItems so a compaction is offered while approval still has room
const FolderItems = 5

// Folders are measured with the text a review sees so the budget and the review cap count the same characters
func (k Knowledge) Text() string {
	if k.Run != nil {
		return fmt.Sprintf("\n[%s v%d %s] %s\nScope: %s\n", k.ID, k.Version, k.Kind, k.Content, k.Run)
	}
	return fmt.Sprintf("\n[%s v%d %s] %s\nScope: %s\n", k.ID, k.Version, k.Kind, k.Content, k.Scope)
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

func (k Knowledge) validate(contexts evidence.Contexts) error {
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
	if err := k.checkScope(contexts); err != nil {
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

// Fails with ErrScopeInvalid when no event could ever match the scope and the exceptions
// 1. a change context or exception the data set does not declare matches no event
// 2. exceptions that cover every change context left leave the item nothing to apply to
// A misspelled exception would otherwise let the item reach the events the person meant to exclude
func (k Knowledge) checkScope(contexts evidence.Contexts) error {
	if k.Run != nil {
		if !k.Scope.Empty() || len(k.Scope.Dims) > 0 || len(k.Exceptions) > 0 {
			return ErrScopeMixed
		}
		return k.Run.check()
	}
	for _, c := range k.Scope.ChangeContexts {
		if !contexts.Valid(c) {
			return fmt.Errorf("%w: change context %q is not one of %v", ErrScopeInvalid, c, contexts.Names())
		}
	}
	for _, c := range k.Exceptions {
		if !contexts.Valid(c) {
			return fmt.Errorf("%w: exception %q is not one of %v", ErrScopeInvalid, c, contexts.Names())
		}
	}
	if k.Excluded(contexts) {
		return fmt.Errorf("%w: the exceptions %v cover every change context of the scope", ErrScopeInvalid, k.Exceptions)
	}
	return nil
}

// Whether the exceptions leave no change context the item could apply to
// An item scoped to no change context may apply to every declared one
func (k Knowledge) Excluded(contexts evidence.Contexts) bool {
	if len(k.Scope.ChangeContexts) == 0 {
		return k.excepts(contexts.Names())
	}
	return k.excepts(k.Scope.ChangeContexts)
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

// The draft over the fields code filled from a correction
// 1. a scope axis the draft sets replaces the filled one so a person can widen or narrow it
// 2. evidence adds up with the filled references first
// 3. basis falls back to the filled one
func (k Knowledge) Filled(scope Scope, ev Evidence, basis Basis) Knowledge {
	if len(k.Scope.ChangeContexts) == 0 {
		k.Scope.ChangeContexts = scope.ChangeContexts
	}
	if len(k.Scope.Metrics) == 0 {
		k.Scope.Metrics = scope.Metrics
	}
	if len(k.Scope.Dims) == 0 {
		k.Scope.Dims = scope.Dims
	}
	k.Evidence = ev.union(k.Evidence)
	if k.Basis == "" {
		k.Basis = basis
	}
	return k
}

// Whether one review may carry both items
// 1. their change contexts intersect
// 2. neither excepts every change context the other is scoped to
// Metrics and dims split nothing because one event often moves several metrics and carries several dims
func (k Knowledge) sharesFolder(other Knowledge) bool {
	if k.Run != nil || other.Run != nil {
		return false
	}
	return k.Scope.Intersects(evidence.Scope{ChangeContexts: other.Scope.ChangeContexts}) &&
		!k.excepts(other.Scope.ChangeContexts) && !other.excepts(k.Scope.ChangeContexts)
}

// An empty list is every change context and no exception covers all of them
func (k Knowledge) excepts(contexts []evidence.Context) bool {
	if len(contexts) == 0 {
		return false
	}
	for _, c := range contexts {
		if !slices.Contains(k.Exceptions, c) {
			return false
		}
	}
	return true
}

func (k Knowledge) applies(changeContext evidence.Context, moved Moved, dims Dims) bool {
	return k.mayApply(changeContext) && k.Scope.admits(changeContext, moved, dims)
}

// Whether the item applies to some event of the change context whatever its metrics and dims
func (k Knowledge) mayApply(changeContext evidence.Context) bool {
	return k.Status == StatusApproved && k.carriedIn(changeContext)
}

// Whether a review of the change context may carry the item whatever its status
// A judgment with a veto acts on tool calls through the guard so no review carries it
// Otherwise vetoes would fill the review caps and a compaction could never free them because it keeps every veto apart
func (k Knowledge) carriedIn(changeContext evidence.Context) bool {
	return k.Veto == nil && k.reaches(changeContext)
}

// The scope and the exceptions in one line so a refusal can quote what a new version must keep
func (k Knowledge) reachText() string {
	if k.Run != nil {
		return k.Run.String()
	}
	if len(k.Exceptions) == 0 {
		return k.Scope.String()
	}
	except := make([]string, len(k.Exceptions))
	for i, c := range k.Exceptions {
		except[i] = string(c)
	}
	return k.Scope.String() + ". except " + strings.Join(except, " and ")
}

// Whether the scope and the exceptions leave the change context to the item whatever its status
// An item with a run scope reaches no change context
func (k Knowledge) reaches(changeContext evidence.Context) bool {
	return k.Run == nil && !slices.Contains(k.Exceptions, changeContext) && k.Scope.MatchesContext(changeContext)
}

// Whether the item reaches every event of the change context and the metric that carries the dims
// 1. an empty metric stands for every metric so only an item without metrics reaches it
// 2. a dim value of the item that the dims lack leaves out the events of every other value
func (k Knowledge) covers(changeContext evidence.Context, metric string, dims map[string]string) bool {
	if !k.reaches(changeContext) {
		return false
	}
	if len(k.Scope.Metrics) > 0 && (metric == "" || !slices.Contains(k.Scope.Metrics, metric)) {
		return false
	}
	for key, value := range k.Scope.Dims {
		if dims[key] != value {
			return false
		}
	}
	return true
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
