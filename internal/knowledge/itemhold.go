package knowledge

import (
	"fmt"
	"slices"
)

// The share of sessions that miss each version nodloop approved
// A run carries several items at once so only a draw per version tells what one version no person approved does to the runs that get it
type ItemHold float64

// From 0 up to but not including 1 since a share of 1 leaves no session that includes the version
func (q ItemHold) Check() error {
	if q >= 0 && q < 1 {
		return nil
	}
	return fmt.Errorf("%w: %v", ErrItemHoldInvalid, float64(q))
}

// Whether the run of the key misses the version
// 1. the draw bytes of <producer>:<key>:<id>:v<version> read as a fraction fall below the share
// 2. an empty key or a share of 0 never holds
// 3. every run of one key draws alike so a session keeps or misses the version for all its turns
func (q ItemHold) Holds(producer, key string, ref Ref) bool {
	if q <= 0 || key == "" {
		return false
	}
	return drawFraction(fmt.Sprintf("%s:%s:%s:v%d", producer, key, ref.ID, ref.Version)) < float64(q)
}

// The versions of the fitting items the hold keeps from the run of the key in their order
// Only a version nodloop approved is drawn since a person stood behind every other
func (q ItemHold) Held(fitting Set, producer, key string) []Ref {
	var out []Ref
	for _, k := range fitting {
		if ref := (Ref{ID: k.ID, Version: k.Version}); k.carriedOut() && q.Holds(producer, key, ref) {
			out = append(out, ref)
		}
	}
	return out
}

// The input of a run of the key with every fitting version and the versions the hold kept back
// 1. the hold is taken again from the key so every hook of a session holds the same versions
// 2. a share of 0 or an empty key draws nothing and leaves the input as it was before the hold
// 3. a held version leaves applied so applied is eligible less held
// 4. a run the holdout withheld includes no version and the hold keeps none from it
func (q ItemHold) Drawn(in RunInput, fitting Set, producer, key string) RunInput {
	if q <= 0 || key == "" {
		return in
	}
	withheld := len(in.Withheld) > 0
	if !withheld {
		in.Held = q.Held(fitting, producer, key)
		in.Applied = slices.DeleteFunc(slices.Clone(in.Applied), func(r Ref) bool { return slices.Contains(in.Held, r) })
	}
	in.Eligible = make([]Eligible, 0, len(fitting))
	for _, k := range fitting {
		p := 1.0
		switch {
		case withheld:
			p = 0
		case k.carriedOut():
			p = 1 - float64(q)
		}
		in.Eligible = append(in.Eligible, Eligible{Ref: Ref{ID: k.ID, Version: k.Version}, PInclude: p})
	}
	return in
}

// One version a run could receive
type Eligible struct {
	Ref
	// The chance that a run of its session includes the version
	// 1. a version a person approved has 1
	// 2. a version nodloop approved has 1 − q
	// 3. every version of a session the holdout withheld has 0
	PInclude float64 `json:"p_include"`
}

// Whether curate approved the version and no person reaffirmed it since
// The latest approve or reaffirm names who stood behind the version last
func (k Knowledge) carriedOut() bool {
	return k.Approver == ApproverNodloop
}
