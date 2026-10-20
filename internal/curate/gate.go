package curate

import (
	"slices"
)

// One kind of change and the judge bin tier 1 may carry it out in
// A withdrawal asks no judge so its bin is empty
type Opening struct {
	Kind ChangeKind `json:"kind"`
	Bin  Bin        `json:"bin,omitempty"`
}

// The audit p of the tiers that carry out
type Audit struct {
	Tier0 float64 `json:"tier0"`
	Tier1 float64 `json:"tier1"`
}

// The differ rate the answers of the person must show tier 1 stays below and the error its bound allows
type Target struct {
	Rate  float64 `json:"rate"`
	Alpha float64 `json:"alpha"`
}

// The Bernoulli CUSUM over the tier 1 audit answers from the differ rate p0 to p1 with its alarm at h
type CUSUM struct {
	P0 float64 `json:"p0"`
	P1 float64 `json:"p1"`
	H  float64 `json:"h"`
}

// The knobs of the first cycle
func StartKnobs() Knobs {
	return Knobs{
		ItemHold: 0.1, CycleWeeks: 2, Open: []Opening{{Kind: ChangeAdd, Bin: Bin1}, {Kind: ChangeWithdraw}},
		Audit: Audit{Tier0: 0.05, Tier1: 0.15}, Target: Target{Rate: 0.1, Alpha: 0.05}, CUSUM: CUSUM{P0: 0.05, P1: 0.15, H: 2.5},
	}
}

// Whether tier 1 carries out a change of the kind in the bin
func (k Knobs) Opens(o Opening) bool {
	return slices.Contains(k.Open, o)
}
