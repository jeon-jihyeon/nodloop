package trace

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"slices"
	"time"
)

// Where a decision or an exposure came from
type Source string

const (
	SourcePush    Source = "push"    // a decision of curate or a question the stop hook pushed
	SourceManual  Source = "manual"  // a judgment or a question the person asked for
	SourceCommand Source = "command" // a person's command
)

func Sources() []Source {
	return []Source{SourcePush, SourceManual, SourceCommand}
}

func (s Source) Valid() bool {
	return slices.Contains(Sources(), s)
}

// Whether the person chose what to look at so no probability stands behind the record
func (s Source) SelfSelected() bool {
	return s == SourceManual || s == SourceCommand
}

// The format of a cycle id
const CycleDate = time.DateOnly

// The draw a decide, an expose or an answer was recorded under
// Every number a report estimates divides by the probability recorded here and never by one computed later
type Draw struct {
	// The date of the Monday the cycle began
	CycleID string `json:"cycle_id"`
	// The build version and the first 8 hex of SHA-256 of the knob values of the cycle
	PolicyVersion string `json:"policy_version"`
	// What was drawn: the decide id for an audit and the session for a shadow pick
	Unit string `json:"unit,omitempty"`
	// The seed of the cycle and empty for a hash draw without one
	Seed   string `json:"seed,omitempty"`
	DrawID string `json:"draw_id,omitempty"`
	// The probability of being drawn
	// Nil for a self selected source
	P     *float64 `json:"p,omitempty"`
	Drawn bool     `json:"drawn"`
}

// The draw of the unit for the purpose at p under the seed of the cycle
// 1. the first 8 bytes of SHA-256 of seed, unit and purpose read as a fraction draw the unit when below p
// 2. their hex is the draw id so a reader tells two draws of one unit apart
// So the draw is taken once from the seed and never from model sampling
func NewDraw(cycleID, policyVersion, seed, unit, purpose string, p float64) Draw {
	sum := sha256.Sum256([]byte(seed + ":" + unit + ":" + purpose))
	fraction := float64(binary.BigEndian.Uint64(sum[:8])) / math.MaxUint64
	return Draw{
		CycleID: cycleID, PolicyVersion: policyVersion, Unit: unit, Seed: seed, DrawID: hex.EncodeToString(sum[:8]),
		P: &p, Drawn: fraction < p,
	}
}

// The envelope of a record of the source
// 1. a cycle id is the date of a Monday and the policy version is named
// 2. a self selected source carries no probability and was not drawn
// 3. a pushed record names its unit and its draw id with a probability from 0 to 1 that is above 0 once drawn
func (d Draw) Check(s Source) error {
	if !s.Valid() {
		return fmt.Errorf("%w: unknown source %q", ErrDrawInvalid, s)
	}
	day, err := time.Parse(CycleDate, d.CycleID)
	switch {
	case err != nil || day.Weekday() != time.Monday:
		return fmt.Errorf("%w: cycle %q is no Monday", ErrDrawInvalid, d.CycleID)
	case d.PolicyVersion == "":
		return fmt.Errorf("%w: no policy version", ErrDrawInvalid)
	case s.SelfSelected() && (d.P != nil || d.Drawn):
		return fmt.Errorf("%w: a %s record carries no probability", ErrDrawInvalid, s)
	case s.SelfSelected():
		return nil
	}
	return d.pushed()
}

// A pushed record names its unit and its draw id with a probability from 0 to 1 that is above 0 once drawn
func (d Draw) pushed() error {
	switch {
	case d.P == nil || *d.P < 0 || *d.P > 1 || d.Drawn && *d.P == 0:
		return fmt.Errorf("%w: a pushed record carries a probability from 0 to 1", ErrDrawInvalid)
	case d.Unit == "" || !d.named():
		return fmt.Errorf("%w: a pushed record names its unit and draw", ErrDrawInvalid)
	}
	return nil
}

// Whether the draw id is 16 lowercase hex digits as NewDraw writes it
func (d Draw) named() bool {
	b, err := hex.DecodeString(d.DrawID)
	return err == nil && len(b) == 8 && hex.EncodeToString(b) == d.DrawID
}
