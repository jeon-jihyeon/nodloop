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

// Whether curate approved the version and no person reaffirmed it since
// The latest approve or reaffirm names who stood behind the version last
func (k Knowledge) carriedOut() bool {
	return k.Approver == ApproverNodloop
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
	return len(s.PersonApproved().Reaching(scope)) > 0
}

// The items of the set in their order that each reach every run the scope reaches
// One item and never a union for the reason PersonReaches gives
func (s Set) Reaching(scope RunScope) Set {
	out := Set{}
	for _, k := range s {
		if k.Run != nil && !scope.widens(*k.Run) {
			out = append(out, k)
		}
	}
	return out
}

// The reasons nodloop retires under
// A retire by a person and one of an approved compaction name what nodloop never decides
func (r Reason) carriedOut() bool {
	return r.Valid() && r != ReasonPerson && r != ReasonCompaction
}

// A write of nodloop names its decide and a retire one of the reasons nodloop retires under
func checkCarriedOut(reason Reason, decision string) error {
	switch {
	case decision == "":
		return ErrDecisionRequired
	case !reason.carriedOut():
		return fmt.Errorf("%w: nodloop retires under %q never", ErrReasonUnknown, reason)
	}
	return nil
}

// Whether a person decided the version last: approved, superseded or retired it under a name of their own
func (s Set) personDecided(id string, version int) bool {
	k, err := s.Version(id, version)
	return err == nil && k.Approver != "" && !k.carriedOut()
}

// The records of an approval curate carries out as nodloop under the decide
// 1. a candidate with a veto, one built on a version a person decided and one no version a person approved reaches fail with ErrPersonDecides
// 2. so does an approval that supersedes a version a person approved
// 3. every check of an approval holds and every record names the decide
func (s Set) applied(id string, version int, decision string, now time.Time) ([]Knowledge, error) {
	if decision == "" {
		return nil, ErrDecisionRequired
	}
	k, err := s.Version(id, version)
	if err != nil {
		return nil, err
	}
	cur := s.current(id)
	switch {
	case k.Veto != nil:
		return nil, fmt.Errorf("%w: %s v%d carries a veto", ErrPersonDecides, id, version)
	case k.Base > 0 && s.personDecided(id, k.Base):
		return nil, fmt.Errorf("%w: %s v%d was built on v%d, which a person decided", ErrPersonDecides, id, version, k.Base)
	case k.Run == nil || !s.PersonReaches(*k.Run):
		return nil, fmt.Errorf("%w: %s v%d reaches runs no lesson a person approved reaches", ErrPersonDecides, id, version)
	case cur != nil && cur.Status == StatusApproved && !cur.carriedOut():
		return nil, fmt.Errorf("%w: %s v%d would replace v%d, which a person approved", ErrPersonDecides, id, version, cur.Version)
	}
	records, err := s.approval(id, version, ApproverNodloop, now)
	if err != nil {
		return nil, err
	}
	for i := range records {
		records[i].Decision = decision
	}
	return records, nil
}

// The records of a compaction curate carries out as nodloop under the decide
// 1. a compaction that retires or supersedes a version a person approved fails with ErrPersonDecides
// 2. so does one whose new item carries a veto
// 3. every check of a compaction approval holds and every record names the decide
// No new item reaches a run its old items never reached and each of them passed PersonReaches when nodloop approved it
// A version a person approved that reaches them shares their folder so the merge would retire it
func (s Set) appliedCompaction(id, decision string, check Coverage, now time.Time) ([]Knowledge, error) {
	if decision == "" {
		return nil, ErrDecisionRequired
	}
	c, err := s.compaction(id)
	if err != nil {
		return nil, err
	}
	for _, old := range c.Replaced {
		if !old.carriedOut() {
			return nil, fmt.Errorf("%w: compaction %s merges %s v%d, which a person approved", ErrPersonDecides, id, old.ID, old.Version)
		}
	}
	for _, k := range c.Items {
		if k.Veto != nil {
			return nil, fmt.Errorf("%w: compaction %s drafts %s v%d with a veto", ErrPersonDecides, id, k.ID, k.Version)
		}
	}
	records, err := s.compactionApproval(id, ApproverNodloop, check, now)
	if err != nil {
		return nil, err
	}
	for i := range records {
		records[i].Decision = decision
	}
	return records, nil
}

// The retire of a version nodloop approved that curate carries out under the decide
// A version a person approved or reaffirmed last fails with ErrPersonDecides
func (s Set) withdrawal(id string, version int, reason Reason, decision string, now time.Time) (Knowledge, error) {
	if err := checkCarriedOut(reason, decision); err != nil {
		return Knowledge{}, err
	}
	k, err := s.checkApproved(id, version)
	if err != nil {
		return Knowledge{}, err
	}
	if !k.carriedOut() {
		return Knowledge{}, fmt.Errorf("%w: %s v%d was approved by %s", ErrPersonDecides, id, version, k.Approver)
	}
	return k.retiredBy(reason, decision, now), nil
}

// The retire of a waiting candidate that curate carries out under the decide
// Fails with ErrNotCandidate unless the version is the newest candidate of its id
func (s Set) dropped(id string, version int, reason Reason, decision string, now time.Time) (Knowledge, error) {
	if err := checkCarriedOut(reason, decision); err != nil {
		return Knowledge{}, err
	}
	k, err := s.waitingVersion(id, version)
	if err != nil {
		return Knowledge{}, err
	}
	return k.retiredBy(reason, decision, now), nil
}

// The retire record of nodloop with the reason and the decide behind it
// Only for an approved version or a candidate since both may become retired
func (k Knowledge) retiredBy(reason Reason, decision string, now time.Time) Knowledge {
	to := k.changed(StatusRetired, ApproverNodloop, now)
	to.Reason, to.Decision = reason, decision
	return to
}
