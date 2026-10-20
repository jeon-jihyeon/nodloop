package loop

import (
	"cmp"
	"maps"
	"math"
	"slices"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// One question curate pushed with the latest answer of the person or the no answer that closed it
type unit struct {
	decide decideRecord
	// audit, shadow or direction
	question string
	// The probability the question was asked with
	p float64
	// Whether the person chose and whether the choice reversed what curate carried out or differed from what the judge predicted
	chose, differ bool
	at            time.Time
}

// Units in the order of their answers
type units []unit

// The units of the pushed questions about decisions of curate
// 1. a record the person asked for or gave by command and a choice of an agent never count since no draw stands behind them
// 2. a question about an agent proposal, a widening or the activation and a notice sample no judgment of curate
// since none prepares a change the choice could overturn
// 3. the latest answer about a decide counts once
// 4. an audit weighs by the p its decide was drawn at and any other question by the p of its expose
func (e events) units() units {
	latest := map[string]unit{}
	for _, a := range e.answers {
		if u, ok := e.unit(a); ok {
			latest[u.decide.ID] = u
		}
	}
	out := slices.Collect(maps.Values(latest))
	slices.SortFunc(out, func(a, b unit) int { return cmp.Or(a.at.Compare(b.at), cmp.Compare(a.decide.ID, b.decide.ID)) })
	return out
}

func (e events) unit(a answerRecord) (unit, bool) {
	x, asked := e.exposes[a.Ref]
	d, decided := e.decides[cmp.Or(a.in.Decide, x.Ref)]
	switch {
	case !asked || !decided || a.Source != trace.SourcePush || x.Source != trace.SourcePush:
		return unit{}, false
	case x.in.Kind == questionNotice || d.out.RuleCode == ruleProposal || slices.Contains(offeredKinds, d.in.Kind) ||
		a.in.Choice != choiceNoAnswer && !a.chose():
		return unit{}, false
	}
	drawn := x.Draw
	if x.in.Kind == questionAudit {
		drawn = d.Draw
	}
	if drawn == nil || drawn.P == nil || *drawn.P <= 0 {
		return unit{}, false
	}
	return unit{decide: d, question: x.in.Kind, p: *drawn.P, chose: a.chose(), differ: a.chose() && a.in.Overturn, at: a.Time}, true
}

// The units about decides of the cycle or a later one
func (us units) since(cycle string) units {
	return slices.DeleteFunc(slices.Clone(us), func(u unit) bool { return u.decide.cycle() < cycle })
}

// How curate decided one kind of change in one judge bin and how the answers of the person judged it
type KindRow struct {
	Kind string `json:"kind"`
	// Empty for a kind no judge decides
	Bin string `json:"bin"`
	// Decisions since the judge configuration of now started
	Decided int `json:"decided"`
	// Carried out by tier 0 or tier 1
	Carried int `json:"carried"`
	// Prepared as a question for the person
	Asked int `json:"asked"`
	// Carried out and drawn for audit and each p they were drawn at
	Drawn int       `json:"drawn"`
	P     []float64 `json:"p"`
	// Questions about them the stop pushed
	Exposed int `json:"exposed"`
	// The answers the bound reads
	// 1. audit and shadow answers while tier 1 carries the kind and bin out
	// 2. direction answers the judge found conform while it is closed
	Same     int `json:"same"`
	Differ   int `json:"differ"`
	NoAnswer int `json:"no_answer"`
	// The cycle the posterior reads from: the start of the configuration or the newest judge shift that touched the bin
	PosteriorSince   string `json:"posterior_since"`
	PosteriorAnswers int    `json:"posterior_answers"`
	PosteriorDiffer  int    `json:"posterior_differ"`
	// The mean of the Beta(1, 9) posterior of the differ rate
	Mean float64 `json:"posterior_mean"`
	// The probability the posterior puts above the target and nil without a target on the newest knob event
	Above *float64 `json:"above"`
	// The mixture e-value of the answers against the target and the smallest differ rate it bounds them below at α
	E     *float64 `json:"e_value"`
	Bound *float64 `json:"bound"`
	// Whether the code rules of tier 0 or tier 1 by the newest knob event carry it out
	Open     bool `json:"open"`
	BoundMet bool `json:"bound_met"`
}

// The decide a question the stop pushed asks about
func (e events) pushed(x exposeRecord) (decideRecord, bool) {
	d, ok := e.decides[x.Ref]
	return d, ok && x.Source == trace.SourcePush
}

// One row per kind and bin in the order of kind and bin
func (e events) kinds(asked units, shifts []ShiftRow) []KindRow {
	knob, _ := e.knob()
	t := kindTally{rows: map[KindBin]*KindRow{}, knob: knob, start: e.configStart(), shifts: shifts}
	for _, d := range e.decides {
		if d.cycle() >= t.start {
			t.row(d.kindBin()).decide(d)
		}
	}
	for _, x := range e.exposes {
		if d, ok := e.pushed(x); ok && d.cycle() >= t.start {
			t.row(d.kindBin()).Exposed++
		}
	}
	for _, u := range asked.since(t.start) {
		if r := t.row(u.decide.kindBin()); r.reads(u) {
			r.count(u)
		}
	}
	keys := slices.SortedFunc(maps.Keys(t.rows), KindBin.compare)
	out := make([]KindRow, 0, len(keys))
	for _, k := range keys {
		out = append(out, t.rows[k].finish(knob))
	}
	return out
}

// The kind rows while the records are counted
type kindTally struct {
	rows   map[KindBin]*KindRow
	knob   knobRecord
	start  string
	shifts []ShiftRow
}

// The row of the kind and bin made on its first record
// Its posterior reads from the newest shift of the same configuration that touched its bin
func (t kindTally) row(o KindBin) *KindRow {
	r, ok := t.rows[o]
	if ok {
		return r
	}
	r = &KindRow{Kind: o.Kind, Bin: o.Bin, P: []float64{}, Open: t.knob.opens(o), PosteriorSince: t.start}
	for _, s := range t.shifts {
		if s.Shift && !s.Changed && s.Cycle >= r.PosteriorSince && slices.Contains(s.Touched, o.Bin) {
			r.PosteriorSince = s.Cycle
		}
	}
	t.rows[o] = r
	return r
}

func (r *KindRow) decide(d decideRecord) {
	r.Decided++
	switch {
	case slices.Contains(carriedActions, d.out.Action):
		r.Carried++
		if d.Draw != nil && d.Draw.Drawn && d.Draw.P != nil {
			r.Drawn++
			if !slices.Contains(r.P, *d.Draw.P) {
				r.P = append(r.P, *d.Draw.P)
			}
		}
	case d.out.Action == actionAsk:
		r.Asked++
	}
}

// Whether the bound of the row reads the unit
// Answers to direction questions never enter the bound of a kind tier 1 carries out
func (r *KindRow) reads(u unit) bool {
	if r.Open {
		return u.question == questionAudit || u.question == questionShadow
	}
	return u.question == questionDirection && u.decide.out.Outcome == outcomeConform
}

func (r *KindRow) count(u unit) {
	switch {
	case !u.chose:
		r.NoAnswer++
		return
	case u.differ:
		r.Differ++
	default:
		r.Same++
	}
	if u.decide.cycle() >= r.PosteriorSince {
		r.PosteriorAnswers++
		if u.differ {
			r.PosteriorDiffer++
		}
	}
}

// The posterior and the bound against the target of the knob event
func (r *KindRow) finish(knob knobRecord) KindRow {
	slices.Sort(r.P)
	posterior := answerCount{n: r.PosteriorAnswers, k: r.PosteriorDiffer}
	r.Mean = posterior.mean()
	if !knob.targeted() {
		return *r
	}
	target := knob.in.Knobs.Target
	bound := answerCount{n: r.Same + r.Differ, k: r.Differ}
	e := bound.e(target.Rate)
	r.Above, r.E, r.Bound = ptr(posterior.above(target.Rate)), ptr(e), ptr(bound.bound(target.Alpha))
	r.BoundMet = e >= 1/target.Alpha
	return *r
}

// How often the person kept what the judge found conform in each bin
// Counts and intervals only since a calibration error of a perfect judge reads 0.07 to 0.18 under 100 answers
type Calibration struct {
	Bins []CalibrationRow `json:"bins"`
	// The mean squared gap between the confidence of the judge and the person keeping the change
	// Nil without an answer about a decide that recorded its confidence
	Brier *float64 `json:"brier"`
	// The answers the Brier score read
	Scored int `json:"scored"`
}

type CalibrationRow struct {
	Bin      string `json:"bin"`
	Answered int    `json:"answered"`
	Same     int    `json:"same"`
	// Same of answered with its 95 percent Wilson interval
	Rate     *float64 `json:"rate"`
	RateLow  *float64 `json:"rate_low"`
	RateHigh *float64 `json:"rate_high"`
}

// The calibration over the units the person chose on whose decide the judge found conform
func (us units) calibration() Calibration {
	bins := map[string]*CalibrationRow{}
	squares, scored := 0.0, 0
	for _, u := range us {
		if !u.chose || u.decide.out.Outcome != outcomeConform {
			continue
		}
		row, ok := bins[u.decide.out.Bin]
		if !ok {
			row = &CalibrationRow{Bin: u.decide.out.Bin}
			bins[u.decide.out.Bin] = row
		}
		row.Answered++
		kept := 1.0
		if u.differ {
			kept = 0
		} else {
			row.Same++
		}
		if c := u.decide.out.Confidence; c != nil {
			squares += (*c - kept) * (*c - kept)
			scored++
		}
	}
	out := Calibration{Bins: []CalibrationRow{}, Scored: scored}
	for _, b := range slices.Sorted(maps.Keys(bins)) {
		row := bins[b]
		row.Rate, row.RateLow, row.RateHigh = effective{x: float64(row.Same), n: float64(row.Answered)}.wilson()
		out.Bins = append(out.Bins, *row)
	}
	if scored > 0 {
		out.Brier = ptr(squares / float64(scored))
	}
	return out
}

// Tier 1 decided in shadow and how the answers of the person met what the judge predicted
type ShadowReport struct {
	// Closed cycles tier 1 ran in shadow on the judge configuration of now
	Cycles int         `json:"cycles"`
	Kinds  []ShadowRow `json:"kinds"`
}

type ShadowRow struct {
	Kind string `json:"kind"`
	// Decided in shadow and left waiting
	Decided int `json:"decided"`
	// Shown as shadow questions and each p_select they were drawn at
	Asked   int       `json:"asked"`
	PSelect []float64 `json:"p_select"`
	// Choices of the person and those as the judge predicted with their share
	Answered int      `json:"answered"`
	Same     int      `json:"same"`
	Share    *float64 `json:"share"`
	// The anytime valid bound of the answers against the target of the newest knob event
	E        *float64 `json:"e_value"`
	Bound    *float64 `json:"bound"`
	BoundMet bool     `json:"bound_met"`
}

// The shadow rows since the judge configuration of now started in the order of kind
func (e events) shadow(asked units) ShadowReport {
	start := e.configStart()
	rows := shadowRows{}
	for _, d := range e.decides {
		if d.out.Action == actionShadow && d.cycle() >= start {
			rows.of(d.in.Kind).Decided++
		}
	}
	for _, x := range e.exposes {
		if d, ok := e.pushed(x); ok && x.in.Kind == questionShadow && d.cycle() >= start {
			rows.of(d.in.Kind).ask(x)
		}
	}
	for _, u := range asked.since(start) {
		if u.question == questionShadow && u.chose {
			rows.of(u.decide.in.Kind).answer(u)
		}
	}
	knob, _ := e.knob()
	out := ShadowReport{Cycles: e.shadowCycles(start), Kinds: []ShadowRow{}}
	for _, kind := range slices.Sorted(maps.Keys(rows)) {
		out.Kinds = append(out.Kinds, rows[kind].finish(knob))
	}
	return out
}

// The shadow rows by kind while the records are counted
type shadowRows map[string]*ShadowRow

// The row of the kind made on its first record
func (rows shadowRows) of(kind string) *ShadowRow {
	if rows[kind] == nil {
		rows[kind] = &ShadowRow{Kind: kind, PSelect: []float64{}}
	}
	return rows[kind]
}

// One shadow question with the p_select it was drawn at
func (r *ShadowRow) ask(x exposeRecord) {
	r.Asked++
	if x.Draw != nil && x.Draw.P != nil && !slices.Contains(r.PSelect, *x.Draw.P) {
		r.PSelect = append(r.PSelect, *x.Draw.P)
	}
}

func (r *ShadowRow) answer(u unit) {
	r.Answered++
	if !u.differ {
		r.Same++
	}
}

func (r *ShadowRow) finish(knob knobRecord) ShadowRow {
	slices.Sort(r.PSelect)
	r.Share = share(r.Same, r.Answered)
	if !knob.targeted() {
		return *r
	}
	target := knob.in.Knobs.Target
	a := answerCount{n: r.Answered, k: r.Answered - r.Same}
	e := a.e(target.Rate)
	r.E, r.Bound, r.BoundMet = ptr(e), ptr(a.bound(target.Alpha)), e >= 1/target.Alpha
	return *r
}

// The closed cycles of the configuration that starts at the cycle whose knob event kept tier 1 in shadow
func (e events) shadowCycles(start string) int {
	n := 0
	for _, c := range e.cycles {
		k, ok := e.knobFor(c)
		if start != "" && c.ID >= start && !c.End.After(e.now) && ok && !k.in.Knobs.Activated {
			n++
		}
	}
	return n
}

// The differ rate of one tier among the questions about decisions of one cycle
// Tiers stand apart since a pooled rate would weigh a tier 0 audit three times a tier 1 one
type Estimate struct {
	Cycle    string `json:"cycle"`
	Tier     int    `json:"tier"`
	Answered int    `json:"answered"`
	Differ   int    `json:"differ"`
	NoAnswer int    `json:"no_answer"`
	// The Hájek ratio of the differ weighed by one over p with its 95 percent Wilson interval at the Kish effective size
	Rate     *float64 `json:"rate"`
	RateLow  *float64 `json:"rate_low"`
	RateHigh *float64 `json:"rate_high"`
	Kish     *float64 `json:"kish"`
	// The same ratio with every question closed with no answer counted as differ
	Worst *float64 `json:"worst"`
}

// One estimate per cycle and tier in that order
func (us units) estimates() []Estimate {
	type key struct {
		cycle string
		tier  int
	}
	samples := map[key]*sample{}
	for _, u := range us {
		k := key{u.decide.cycle(), u.decide.out.Tier}
		if samples[k] == nil {
			samples[k] = &sample{}
		}
		samples[k].add(u.p, u.chose, u.differ)
	}
	keys := slices.SortedFunc(maps.Keys(samples), func(a, b key) int { return cmp.Or(cmp.Compare(a.cycle, b.cycle), cmp.Compare(a.tier, b.tier)) })
	out := make([]Estimate, 0, len(keys))
	for _, k := range keys {
		s := samples[k]
		rate, kish := s.hajek()
		out = append(out, Estimate{
			Cycle: k.cycle, Tier: k.tier, Answered: s.answered, Differ: s.differ, NoAnswer: s.noAnswer,
			Rate: rate.rate, RateLow: rate.low, RateHigh: rate.high, Kish: kish, Worst: s.worst(),
		})
	}
	return out
}

// The Bernoulli CUSUM over the tier 1 audit answers
type CUSUMReport struct {
	// Tier 1 audit answers read in time order since the judge configuration of now started
	Answers int `json:"answers"`
	// The statistic after the last answer
	S float64 `json:"s"`
	// Each alarm after which S starts again from zero
	Alarms []CUSUMAlarm `json:"alarms"`
	// Whether an alarm came after the newest knob event so the pass returns its kinds and bins to tier 2 at once
	Alarm bool `json:"alarm"`
	// The kinds and bins those alarms named
	Narrow []KindBin `json:"narrow"`
}

type CUSUMAlarm struct {
	// The answer it alarmed at counted from 1
	Step int       `json:"step"`
	Time time.Time `json:"time"`
	// The kinds and bins with a differ since S last stood at zero
	Kinds []KindBin `json:"kinds"`
}

// The CUSUM over the tier 1 audit answers since the judge configuration of now started in their time order
func (e events) cusum(asked units) CUSUMReport {
	knob, _ := e.knob()
	run := cusumRun{knob: knob.in.Knobs.CUSUM, report: CUSUMReport{Alarms: []CUSUMAlarm{}, Narrow: []KindBin{}}}
	for _, u := range asked.since(e.configStart()) {
		if u.decide.out.Tier == 1 && u.question == questionAudit && u.chose {
			run.read(u)
		}
	}
	run.acted(knob.Time)
	return run.report
}

// The parameters of the CUSUM a knob event records
type cusumKnob struct {
	P0 float64 `json:"p0"`
	P1 float64 `json:"p1"`
	H  float64 `json:"h"`
}

// Whether the parameters describe a move of the differ rate up to an alarm the statistic can reach
func (c cusumKnob) readable() bool {
	return c.P0 > 0 && c.P1 > c.P0 && c.P1 < 1 && c.H > 0
}

// The CUSUM while the answers are read
type cusumRun struct {
	knob   cusumKnob
	report CUSUMReport
	// The kinds and bins with a differ since S last stood at zero
	differed []KindBin
}

// One answer in the statistic
// 1. S = max(0, S + w) with w = ln(p1 / p0) for a differ and ln((1 − p1) / (1 − p0)) for a same
// 2. S reaching h alarms with the kinds and bins of a differ since S last stood at zero and starts again from zero
// 3. parameters it cannot read leave S at zero
func (r *cusumRun) read(u unit) {
	r.report.Answers++
	if !r.knob.readable() {
		return
	}
	w := math.Log((1 - r.knob.P1) / (1 - r.knob.P0))
	if u.differ {
		w = math.Log(r.knob.P1 / r.knob.P0)
		if o := u.decide.kindBin(); !slices.Contains(r.differed, o) {
			r.differed = append(r.differed, o)
		}
	}
	r.report.S = max(0, r.report.S+w)
	switch {
	case r.report.S >= r.knob.H:
		r.report.Alarms = append(r.report.Alarms, CUSUMAlarm{Step: r.report.Answers, Time: u.at, Kinds: r.differed})
		r.report.S, r.differed = 0, nil
	case r.report.S == 0:
		r.differed = nil
	}
}

// The alarms after the newest knob event that the pass has yet to narrow on
func (r *cusumRun) acted(knob time.Time) {
	for _, a := range r.report.Alarms {
		if !a.Time.After(knob) {
			continue
		}
		r.report.Alarm = true
		for _, o := range a.Kinds {
			if !slices.Contains(r.report.Narrow, o) {
				r.report.Narrow = append(r.report.Narrow, o)
			}
		}
	}
}
