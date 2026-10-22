package loop

import (
	"cmp"
	"maps"
	"slices"
)

// What one grant carried out and what the person said of it
// Kept apart from the judge rows since no judge answer stands behind a delegated change
type DelegatedRow struct {
	// The HMAC of the grant the decides name
	Grant string `json:"grant"`
	// Decides that name the grant and the ones of them that apply
	Decided        int `json:"decided"`
	ApplyDecisions int `json:"apply_decisions"`
	// Applies the audit drew
	Drawn int `json:"drawn"`
	// Questions shown about them and the sessions that showed them
	Exposed  int `json:"exposed"`
	Sessions int `json:"sessions"`
	// Answers to drawn audits with the undos among them and the ones left unanswered
	Answers    int `json:"answers"`
	Overturned int `json:"overturned"`
	NoAnswer   int `json:"no_answer"`
	// The newest answer of each decide the person asked for and the undos among them
	Manual           int `json:"manual"`
	ManualOverturned int `json:"manual_overturned"`
	// Whether the newest knob event holds the grant stopped
	Stopped bool `json:"stopped"`
}

// One row per grant in the order of their HMACs
func (e events) delegated(asked units) []DelegatedRow {
	t := delegatedTally{rows: map[string]*DelegatedRow{}, sessions: map[string]map[string]bool{}}
	for _, d := range e.decides {
		t.decide(d)
	}
	for _, x := range e.exposes {
		t.expose(e.decides[x.Ref], x)
	}
	for _, u := range asked {
		t.answer(u)
	}
	t.manual(e)
	if knob, ok := e.knob(); ok {
		for _, grant := range knob.in.Knobs.DelegationPaused {
			if r := t.rows[grant]; r != nil {
				r.Stopped = true
			}
		}
	}
	return t.finish()
}

type delegatedTally struct {
	rows     map[string]*DelegatedRow
	sessions map[string]map[string]bool
}

func (t delegatedTally) decide(d decideRecord) {
	if d.out.RuleCode != ruleDelegated || d.out.Delegation == "" {
		return
	}
	r := t.rows[d.out.Delegation]
	if r == nil {
		r = &DelegatedRow{Grant: d.out.Delegation}
		t.rows[r.Grant], t.sessions[r.Grant] = r, map[string]bool{}
	}
	r.Decided++
	if d.out.Action == "apply" {
		r.ApplyDecisions++
		if d.Draw != nil && d.Draw.Drawn && d.Draw.P != nil && *d.Draw.P > 0 {
			r.Drawn++
		}
	}
}

func (t delegatedTally) expose(d decideRecord, x exposeRecord) {
	r := t.rows[d.out.Delegation]
	if d.out.RuleCode != ruleDelegated || r == nil || x.in.Kind == questionNotice {
		return
	}
	r.Exposed++
	if x.SessionID != "" {
		t.sessions[r.Grant][x.SessionID] = true
	}
}

func (t delegatedTally) answer(u unit) {
	r := t.rows[u.decide.out.Delegation]
	if u.decide.out.RuleCode != ruleDelegated || u.question != questionAudit || r == nil {
		return
	}
	switch {
	case !u.chose:
		r.NoAnswer++
	case u.differ:
		r.Answers++
		r.Overturned++
	default:
		r.Answers++
	}
}

func (t delegatedTally) manual(e events) {
	latest := map[string]answerRecord{}
	for _, a := range e.answers {
		x, ok := e.exposes[a.Ref]
		if !ok || !a.Source.SelfSelected() || !a.chose() {
			continue
		}
		d, ok := e.decides[cmp.Or(a.in.Decide, x.Ref)]
		if !ok || d.out.RuleCode != ruleDelegated || t.rows[d.out.Delegation] == nil {
			continue
		}
		if old, ok := latest[d.ID]; !ok || a.Time.After(old.Time) || a.Time.Equal(old.Time) && a.ID > old.ID {
			latest[d.ID] = a
		}
	}
	for id, a := range latest {
		r := t.rows[e.decides[id].out.Delegation]
		r.Manual++
		if a.in.Overturn {
			r.ManualOverturned++
		}
	}
}

func (t delegatedTally) finish() []DelegatedRow {
	out := make([]DelegatedRow, 0, len(t.rows))
	for _, grant := range slices.Sorted(maps.Keys(t.rows)) {
		r := t.rows[grant]
		r.Sessions = len(t.sessions[grant])
		out = append(out, *r)
	}
	return out
}
