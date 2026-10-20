package curate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// The knobs of a cycle as curate.json keeps them
type Knobs struct {
	// Whether the person's yes to the activate question ended the shadow so tier 1 carries out what it decides
	// False until then so a mark that does not decode keeps tier 1 in shadow
	Activated bool `json:"activated"`
	// The share of sessions that miss each version nodloop approved
	ItemHold knowledge.ItemHold `json:"item_hold"`
	// The weeks a cycle lasts from its start
	// retain closes a cycle by it so the cycle ends even when no later pass starts the next one
	CycleWeeks int `json:"cycle_weeks,omitempty"`
	// The kinds and bins tier 1 carries out
	// A narrowing takes one out and only the person's yes to a widen question puts one back
	Open []Opening `json:"open,omitempty"`
	// The audit p of each tier that carries out
	Audit Audit `json:"audit,omitzero"`
	// The differ rate tier 1 stays below and the error its bound allows
	Target Target `json:"target,omitzero"`
	CUSUM  CUSUM  `json:"cusum,omitzero"`
}

// The weeks a person may set a cycle to
const (
	minCycleWeeks = 1
	maxCycleWeeks = 4
)

// The item hold the hooks draw
// 1. none while tier 1 runs in shadow so nothing a run receives changes before nodloop approves a version
// 2. none for a share Check refuses so a mark edited by hand never holds every version
func (k Knobs) Hold() knowledge.ItemHold {
	if !k.Activated || k.ItemHold.Check() != nil {
		return 0
	}
	return k.ItemHold
}

// Why a knob event was appended
type KnobCause string

const (
	KnobStart  KnobCause = "start"  // the first pass after a boundary starts the cycle
	KnobNarrow KnobCause = "narrow" // a narrowing inside the cycle that keeps its id
)

func KnobCauses() []KnobCause {
	return []KnobCause{KnobStart, KnobNarrow}
}

// The knobs of a cycle as its knob event records them
// Every fact a consumer needs from curate.json is here since a mark is state a writer replaces
type KnobEvent struct {
	// The date of the Monday the cycle began
	CycleID string `json:"cycle_id"`
	// The boundary the cycle started at whatever pass appended the event
	Start time.Time `json:"start"`
	// Recorded on every decide, expose and answer of the cycle
	PolicyVersion string `json:"policy_version"`
	// The seed every draw of the cycle reads
	Seed  string      `json:"seed"`
	Knobs Knobs       `json:"knobs"`
	Judge JudgeConfig `json:"judge"`
	Cause KnobCause   `json:"cause"`
	// The knob event this one follows: the one of the cycle before at a start and the one of the same cycle at a narrowing
	Before string `json:"before,omitempty"`
}

// The build version with the first 8 hex of SHA-256 of the knobs and the judge as semver build metadata
// So a change of any knob value under one build is a new policy
func (k KnobEvent) Policy(build string) string {
	// Knobs and a judge configuration of strings, numbers and refs always encode
	b, _ := json.Marshal(struct {
		Knobs Knobs       `json:"knobs"`
		Judge JudgeConfig `json:"judge"`
	}{k.Knobs, k.Judge})
	sum := sha256.Sum256(b)
	return build + "+" + hex.EncodeToString(sum[:4])
}

// The knob trace of the event under its policy version of the build
// 1. the cycle starts at 00:00 UTC on a Monday whose date is its id
// 2. the cycle lasts one to four weeks and the item hold is a share Check allows
// 3. the seed and the cause are named
func (k KnobEvent) Trace(build string, now time.Time) (trace.Trace, error) {
	start := k.Start.UTC()
	switch {
	case start.Weekday() != time.Monday || !start.Equal(start.Truncate(24*time.Hour)) || start.Format(trace.CycleDate) != k.CycleID:
		return trace.Trace{}, fmt.Errorf("%w: cycle %q starting %s is no Monday boundary", ErrKnobInvalid, k.CycleID, k.Start.Format(time.RFC3339))
	case k.Knobs.CycleWeeks < minCycleWeeks || k.Knobs.CycleWeeks > maxCycleWeeks:
		return trace.Trace{}, fmt.Errorf("%w: a cycle of %d weeks", ErrKnobInvalid, k.Knobs.CycleWeeks)
	case k.Knobs.ItemHold.Check() != nil:
		return trace.Trace{}, fmt.Errorf("%w: %w", ErrKnobInvalid, k.Knobs.ItemHold.Check())
	case k.Seed == "" || !slices.Contains(KnobCauses(), k.Cause):
		return trace.Trace{}, fmt.Errorf("%w: seed %q and cause %q", ErrKnobInvalid, k.Seed, k.Cause)
	}
	k.Start, k.PolicyVersion = start, k.Policy(build)
	// A knob event of strings, numbers, times and refs always encodes
	input, _ := json.Marshal(k)
	return trace.Trace{ID: trace.NewID(now), Name: trace.NameKnob, Subject: k.CycleID, Time: now, Input: input}, nil
}

// The knob event a knob trace holds and false for a trace that does not decode
func RestoreKnob(t trace.Trace) (KnobEvent, bool) {
	var k KnobEvent
	if t.Name != trace.NameKnob || json.Unmarshal(t.Input, &k) != nil || k.CycleID == "" {
		return KnobEvent{}, false
	}
	return k, true
}
