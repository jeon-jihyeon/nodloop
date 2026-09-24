// Package knowledge is what corrections taught as versioned scoped items applied only after a named approval
package knowledge

import (
	"fmt"
	"slices"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/evidence"
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
	// At least one reference is required
	Evidence Evidence `json:"evidence"`
	Basis    Basis    `json:"basis"`
	Status   Status   `json:"status"`
	// Required once the status leaves candidate
	Approver   string    `json:"approver,omitempty"`
	ApprovedAt time.Time `json:"approved_at,omitzero"`
	// Previous version of the same id
	Supersedes int `json:"supersedes,omitempty"`
	// When this record set its status
	// Every status change is a new record so this is not the creation time of the version
	Time   time.Time `json:"time"`
	Author string    `json:"author"`
}

func (k Knowledge) validate() error {
	switch {
	case k.ID == "":
		return ErrIDRequired
	case k.Version <= 0:
		return ErrVersionInvalid
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
	return nil
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

func (k Knowledge) applies(changeContext evidence.Context, moved Moved, dims Dims) bool {
	if k.Status != StatusApproved || slices.Contains(k.Exceptions, changeContext) {
		return false
	}
	return k.Scope.matches(changeContext, moved, dims)
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
}

// Every trace id the evidence names
func (e Evidence) TraceIDs() []string {
	return slices.Concat(e.FeedbackTraceIDs, e.OutcomeTraceIDs)
}

func (e Evidence) empty() bool {
	return len(e.FeedbackTraceIDs) == 0 && len(e.OutcomeTraceIDs) == 0 && len(e.ParagraphIDs) == 0
}
