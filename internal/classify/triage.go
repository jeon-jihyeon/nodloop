package classify

import "slices"

// The version of the triage rules the critic answers under
const TriagePolicy = "1"

type Nature string

const (
	NaturePreference   Nature = "preference"   // a personal presentation preference
	NatureSafety       Nature = "safety"       // a constraint on harm or authority
	NatureOrganization Nature = "organization" // a shared organization rule
	NatureUnknown      Nature = "unknown"      // no established category
)

func Natures() []Nature {
	return []Nature{NaturePreference, NatureSafety, NatureOrganization, NatureUnknown}
}

type Reversibility string

const (
	ReversibilityH       Reversibility = "H"       // only the answer presentation changes
	ReversibilityM       Reversibility = "M"       // a stored artifact or code style changes
	ReversibilityL       Reversibility = "L"       // external or difficult to undo effects
	ReversibilityUnknown Reversibility = "unknown" // harm or recovery cost is unestablished
)

func Reversibilities() []Reversibility {
	return []Reversibility{ReversibilityH, ReversibilityM, ReversibilityL, ReversibilityUnknown}
}

type Source string

const (
	SourceComplete   Source = "complete"   // every correcting source is direct and complete
	SourceIncomplete Source = "incomplete" // a source may be truncated or summarized
	SourceMissing    Source = "missing"    // no direct correction is available
	SourceScope      Source = "scope"      // a candidate reaches beyond a correction place
)

func Sources() []Source {
	return []Source{SourceComplete, SourceIncomplete, SourceMissing, SourceScope}
}

type Protection string

const (
	ProtectionNone         Protection = "none"         // no protected concern was established
	ProtectionSafety       Protection = "safety"       // safety or permission behavior
	ProtectionOrganization Protection = "organization" // a rule belonging to an organization
	ProtectionMixed        Protection = "mixed"        // distinct concerns in the same candidate
	ProtectionConflict     Protection = "conflict"     // a change against an existing direction
	ProtectionArtifact     Protection = "artifact"     // stored or external work requires its own proof
	ProtectionUnknown      Protection = "unknown"      // no established low risk interpretation
)

func Protections() []Protection {
	return []Protection{ProtectionNone, ProtectionSafety, ProtectionOrganization, ProtectionMixed,
		ProtectionConflict, ProtectionArtifact, ProtectionUnknown}
}

// What the critic described of a lesson beside its answers
// It grants no authority and curate checks the source again
type Triage struct {
	Policy        string        `json:"policy"`
	Classify      string        `json:"classify,omitempty"`
	Nature        Nature        `json:"nature"`
	Mixed         bool          `json:"mixed"`
	Reversibility Reversibility `json:"reversibility"`
	Source        Source        `json:"source"`
	Protected     Protection    `json:"protected"`
	Lasting       bool          `json:"lasting"`
	Faithful      bool          `json:"faithful"`
	Conflict      bool          `json:"conflict"`
}

// Whether every label is one of its set
func (t Triage) Labeled() bool {
	return slices.Contains(Natures(), t.Nature) && slices.Contains(Reversibilities(), t.Reversibility) &&
		slices.Contains(Protections(), t.Protected)
}
