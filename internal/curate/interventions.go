package curate

import (
	"cmp"
	"encoding/json"
	"slices"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// One knob a person may set with the range of SPEC-curate Knobs
type KnobName string

const (
	KnobAuditTier0 KnobName = "audit_tier0" // the audit p of what tier 0 carries out
	KnobAuditTier1 KnobName = "audit_tier1" // the audit p of what tier 1 carries out
	KnobItemHold   KnobName = "item_hold"   // the share of sessions that miss each version nodloop approved
	KnobCycleWeeks KnobName = "cycle_weeks" // the weeks a cycle lasts
)

func KnobNames() []KnobName {
	return []KnobName{KnobAuditTier0, KnobAuditTier1, KnobItemHold, KnobCycleWeeks}
}

// The lowest and highest value a person may set the knob to
func (n KnobName) bounds() (float64, float64) {
	switch n {
	case KnobItemHold:
		return 0, 0.2
	case KnobCycleWeeks:
		return minCycleWeeks, maxCycleWeeks
	}
	return 0.05, 0.5
}

// Whether the value lies in the range of the knob and a cycle length is whole weeks
func (n KnobName) admits(v float64) bool {
	low, high := n.bounds()
	whole := n != KnobCycleWeeks || v == float64(int(v))
	return slices.Contains(KnobNames(), n) && v >= low && v <= high && whole
}

// The value the knobs hold for the knob
func (k Knobs) value(n KnobName) float64 {
	switch n {
	case KnobAuditTier0:
		return k.Audit.Tier0
	case KnobAuditTier1:
		return k.Audit.Tier1
	case KnobItemHold:
		return float64(k.ItemHold)
	}
	return float64(k.CycleWeeks)
}

// The knobs with the knob at the value
func (k Knobs) with(n KnobName, v float64) Knobs {
	switch n {
	case KnobAuditTier0:
		k.Audit.Tier0 = v
	case KnobAuditTier1:
		k.Audit.Tier1 = v
	case KnobItemHold:
		k.ItemHold = knowledge.ItemHold(v)
	case KnobCycleWeeks:
		k.CycleWeeks = int(v)
	}
	return k
}

// One change of the knobs a person made that the next cycle start carries out
type knobSetting struct {
	At time.Time
	// A knob of the range a person sets and its value
	Knob  KnobName
	Value float64
	// The kind and bin a yes to a widening opens and a reverse of that yes closes
	Opening *Opening
	// Whether a yes to the activation ended the shadow or a reverse of that yes started it again
	Activated *bool
	Open      bool
}

func (s knobSetting) apply(k Knobs) Knobs {
	switch {
	case s.Opening != nil && s.Open:
		return k.Widened(*s.Opening)
	case s.Opening != nil:
		k.Open = slices.DeleteFunc(slices.Clone(k.Open), func(o Opening) bool { return o == *s.Opening })
		return k
	case s.Activated != nil:
		k.Activated = *s.Activated
		return k
	}
	return k.with(s.Knob, s.Value)
}

// One intervention as the fold reads it
type intervention struct {
	ID      string
	Time    time.Time
	Session string
	In      trace.Intervention
	Out     trace.InterventionOutput
}

// One answer as the fold reads it
type answered struct {
	ID     string
	Time   time.Time
	Answer Answer
	Out    AnswerOutput
}

// The interventions and the answers of the records as curate keeps their effect
// 1. a person's intervention holds unless a later reverse or resume that holds names it
// 2. a reverse of an answer takes back the answer it wrote and the intervention a yes to a proposal carried out
// 3. an agent's proposal changes nothing until the person answers the question made of it
type Interventions struct {
	// The person's interventions in time order
	person []intervention
	// The agent's proposals in time order with every digest left out
	// A reverse takes back only a person's intervention so every proposal stays
	proposals []intervention
	answers   []answered
	// The ids of the interventions and the answers taken back
	reversed map[string]bool
	// The change each decide asks about
	decides map[string]Change
}

// The fold of the intervention, answer and decide traces in any order
// A trace that does not decode is left out as every reader of the records does
func RestoreInterventions(traces trace.Traces) Interventions {
	f := Interventions{reversed: map[string]bool{}, decides: map[string]Change{}}
	for _, t := range traces {
		f.add(t)
	}
	byTime := func(a, b intervention) int { return cmp.Or(a.Time.Compare(b.Time), cmp.Compare(a.ID, b.ID)) }
	slices.SortFunc(f.person, byTime)
	slices.SortFunc(f.proposals, byTime)
	slices.SortFunc(f.answers, func(a, b answered) int { return cmp.Or(a.Time.Compare(b.Time), cmp.Compare(a.ID, b.ID)) })
	f.reverse()
	return f
}

func (f *Interventions) add(t trace.Trace) {
	switch t.Name {
	case trace.NameIntervention:
		in := intervention{ID: t.ID, Time: t.Time, Session: t.SessionID}
		if json.Unmarshal(t.Input, &in.In) != nil || json.Unmarshal(t.Output, &in.Out) != nil {
			return
		}
		switch {
		case in.In.Proof.Authority() == trace.AuthorityPerson:
			f.person = append(f.person, in)
		case in.In.Action != trace.ActionDigest:
			f.proposals = append(f.proposals, in)
		}
	case trace.NameAnswer:
		a := answered{ID: t.ID, Time: t.Time}
		if json.Unmarshal(t.Input, &a.Answer) == nil && json.Unmarshal(t.Output, &a.Out) == nil {
			f.answers = append(f.answers, a)
		}
	case trace.NameDecide:
		if d, ok := RestoreDecide(t); ok {
			f.decides[d.ID] = d.Change
		}
	}
}

// Marks what a reverse or a resume that holds takes back
// Newest first so whether a reverse holds is known before the one it names is read
func (f *Interventions) reverse() {
	for _, in := range slices.Backward(f.person) {
		if f.reversed[in.ID] || in.In.Reverses == "" {
			continue
		}
		f.reversed[in.In.Reverses] = true
		f.takeBack(in.In.Reverses)
	}
}

// The answer a reversed intervention wrote and the intervention that answer carried out are taken back with it
func (f *Interventions) takeBack(id string) {
	for _, in := range f.person {
		if in.ID != id || in.Out.Answer == "" {
			continue
		}
		f.reversed[in.Out.Answer] = true
		for _, a := range f.answers {
			if a.ID == in.Out.Answer && a.Out.Carried != "" {
				f.reversed[a.Out.Carried] = true
			}
		}
	}
}

// The person's interventions of the action that hold
func (f Interventions) holding(action trace.Action) []intervention {
	var out []intervention
	for _, in := range f.person {
		if in.In.Action == action && !f.reversed[in.ID] {
			out = append(out, in)
		}
	}
	return out
}

// Whether a pause that holds covers a change of the kind in the scope
func (f Interventions) Paused(kind ChangeKind, scope knowledge.RunScope) bool {
	for _, in := range f.holding(trace.ActionPause) {
		if in.pauses(kind, scope) {
			return true
		}
	}
	return false
}

// Whether the pause covers a change of the kind in the scope
// 1. a pause of a kind covers that kind in every scope and a pause of a scope every kind there
// 2. a scope is covered when for each label of the pause it requires none or one of the values the pause names
// So a pause covers every change that may reach a run of its scope
func (in intervention) pauses(kind ChangeKind, scope knowledge.RunScope) bool {
	if in.In.Target != "" && in.In.Target != string(kind) {
		return false
	}
	for key, values := range in.In.Labels {
		required := scope.Labels[key]
		if len(required) > 0 && !slices.ContainsFunc(required, func(v string) bool { return slices.Contains(values, v) }) {
			return false
		}
	}
	return true
}

// When a dismiss that holds closed the question of the decide and false while it is open
func (f Interventions) Dismissed(decide string) (time.Time, bool) {
	var at time.Time
	found := false
	for _, in := range f.holding(trace.ActionDismiss) {
		if in.In.Decide == decide {
			at, found = in.Time, true
		}
	}
	return at, found
}

// Whether a reverse that holds took back the intervention or the answer
// A question whose answer was taken back is open again
func (f Interventions) Reversed(id string) bool {
	return f.reversed[id]
}

// The value the person's newest knob intervention of the knob that holds set and 0 when none holds
// It stands until the person sets the knob again or reverses it so no automatic lowering goes under it
func (f Interventions) setBy(n KnobName) float64 {
	for _, in := range slices.Backward(f.holding(trace.ActionKnob)) {
		if KnobName(in.In.Knob) == n && in.In.Value != nil {
			return *in.In.Value
		}
	}
	return 0
}

// Whether a later setting of the person of the same knob holds
func (f Interventions) setSince(knob intervention) bool {
	i := slices.IndexFunc(f.person, func(in intervention) bool { return in.ID == knob.ID })
	return slices.ContainsFunc(f.person[i+1:], func(in intervention) bool {
		return in.In.Knob == knob.In.Knob && in.In.Value != nil && !f.reversed[in.ID]
	})
}

// The versions nodloop approved that the person took into the anchor with the intervention or the answer that took each
// A reanchor and an answer mine that hold both take
func (f Interventions) Reanchored() map[knowledge.Ref]string {
	out := map[knowledge.Ref]string{}
	for _, in := range f.holding(trace.ActionReanchor) {
		for _, r := range in.In.Refs {
			out[knowledge.Ref{ID: r.ID, Version: r.Version}] = in.ID
		}
	}
	for _, a := range f.answers {
		if a.Answer.Choice != ChoiceMine || f.reversed[a.ID] {
			continue
		}
		for _, r := range a.Answer.Refs {
			out[r] = a.ID
		}
	}
	return out
}

// The knobs with every change the person made after the time carried out in the order made
// The pass carries them out at a cycle start since knobs are fixed inside a cycle
func (f Interventions) Knobs(k Knobs, after time.Time) Knobs {
	for _, s := range f.settings(after) {
		k = s.apply(k)
	}
	return k
}

// The changes of the knobs the person made after the time in the order they were made
// 1. a knob intervention sets its value and a reverse of one sets the value it had before
// 2. a yes to a widening opens its kind and bin and a yes to the activation ends the shadow
// 3. a reverse of such an answer closes the kind and bin again or starts the shadow again and a reverse of that reverse gives the yes again
func (f Interventions) settings(after time.Time) []knobSetting {
	var out []knobSetting
	for _, in := range f.person {
		if !in.Time.After(after) {
			continue
		}
		switch {
		case in.In.Value != nil && (in.In.Action == trace.ActionKnob || in.In.Action == trace.ActionReverse):
			out = append(out, knobSetting{At: in.Time, Knob: KnobName(in.In.Knob), Value: *in.In.Value})
		case in.In.Action == trace.ActionReverse:
			out = append(out, f.unanswered(in)...)
		}
	}
	for _, a := range f.answers {
		if a.Time.After(after) && a.Answer.Choice == ChoiceYes {
			out = append(out, f.opened(a, a.Time, true)...)
		}
	}
	slices.SortStableFunc(out, func(a, b knobSetting) int { return a.At.Compare(b.At) })
	return out
}

// The settings a reverse sets when the chain of the reverses it names ends at an answer yes
// 1. each reverse along the chain turns the setting the other way so a reverse of the reverse of a yes gives the yes again
// 2. the chain is at most as long as the person's interventions so records that name each other end it
func (f Interventions) unanswered(rev intervention) []knobSetting {
	target, on := rev.In.Reverses, false
	for range f.person {
		i := slices.IndexFunc(f.person, func(in intervention) bool { return in.ID == target })
		if i < 0 {
			return nil
		}
		if answer := f.person[i].Out.Answer; answer != "" {
			var out []knobSetting
			for _, a := range f.answers {
				if a.ID == answer && a.Answer.Choice == ChoiceYes {
					out = append(out, f.opened(a, rev.Time, on)...)
				}
			}
			return out
		}
		target, on = f.person[i].In.Reverses, !on
	}
	return nil
}

// What a yes to a widening or the activation sets at the time
func (f Interventions) opened(a answered, at time.Time, on bool) []knobSetting {
	c, ok := f.decides[a.Answer.Decide]
	switch {
	case !ok:
		return nil
	case c.Kind == ChangeWiden && c.Opens != nil:
		return []knobSetting{{At: at, Opening: c.Opens, Open: on}}
	case c.Kind == ChangeActivate:
		return []knobSetting{{At: at, Activated: &on}}
	}
	return nil
}

// The session that submitted the proposal and empty for none
func (f Interventions) session(proposal string) string {
	for _, p := range f.proposals {
		if p.ID == proposal {
			return p.Session
		}
	}
	return ""
}
