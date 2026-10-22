package curate

import (
	"slices"

	"github.com/jeon-jihyeon/nodloop/internal/classify"
)

// The triage a decide records with the source curate proved and the grant it checked
// The labels come from the critic of the extraction and never grant authority on their own
type Triage struct {
	classify.Triage
	// The HMAC of the grant that covered the candidate when it was decided and empty for none
	Grant string `json:"grant,omitempty"`
}

func (t Triage) valid() bool {
	return t.Policy == classify.TriagePolicy && t.Labeled() && slices.Contains(classify.Sources(), t.Source)
}

// Whether the triage lets a grant cover the candidate
// Only an unmixed lasting preference that changes the answer presentation alone with a complete direct source
func (t Triage) eligible() bool {
	return t.valid() && t.Nature == classify.NaturePreference && !t.Mixed && t.Reversibility == classify.ReversibilityH &&
		t.Source == classify.SourceComplete && t.Protected == classify.ProtectionNone &&
		t.Lasting && t.Faithful && !t.Conflict
}

// Whether a protected concern holds the candidate for the person whatever the judge says
// M and L hold even when the protection label says none since no proof of equal behavior exists
func (t Triage) protected() bool {
	return t.Protected != classify.ProtectionNone && t.Protected != classify.ProtectionUnknown || t.Mixed || t.Conflict ||
		t.Nature == classify.NatureSafety || t.Nature == classify.NatureOrganization ||
		t.Reversibility == classify.ReversibilityM || t.Reversibility == classify.ReversibilityL
}
