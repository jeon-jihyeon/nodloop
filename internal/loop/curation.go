package loop

import (
	"cmp"
	"context"
	"encoding/json"
	"maps"
	"slices"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// The curation report as one value
// A convenience over the raw records for the person and the knobs of curate and never an integration contract
type CurationReport struct {
	// The start of the runs the report read and zero for every run
	Since       time.Time       `json:"since,omitzero"`
	Cycles      []CurationCycle `json:"cycles"`
	Touchpoints []Touchpoints   `json:"touchpoints"`
	Kinds       []KindRow       `json:"kinds"`
	Calibration Calibration     `json:"calibration"`
	Shadow      ShadowReport    `json:"shadow"`
	Estimates   []Estimate      `json:"estimates"`
	Overturns   Overturns       `json:"overturns"`
	CUSUM       CUSUMReport     `json:"cusum"`
	Shift       []ShiftRow      `json:"shift"`
	Items       []ItemEffect    `json:"items"`
}

// The trace names report curation reads
var curationTraces = []trace.Name{
	trace.NameRun, trace.NameRollup, trace.NameDecide, trace.NameKnob, trace.NameExpose, trace.NameAnswer, trace.NameIntervention,
}

// The curation report with every cycle whole as report effect reads it
// The CLI prints it and curate reads its numbers through cmd so it is no report of a name the MCP tool or the HTTP API serve
func (s Stores) Curation(ctx context.Context, now time.Time) (CurationReport, error) {
	snap, err := s.cycled(ctx, curationTraces)
	if err != nil {
		return CurationReport{}, err
	}
	return snap.Curation(now), nil
}

// The curation report over the curate events and the runs of the snapshot
func (s Snapshot) Curation(now time.Time) CurationReport {
	e := newEvents(s.traces, now)
	asked, shifts, carried := e.units(), e.shifts(), s.carried()
	return CurationReport{
		Since: s.since, Cycles: e.cycleRows(), Touchpoints: e.touchpoints(), Kinds: e.kinds(asked, shifts),
		Calibration: asked.since(e.configStart()).calibration(), Shadow: e.shadow(asked), Estimates: asked.estimates(),
		Overturns: e.overturns(asked, carried, s.items, s.runs), CUSUM: e.cusum(asked), Shift: shifts,
		Items: s.runs.itemEffects(carried, s.rollups),
	}
}

// The versions any record shows nodloop approved
func (s Snapshot) carried() map[knowledge.Ref]bool {
	out := map[knowledge.Ref]bool{}
	for _, k := range s.items {
		if k.Status == knowledge.StatusApproved && k.Approver == knowledge.ApproverNodloop {
			out[knowledge.Ref{ID: k.ID, Version: k.Version}] = true
		}
	}
	return out
}

// The values of the curate records the report reads
// loop never imports curate so they stand here as the strings of the published enums
const (
	questionAudit     = "audit"
	questionShadow    = "shadow"
	questionDirection = "direction"
	questionNotice    = "notice"
	choiceNoAnswer    = "no_answer"
	outcomeConform    = "conform"
	ruleProposal      = "proposal"
	changeWithdraw    = "withdraw"
	actionShadow      = "shadow"
	actionAsk         = "ask"
	// An intervention action a release may add that the overturns count
	actionPause = "pause"
	// The intervention a person asks for to see what was prepared, which counts as a touchpoint whatever its proof
	actionDigest = "digest"
)

// The actions of a decide that carry a change out
var carriedActions = []string{"apply", "withdraw", "drop", "join"}

// The kinds of a decide that ask what curate may do and prepare no change a choice could overturn
var offeredKinds = []string{"widen", "activate"}

// The kinds the code rules of tier 0 carry out with no knob in between
var tier0Kinds = []string{"drop", "join"}

// A kind of change and the judge bin of a decide as curate records them
// A withdrawal asks no judge so its bin is empty
type KindBin struct {
	Kind string `json:"kind"`
	Bin  string `json:"bin,omitempty"`
}

func (o KindBin) compare(other KindBin) int {
	return cmp.Or(cmp.Compare(o.Kind, other.Kind), cmp.Compare(o.Bin, other.Bin))
}

type decideInput struct {
	Kind string `json:"kind"`
}

type decideOutput struct {
	Tier       int      `json:"tier"`
	RuleCode   string   `json:"rule_code"`
	Outcome    string   `json:"outcome"`
	Bin        string   `json:"bin"`
	Action     string   `json:"action"`
	Confidence *float64 `json:"confidence"`
}

// A decide trace as the report reads it
type decideRecord struct {
	trace.Trace
	in  decideInput
	out decideOutput
}

func (d decideRecord) kindBin() KindBin {
	if d.in.Kind == changeWithdraw {
		return KindBin{Kind: d.in.Kind}
	}
	return KindBin{Kind: d.in.Kind, Bin: d.out.Bin}
}

// The cycle the decide was drawn in
func (d decideRecord) cycle() string {
	if d.Draw == nil {
		return ""
	}
	return d.Draw.CycleID
}

type exposeInput struct {
	Kind  string `json:"kind"`
	Runes int    `json:"runes"`
}

// An expose trace as the report reads it
type exposeRecord struct {
	trace.Trace
	in exposeInput
}

type answerInput struct {
	Choice    string          `json:"choice"`
	Decide    string          `json:"decide"`
	Overturn  bool            `json:"overturn"`
	Authority trace.Authority `json:"authority"`
}

// An answer trace as the report reads it
type answerRecord struct {
	trace.Trace
	in answerInput
}

// Whether the person chose one of the choices of the question
func (a answerRecord) chose() bool {
	return a.in.Choice != choiceNoAnswer && a.in.Authority == trace.AuthorityPerson
}

type interventionInput struct {
	Action    string          `json:"action"`
	ID        string          `json:"id"`
	Version   int             `json:"version"`
	Choice    string          `json:"choice"`
	Authority trace.Authority `json:"authority"`
}

// An intervention trace as the report reads it
type interventionRecord struct {
	trace.Trace
	in interventionInput
}

// The judge configuration a knob event records
type judgeConfig struct {
	Model  string            `json:"model"`
	Effort string            `json:"effort"`
	Prompt string            `json:"prompt"`
	Start  time.Time         `json:"start"`
	Sets   [][]knowledge.Ref `json:"sets"`
	// The approvals and the retires each set may hold
	Kept    int `json:"kept"`
	Refused int `json:"refused"`
}

// A change of the model, the effort, the prompt version, the sizes of the sets or the few-shot sets is another judge
func (c judgeConfig) same(other judgeConfig) bool {
	return c.Model == other.Model && c.Effort == other.Effort && c.Prompt == other.Prompt && c.Start.Equal(other.Start) &&
		c.Kept == other.Kept && c.Refused == other.Refused && slices.EqualFunc(c.Sets, other.Sets, slices.Equal[[]knowledge.Ref])
}

type knobInput struct {
	CycleID       string    `json:"cycle_id"`
	Start         time.Time `json:"start"`
	PolicyVersion string    `json:"policy_version"`
	Knobs         struct {
		Activated bool      `json:"activated"`
		Open      []KindBin `json:"open"`
		Target    struct {
			Rate  float64 `json:"rate"`
			Alpha float64 `json:"alpha"`
		} `json:"target"`
		CUSUM cusumKnob `json:"cusum"`
	} `json:"knobs"`
	Judge judgeConfig `json:"judge"`
}

// A knob trace as the report reads it
type knobRecord struct {
	trace.Trace
	in knobInput
}

// Whether tier 1 carries out the kind in the bin by the knob event or the code rules of tier 0 do
func (k knobRecord) opens(o KindBin) bool {
	return slices.Contains(tier0Kinds, o.Kind) || slices.Contains(k.in.Knobs.Open, o)
}

// Whether the knob event names a target the bound and the posterior can read
func (k knobRecord) targeted() bool {
	t := k.in.Knobs.Target
	return t.Rate > 0 && t.Rate < 1 && t.Alpha > 0 && t.Alpha < 1
}

// The curate events of the records joined once
type events struct {
	decides map[string]decideRecord
	exposes map[string]exposeRecord
	// In time order
	answers       []answerRecord
	interventions []interventionRecord
	// The interventions of an agent by id
	proposals map[string]bool
	// In the order of the boundary they start at and then of their time
	knobs  []knobRecord
	cycles trace.Cycles
	now    time.Time
}

func newEvents(traces trace.Traces, now time.Time) events {
	e := events{
		decides: map[string]decideRecord{}, exposes: map[string]exposeRecord{}, proposals: map[string]bool{}, cycles: traces.Cycles(now), now: now,
	}
	for _, tr := range traces {
		e.add(tr)
	}
	slices.SortFunc(e.answers, func(a, b answerRecord) int { return byTime(a.Trace, b.Trace) })
	slices.SortFunc(e.interventions, func(a, b interventionRecord) int { return byTime(a.Trace, b.Trace) })
	slices.SortFunc(e.knobs, func(a, b knobRecord) int { return cmp.Or(a.in.Start.Compare(b.in.Start), byTime(a.Trace, b.Trace)) })
	return e
}

func byTime(a, b trace.Trace) int {
	return cmp.Or(a.Time.Compare(b.Time), cmp.Compare(a.ID, b.ID))
}

// The trace as a curate event and nothing for another name or a record that does not decode
func (e *events) add(tr trace.Trace) {
	switch tr.Name {
	case trace.NameDecide:
		d := decideRecord{Trace: tr}
		if json.Unmarshal(tr.Input, &d.in) == nil && json.Unmarshal(tr.Output, &d.out) == nil {
			e.decides[tr.ID] = d
		}
	case trace.NameExpose:
		x := exposeRecord{Trace: tr}
		if json.Unmarshal(tr.Input, &x.in) == nil {
			e.exposes[tr.ID] = x
		}
	case trace.NameAnswer:
		a := answerRecord{Trace: tr}
		if json.Unmarshal(tr.Input, &a.in) == nil {
			e.answers = append(e.answers, a)
		}
	case trace.NameIntervention:
		in := interventionRecord{Trace: tr}
		if json.Unmarshal(tr.Input, &in.in) == nil {
			e.interventions = append(e.interventions, in)
			e.proposals[tr.ID] = in.in.Authority == trace.AuthorityAgent
		}
	case trace.NameKnob:
		k := knobRecord{Trace: tr}
		if json.Unmarshal(tr.Input, &k.in) == nil && k.in.CycleID != "" {
			e.knobs = append(e.knobs, k)
		}
	}
}

// The newest knob event and false before the first
func (e events) knob() (knobRecord, bool) {
	if len(e.knobs) == 0 {
		return knobRecord{}, false
	}
	return e.knobs[len(e.knobs)-1], true
}

// The knob event in force in the cycle: the newest one starting at its start or before
// A cycle no pass started keeps the knobs of the one before since its knob event ran on by its length
func (e events) knobFor(c trace.Cycle) (knobRecord, bool) {
	var found knobRecord
	ok := false
	for _, k := range e.knobs {
		if !k.in.Start.After(c.Start) {
			found, ok = k, true
		}
	}
	return found, ok
}

// The first cycle of the judge configuration of the newest knob event and empty without one
// A change of configuration starts the bound and the posteriors again since answers under another one describe another judge
func (e events) configStart() string {
	newest, ok := e.knob()
	start := ""
	for i := len(e.cycles) - 1; ok && i >= 0; i-- {
		k, found := e.knobFor(e.cycles[i])
		if !found || !k.in.Judge.same(newest.in.Judge) {
			break
		}
		start = e.cycles[i].ID
	}
	return start
}

// Whether the expose asks about an agent proposal by naming it or a decide of rule code proposal
// A proposal is no judgment until the person answers it
func (e events) proposal(x exposeRecord) bool {
	d, ok := e.decides[x.Ref]
	return e.proposals[x.Ref] || ok && d.out.RuleCode == ruleProposal
}

// One cycle of curate with the knobs it ran under
type CurationCycle struct {
	ID    string    `json:"id"`
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	// Whether the cycle ended by now
	Closed bool `json:"closed"`
	// The policy versions of the knob events of the cycle in time order
	Policies []string `json:"policy_versions"`
	// The judge configuration of the knob event in force
	Model  string `json:"model"`
	Effort string `json:"effort"`
	Prompt string `json:"prompt"`
	// Whether tier 1 carried out what it decided by the knob event in force
	Activated bool `json:"activated"`
}

// One row per cycle in time order
func (e events) cycleRows() []CurationCycle {
	out := []CurationCycle{}
	for _, c := range e.cycles {
		row := CurationCycle{ID: c.ID, Start: c.Start, End: c.End, Closed: !c.End.After(e.now), Policies: []string{}}
		k, _ := e.knobFor(c)
		row.Model, row.Effort, row.Prompt, row.Activated = k.in.Judge.Model, k.in.Judge.Effort, k.in.Judge.Prompt, k.in.Knobs.Activated
		for _, mark := range e.knobs {
			if mark.in.CycleID == c.ID && !slices.Contains(row.Policies, mark.in.PolicyVersion) {
				row.Policies = append(row.Policies, mark.in.PolicyVersion)
			}
		}
		if len(row.Policies) == 0 && k.in.PolicyVersion != "" {
			row.Policies = append(row.Policies, k.in.PolicyVersion)
		}
		out = append(out, row)
	}
	return out
}

// Questions, answers and interventions of the person in one week or one cycle
type Touchpoints struct {
	// week or cycle
	Period string `json:"period"`
	// The Monday it starts on
	Start string `json:"start"`
	// Questions shown by question kind and source with notices and agent proposals apart
	Questions map[string]map[string]int `json:"questions"`
	Notices   int                       `json:"notices"`
	Proposals int                       `json:"proposals"`
	// Sessions shown a question or a proposal
	Sessions int `json:"sessions"`
	// Choices of the person and questions closed with no answer
	Answers  int `json:"answers"`
	NoAnswer int `json:"no_answer"`
	// Interventions of the person by action
	Interventions map[string]int `json:"interventions"`
	// The runes of the questions and proposals that recorded them
	RunesMedian *float64 `json:"runes_median"`
	RunesMax    int      `json:"runes_max"`
}

// The touchpoints of one period while the records are counted
type touch struct {
	row      Touchpoints
	sessions map[string]bool
	runes    []int
}

// The periods a time lies in: the cycle that holds it and its week from Monday UTC
type period struct {
	name, start string
}

func (e events) periods(t time.Time) []period {
	day := t.UTC().Truncate(24 * time.Hour)
	monday := day.AddDate(0, 0, -(int(day.Weekday())+6)%7)
	out := []period{{"week", monday.Format(trace.CycleDate)}}
	for _, c := range e.cycles {
		if c.Holds(t) {
			out = append(out, period{"cycle", c.ID})
		}
	}
	return out
}

// The touchpoints of every cycle then of every week in time order
func (e events) touchpoints() []Touchpoints {
	rows := map[period]*touch{}
	at := func(t time.Time, count func(*touch)) {
		for _, p := range e.periods(t) {
			row, ok := rows[p]
			if !ok {
				row = &touch{
					row:      Touchpoints{Period: p.name, Start: p.start, Questions: map[string]map[string]int{}, Interventions: map[string]int{}},
					sessions: map[string]bool{},
				}
				rows[p] = row
			}
			count(row)
		}
	}
	for _, x := range e.exposes {
		count := (*touch).expose
		if e.proposal(x) {
			count = (*touch).propose
		}
		at(x.Time, func(row *touch) { count(row, x) })
	}
	for _, a := range e.answers {
		at(a.Time, func(row *touch) { row.answer(a) })
	}
	for _, in := range e.interventions {
		if in.in.Authority == trace.AuthorityPerson || in.in.Action == actionDigest {
			at(in.Time, func(row *touch) { row.row.Interventions[in.in.Action]++ })
		}
	}
	keys := slices.SortedFunc(maps.Keys(rows), func(a, b period) int {
		return cmp.Or(cmp.Compare(a.name, b.name), cmp.Compare(a.start, b.start))
	})
	out := make([]Touchpoints, 0, len(keys))
	for _, k := range keys {
		out = append(out, rows[k].finish())
	}
	return out
}

// One expose in the counts of its kind and source or among the notices since a notice asks nothing
func (t *touch) expose(x exposeRecord) {
	if x.in.Kind == questionNotice {
		t.row.Notices++
		return
	}
	if t.row.Questions[x.in.Kind] == nil {
		t.row.Questions[x.in.Kind] = map[string]int{}
	}
	t.row.Questions[x.in.Kind][string(x.Source)]++
	t.asked(x)
}

// One question about an agent proposal apart from the questions of curate
func (t *touch) propose(x exposeRecord) {
	t.row.Proposals++
	t.asked(x)
}

// The session and the runes of a question
func (t *touch) asked(x exposeRecord) {
	t.sessions[x.SessionID] = true
	if x.in.Runes > 0 {
		t.runes = append(t.runes, x.in.Runes)
	}
}

func (t *touch) answer(a answerRecord) {
	switch {
	case a.in.Choice == choiceNoAnswer:
		t.row.NoAnswer++
	case a.chose():
		t.row.Answers++
	}
}

// The row with its sessions and runes summed up
func (t *touch) finish() Touchpoints {
	t.row.Sessions = len(t.sessions)
	slices.Sort(t.runes)
	if n := len(t.runes); n > 0 {
		t.row.RunesMedian = ptr(float64(t.runes[(n-1)/2]+t.runes[n/2]) / 2)
		t.row.RunesMax = t.runes[n-1]
	}
	return t.row
}
