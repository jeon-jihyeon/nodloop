package knowledge

import (
	"fmt"
	"slices"
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
