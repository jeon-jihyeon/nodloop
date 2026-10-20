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

// Every kind and bin tier 1 may carry out in the order the gate offers them
// 1. a withdrawal first since it only returns to what the person confirmed
// 2. then by bin from the most confident and within a bin by kind
func Openings() []Opening {
	out := []Opening{{Kind: ChangeWithdraw}}
	for _, b := range Bins() {
		for _, k := range []ChangeKind{ChangeAdd, ChangeUpdate, ChangeCompaction} {
			out = append(out, Opening{Kind: k, Bin: b})
		}
	}
	return out
}

func (o Opening) Valid() bool {
	return slices.Contains(Openings(), o)
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

// The moves of the tier 1 audit p
// 1. down to the floor once every open kind and bin met its bound
// 2. up by doubling to the ceiling after a narrowing or a judge shift
const (
	auditFloor   = 0.05
	auditCeiling = 0.3
)

// A posterior that puts the differ rate above the target with more probability than this narrows its kind and bin
const narrowAbove = 0.5

// Closed cycles in shadow on one judge configuration before the activation is offered
const shadowCycles = 2

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

// The knobs once the person said yes to the widen question that opens the kind and bin
// A kind and bin tier 1 may never carry out and one already open change nothing
func (k Knobs) Widened(o Opening) Knobs {
	if !o.Valid() || k.Opens(o) {
		return k
	}
	k.Open = append(slices.Clone(k.Open), o)
	return k
}

// What the curation report of loop found on one kind and bin
// cmd copies it from the report so curate reads plain numbers and never loop
type Gauge struct {
	Opening
	// The answers the bound read and how many of them differ
	Answers int
	Differ  int
	// The probability the posterior puts on a differ rate above the target
	Above float64
	// Whether the anytime valid bound of the answers was met
	Met bool
}

// The numbers the knobs move on
type Numbers struct {
	Gauges []Gauge
	// The kinds and bins with a differ since the CUSUM last stood at zero when it alarmed after the newest knob event
	Alarmed []Opening
	// Whether the judge shifted in the cycle that just closed
	Shift bool
	// Closed cycles tier 1 ran in shadow on the judge configuration of now
	ShadowCycles int
	// Whether the answered shadow decisions of kind add met the bound
	ShadowMet bool
}

// The gauge of the kind and bin and an empty one where loop found no answer
func (n Numbers) gauge(o Opening) Gauge {
	i := slices.IndexFunc(n.Gauges, func(g Gauge) bool { return g.Opening == o })
	if i < 0 {
		return Gauge{Opening: o}
	}
	return n.Gauges[i]
}

// Why a knob moved
type KnobRule string

const (
	KnobPosterior KnobRule = "posterior" // the posterior put the differ rate of a kind and bin above the target
	KnobAlarm     KnobRule = "alarm"     // the CUSUM alarmed with a differ of the kind and bin since it last stood at zero
	KnobRaise     KnobRule = "raise"     // a narrowing or a judge shift doubled the tier 1 audit p
	KnobLower     KnobRule = "lower"     // every open kind and bin met its bound so the tier 1 audit p went down
	KnobPerson    KnobRule = "person"    // a person's knob intervention raised an audit p at once
)

func KnobRules() []KnobRule {
	return []KnobRule{KnobPosterior, KnobAlarm, KnobRaise, KnobLower, KnobPerson}
}

// One move of a knob with the numbers behind it as the knob event records it
type KnobChange struct {
	Rule KnobRule `json:"rule"`
	// The kind and bin a narrowing returned to tier 2
	Opening *Opening `json:"opening,omitempty"`
	// The answers behind a narrowing and how many of them differ
	Answers int `json:"answers,omitempty"`
	Differ  int `json:"differ,omitempty"`
	// The audit p a raise, a lower or a person set
	P float64 `json:"p,omitempty"`
	// The knob a person raised at once
	Knob KnobName `json:"knob,omitempty"`
}

// A widening or the activation the gate offers the person as a question
// Only the person's yes moves the knob so the offer opens nothing
type Offer struct {
	Kind ChangeKind
	// The kind and bin a widening opens and nil for the activation
	Opens *Opening
	// The answers the bound read and how many of them differ
	Answers int
	Differ  int
}

// The change of the decide that prepares the question of the offer
func (o Offer) Change(anchor int) Change {
	return Change{Kind: o.Kind, Opens: o.Opens, Anchor: anchor}
}

// What the knobs do on the numbers of one pass
type Move struct {
	// The knobs from now on
	Knobs Knobs
	// Each knob that moved with the numbers behind it
	// None inside a cycle leaves the knob event out
	Changes []KnobChange
	// The questions the gate offers the person
	Offers []Offer
}

// The knobs of a pass inside a cycle
// Each open kind and bin the alarm names returns to tier 2 at once and the tier 1 audit p goes up one step
// since waiting for the cycle to end would keep carrying out changes the answers started to refuse
func (k Knobs) Alarm(n Numbers) Move {
	m := Move{Knobs: k}
	m.alarm(n)
	if len(m.Changes) > 0 {
		m.raise()
	}
	return m
}

// The knobs of the first pass of a cycle with the tier 1 audit p the person set and 0 when the person set none
// 1. the alarm narrows as inside a cycle and so does a posterior that puts the differ rate above the target with probability over 0.5
// 2. a narrowing or a judge shift doubles the tier 1 audit p up to 0.3 and never lowers one the person set higher
// 3. else the tier 1 audit p goes down to 0.05 once every open kind and bin met its bound and never below what the person set
// 4. the first kind and bin closed before the pass whose bound was met is offered as a widen question
// 5. the activation is offered once two closed cycles ran in shadow on the configuration and the shadow answers of add met the bound
func (k Knobs) Start(n Numbers, set float64) Move {
	m := Move{Knobs: k}
	m.alarm(n)
	for _, g := range n.Gauges {
		if g.Above > narrowAbove && m.Knobs.Opens(g.Opening) {
			m.close(KnobPosterior, g)
		}
	}
	switch {
	case len(m.Changes) > 0 || n.Shift:
		m.raise()
	case m.Knobs.met(n):
		m.lower(set)
	}
	m.offer(k, n)
	return m
}

func (m *Move) alarm(n Numbers) {
	for _, o := range n.Alarmed {
		if m.Knobs.Opens(o) {
			m.close(KnobAlarm, n.gauge(o))
		}
	}
}

// The kind and bin of the gauge back to tier 2 with the answers behind it
func (m *Move) close(rule KnobRule, g Gauge) {
	m.Knobs.Open = slices.DeleteFunc(slices.Clone(m.Knobs.Open), func(o Opening) bool { return o == g.Opening })
	o := g.Opening
	m.Changes = append(m.Changes, KnobChange{Rule: rule, Opening: &o, Answers: g.Answers, Differ: g.Differ})
}

// The tier 1 audit p doubled up to the ceiling
// A p the person set above the ceiling stays where it is
func (m *Move) raise() {
	p := max(m.Knobs.Audit.Tier1, min(auditCeiling, 2*m.Knobs.Audit.Tier1))
	if p == m.Knobs.Audit.Tier1 {
		return
	}
	m.Knobs.Audit.Tier1 = p
	m.Changes = append(m.Changes, KnobChange{Rule: KnobRaise, P: p})
}

// The tier 1 audit p down to the floor or to the p the person set when that is higher
// The person's setting stands until the person changes it so no automatic move goes under it
func (m *Move) lower(set float64) {
	p := max(auditFloor, set)
	if m.Knobs.Audit.Tier1 <= p {
		return
	}
	m.Knobs.Audit.Tier1 = p
	m.Changes = append(m.Changes, KnobChange{Rule: KnobLower, P: p})
}

// Whether the knobs open a kind and bin and every open one met its bound
func (k Knobs) met(n Numbers) bool {
	return len(k.Open) > 0 && !slices.ContainsFunc(k.Open, func(o Opening) bool { return !n.gauge(o).Met })
}

// The first kind and bin closed before the pass whose bound was met and the activation once the shadow earned it
// A kind and bin the pass narrowed is never offered since its bound read the answers that narrowed it
func (m *Move) offer(before Knobs, n Numbers) {
	for _, o := range Openings() {
		if g := n.gauge(o); g.Met && !before.Opens(o) {
			m.Offers = append(m.Offers, Offer{Kind: ChangeWiden, Opens: &o, Answers: g.Answers, Differ: g.Differ})
			break
		}
	}
	if !before.Activated && n.ShadowCycles >= shadowCycles && n.ShadowMet {
		m.Offers = append(m.Offers, Offer{Kind: ChangeActivate})
	}
}
