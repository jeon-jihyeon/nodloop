package knowledge

import (
	"fmt"
	"slices"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// Why a version was retired
type Reason string

const (
	ReasonDuplicate  Reason = "duplicate"   // the candidate says what an approved item of its scope says
	ReasonNoEvidence Reason = "no_evidence" // no run its evidence cites still carries a correction
	ReasonTempScope  Reason = "temp_scope"  // its scope lies in a temporary directory no later session works in
	ReasonConflict   Reason = "conflict"    // an extract conflict the critic passed names the version
	ReasonWords      Reason = "words"       // the person's own words asked to drop the version
	ReasonCompaction Reason = "compaction"  // an approved compaction merged it into another version
	ReasonPerson     Reason = "person"      // a person retired it
)

func Reasons() []Reason {
	return []Reason{ReasonDuplicate, ReasonNoEvidence, ReasonTempScope, ReasonConflict, ReasonWords, ReasonCompaction, ReasonPerson}
}

func (r Reason) Valid() bool {
	return slices.Contains(Reasons(), r)
}

// Only a retired record names a reason and it is one of the set
func (k Knowledge) checkReason() error {
	switch {
	case k.Reason == "":
		return nil
	case !k.Reason.Valid():
		return fmt.Errorf("%w: %q", ErrReasonUnknown, k.Reason)
	case k.Status != StatusRetired:
		return fmt.Errorf("%w: the record is %s and only a retire names one", ErrReasonUnknown, k.Status)
	}
	return nil
}

// The name curate approves and retires under
// No caller may write it so the records tell what a person decided from what curate carried out
const ApproverNodloop = "nodloop"

// The name a person decides under
// 1. an empty name fails with ErrApproverRequired
// 2. the reserved name fails with ErrApproverReserved
func CheckApprover(name string) error {
	if name == "" {
		return ErrApproverRequired
	}
	return refuseReserved(name)
}

// Every write a person decides refuses the reserved name from any caller
// before it reads a record so no argument, flag, key or config value can write it
func refuseReserved(name string) error {
	if name == ApproverNodloop {
		return fmt.Errorf("%w: %s", ErrApproverReserved, name)
	}
	return nil
}

// The current approved versions whose latest approve or reaffirm names a person in id order
// curate reads them as the anchor so a version nodloop approved never becomes direction on its own
func (s Set) PersonApproved() Set {
	out := Set{}
	for _, k := range s.Approved() {
		if !k.carriedOut() {
			out = append(out, k)
		}
	}
	return out
}

// The current approved versions whose latest approve or reaffirm names the approver
// approved at since or later and reaching a run of the producer with the labels
func (s Set) ApprovedBy(approver string, since time.Time, producer string, labels trace.Labels) Set {
	out := Set{}
	for _, k := range s.Approved() {
		if k.Approver == approver && !k.ApprovedAt.Before(since) && k.Run != nil && k.Run.Admits(producer, labels) {
			out = append(out, k)
		}
	}
	return out
}

// Whether one current version a person approved reaches every run the scope reaches
// 1. one version and never a union since a run with a label value no recorded run carries yet would fall between the versions of a union
// 2. it reaches them when the scope widens it by none of the rules of ErrScopeWidened
func (s Set) PersonReaches(scope RunScope) bool {
	return slices.ContainsFunc(s.PersonApproved(), func(k Knowledge) bool { return k.Run != nil && !scope.widens(*k.Run) })
}
