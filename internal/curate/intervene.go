package curate

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// The payload of one intervention as MCP intervene, POST /v1/interventions and nodloop intervene take it
type Request struct {
	Action trace.Action `json:"action"`
	// An item version as id:version, a compaction, a decide or an expose of a question, an intervention, a knob, a change kind or a producer
	Target string       `json:"target,omitempty"`
	Choice trace.Choice `json:"choice,omitempty"`
	// The labels of a scope choice, a pause, a reanchor or a digest
	Labels trace.Labels `json:"labels,omitempty"`
	// The value of a knob
	Value *float64 `json:"value,omitempty"`
	// The session that submits it so the stop of its turn asks a proposal
	Session string `json:"session,omitempty"`
}

// Who submitted an intervention and how
type Submitter struct {
	Proof trace.Proof
	// The name a person decides under and left out of a proposal
	Person string
	// manual for what the person asked for and command for a person's command
	Source trace.Source
}

// Where a proposal becomes due at once
type Marks interface {
	Hold() (*Held, error)
}

// What an intervener reads besides the ledger and the traces
type InterveneSetting struct {
	// The build the policy version of a knob event names
	Build string
	// The passed coverage check of a compaction as compact keeps it
	Coverage func(ctx context.Context, compaction string) (knowledge.Coverage, error)
	// curate.json so a proposal is due at once and nil where no pass runs such as nodloop-server
	Marks Marks
	// The cursor of the record pages that starts after a trace
	Cursor func(at time.Time, id string) string
	// The cursor of the record pages that starts after a ledger record
	KnowledgeCursor func(k record.Knowledge) string
}

// Carries out the interventions of the write side
// 1. a person's takes effect at once through the ledger and the records curate folds
// 2. an agent's changes nothing and becomes a question of the person
// 3. a digest changes nothing and is never a proposal
type Intervener struct {
	ledger *knowledge.Ledger
	traces TraceStore
	s      InterveneSetting
	now    func() time.Time
}

func NewIntervener(ledger *knowledge.Ledger, traces TraceStore, s InterveneSetting, now func() time.Time) *Intervener {
	return &Intervener{ledger: ledger, traces: traces, s: s, now: now}
}

// What one intervention recorded
type Intervened struct {
	// The intervention as recorded
	Trace trace.Trace
	// The knowledge records it wrote
	Wrote knowledge.Set
	// The answer event an answer wrote
	Answer string
	// The cursor a digest answered
	Since string
	// The decide that asks the person about a proposal when its question was made due at once
	Decide string
}

// Records one intervention and carries out a person's
// A failed export of the approved items rides on the error with ErrExport after the intervention is recorded
func (iv *Intervener) Intervene(ctx context.Context, r Request, by Submitter) (Intervened, error) {
	s, err := iv.read(ctx)
	if err != nil {
		return Intervened{}, err
	}
	in, err := iv.resolve(ctx, r, s)
	if err != nil {
		return Intervened{}, err
	}
	in.Proof = by.Proof
	if by.Proof.Authority() == trace.AuthorityPerson {
		if err := knowledge.CheckApprover(by.Person); err != nil {
			return Intervened{}, err
		}
		in.Person = by.Person
	}
	switch {
	case in.Action == trace.ActionDigest:
		return iv.digest(ctx, in, r.Session, by, s)
	case by.Proof.Authority() == trace.AuthorityAgent:
		return iv.propose(ctx, in, r.Session, by, s)
	}
	return iv.carry(ctx, in, r.Session, by, s)
}

// The records an intervention reads
type state struct {
	all   knowledge.Set
	ints  Interventions
	knobs knobTraces
	// The decides and the exposes by id
	decides map[string]Decided
	exposes map[string]trace.Trace
	// The draw each decide was recorded under by its id
	draws map[string]trace.Draw
	// The exposes newest first with what each showed
	shown shownExposes
	// The newest anchor version
	anchors AnchorVersion
}

// The traces every intervention reads
// No run is among them since the capture records every reply through them and a record directory holds thousands
// The scope of a judgment and the place of a mine read the runs they need
var interveneNames = []trace.Name{
	trace.NameIntervention, trace.NameAnswer, trace.NameDecide, trace.NameExpose, trace.NameKnob, trace.NameAnchor,
}

// The state of a write over the ledger as it stands now
func (iv *Intervener) read(ctx context.Context) (state, error) {
	all, err := iv.ledger.All(ctx)
	if err != nil {
		return state{}, err
	}
	return iv.readOver(ctx, all)
}

// The state of the traces every intervention reads over the ledger the caller read
func (iv *Intervener) readOver(ctx context.Context, all knowledge.Set) (state, error) {
	s := state{all: all, decides: map[string]Decided{}, exposes: map[string]trace.Trace{}, draws: map[string]trace.Draw{}}
	var folded trace.Traces
	for _, name := range interveneNames {
		found, err := iv.traces.List(ctx, record.TraceFilter{Name: string(name)})
		if err != nil {
			return state{}, err
		}
		ts := trace.RestoreAll(found)
		s.add(name, ts)
		if name == trace.NameIntervention || name == trace.NameAnswer || name == trace.NameDecide {
			folded = append(folded, ts...)
		}
	}
	s.ints = RestoreInterventions(folded)
	return s, nil
}

// The traces of one name newest first in what the state reads
func (s *state) add(name trace.Name, ts trace.Traces) {
	switch name {
	case trace.NameDecide:
		for _, t := range ts {
			d, ok := RestoreDecide(t)
			if ok {
				s.decides[d.ID] = d
			}
			if ok && t.Draw != nil {
				s.draws[d.ID] = *t.Draw
			}
		}
	case trace.NameExpose:
		for _, t := range ts {
			s.exposes[t.ID] = t
		}
		s.shown = shownOf(ts)
	case trace.NameKnob:
		s.knobs = knobTraces(ts)
	case trace.NameAnchor:
		if a, err := RestoreAnchor(ts.Newest()); err == nil {
			s.anchors = a
		}
	}
}

// The intervention of the request with what it names found in the records
// A target the records do not hold and a value outside its range fail with ErrTargetInvalid
func (iv *Intervener) resolve(ctx context.Context, r Request, s state) (trace.Intervention, error) {
	in := trace.Intervention{Action: r.Action, Target: r.Target, Choice: r.Choice, Labels: r.Labels}
	var err error
	switch r.Action {
	case trace.ActionJudge:
		err = iv.judgeTarget(ctx, &in, s)
	case trace.ActionAnswer, trace.ActionDismiss:
		err = s.question(&in)
	case trace.ActionPause:
		err = pauseTarget(&in)
	case trace.ActionResume:
		err = s.pauseOf(&in)
	case trace.ActionKnob:
		err = s.knobTarget(&in, r.Value)
	case trace.ActionReanchor:
		err = s.placeOf(&in)
	case trace.ActionReverse:
		err = s.reverseTarget(&in)
	case trace.ActionDigest:
	default:
		err = fmt.Errorf("%w: unknown action %q", ErrTargetInvalid, r.Action)
	}
	return in, err
}

// The item version id:version and false for any other target
func itemVersion(target string) (string, int, bool) {
	id, v, ok := strings.Cut(target, ":")
	n, err := strconv.Atoi(strings.TrimPrefix(v, "v"))
	return id, n, ok && id != "" && err == nil && n > 0
}

// The item version or the compaction a judgment acts on as the target names it or as a decide about it names it
// Either one is found in the records so a form never names what the judgment cannot carry out
func (iv *Intervener) judgeTarget(ctx context.Context, in *trace.Intervention, s state) error {
	id, v, versioned := itemVersion(in.Target)
	compaction := in.Target
	if d, ok := s.decides[in.Target]; ok {
		id, v, versioned, compaction, in.Decide = d.Change.ID, d.Change.Version, d.Change.ID != "", d.Change.Compaction, d.ID
	}
	if versioned {
		if _, err := s.all.Version(id, v); err != nil {
			return fmt.Errorf("%w: %w", ErrTargetInvalid, err)
		}
		in.ID, in.Version = id, v
		return nil
	}
	if _, err := iv.ledger.Compaction(ctx, compaction); err != nil {
		return fmt.Errorf("%w: %q names no item version as id:version, decide or compaction: %w", ErrTargetInvalid, in.Target, err)
	}
	in.Compaction = compaction
	return nil
}

// The open question an answer or a dismiss closes as its decide or an expose of it names it
// 1. a question an answer that holds settled or a dismiss closed is no longer open
// 2. an answer takes one of the two choices of the question
func (s state) question(in *trace.Intervention) error {
	id := in.Target
	if x, ok := s.exposes[id]; ok {
		id = x.Ref
	}
	d, ok := s.decides[id]
	_, asks := d.asks()
	_, dismissed := s.ints.Dismissed(id)
	choices := d.Choices()
	switch {
	case !ok || !asks:
		return fmt.Errorf("%w: %q names no question", ErrTargetInvalid, in.Target)
	case s.answered(id) || dismissed:
		return fmt.Errorf("%w: the question %q is closed", ErrTargetInvalid, in.Target)
	case in.Action == trace.ActionAnswer && !slices.Contains(choices[:], Choice(in.Choice)):
		return fmt.Errorf("%w: the question offers %s and %s and not %q", ErrTargetInvalid, choices[0], choices[1], in.Choice)
	}
	in.Decide, in.ID, in.Version, in.Compaction = d.ID, d.Change.ID, d.Change.Version, d.Change.Compaction
	return nil
}

// Whether an answer that holds settled the decide
func (s state) answered(decide string) bool {
	return slices.ContainsFunc(s.ints.answers, func(a answered) bool { return a.Answer.Decide == decide && !s.ints.Reversed(a.ID) })
}

// A pause names a kind tier 1 may carry out or none and labels that each name a value
// A label without a value would cover only the scopes that never name it
func pauseTarget(in *trace.Intervention) error {
	if in.Target != "" && !slices.Contains([]ChangeKind{ChangeAdd, ChangeUpdate, ChangeCompaction}, ChangeKind(in.Target)) {
		return fmt.Errorf("%w: a pause names the kind add, update or compaction and not %q", ErrTargetInvalid, in.Target)
	}
	labels, err := in.Labels.Normalized()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrTargetInvalid, err)
	}
	in.Labels = labels
	return nil
}

// The pause a resume ends
func (s state) pauseOf(in *trace.Intervention) error {
	if p, ok := s.holds(in.Target); !ok || p.In.Action != trace.ActionPause {
		return fmt.Errorf("%w: %q names no pause that holds", ErrTargetInvalid, in.Target)
	}
	in.Reverses = in.Target
	return nil
}

// The person's intervention of the id that no reverse took back
func (s state) holds(id string) (intervention, bool) {
	i := slices.IndexFunc(s.ints.person, func(in intervention) bool { return in.ID == id })
	if i < 0 || s.ints.Reversed(id) {
		return intervention{}, false
	}
	return s.ints.person[i], true
}

// The knob and its value within its range with the value the next cycle start would give it without this setting
// A reverse returns the knob there so a setting the person made before it in the same cycle still holds
func (s state) knobTarget(in *trace.Intervention, value *float64) error {
	n := KnobName(in.Target)
	if value == nil || !n.admits(*value) {
		return fmt.Errorf("%w: the knob %q takes a value within its range", ErrTargetInvalid, in.Target)
	}
	before := s.pending().value(n)
	in.Knob, in.Value, in.Before = string(n), value, &before
	return nil
}

// The knobs the next cycle start gives as the records stand
// The knobs of the newest knob event or the start knobs before the first with every setting of the person since the cycle started
func (s state) pending() Knobs {
	k := StartKnobs()
	if ev, ok := s.knobEvent(); ok {
		k = ev.Knobs
	}
	return s.ints.Knobs(k, s.knobs.started())
}

func (s state) knobEvent() (KnobEvent, bool) {
	for _, t := range s.knobs {
		if ev, ok := RestoreKnob(t); ok {
			return ev, true
		}
	}
	return KnobEvent{}, false
}

// The place of a reanchor with the versions nodloop approved there
func (s state) placeOf(in *trace.Intervention) error {
	for _, k := range s.all.ApprovedBy(knowledge.ApproverNodloop, time.Time{}, in.Target, in.Labels) {
		in.Refs = append(in.Refs, trace.ItemVersion{ID: k.ID, Version: k.Version})
	}
	if len(in.Refs) == 0 {
		return fmt.Errorf("%w: no version nodloop approved reaches the place %s %s", ErrTargetInvalid, in.Target, in.Labels)
	}
	return nil
}

// The person's intervention a reverse takes back with the value a knob returns to
// 1. a digest changed nothing so nothing reverses it
// 2. a knob a later setting of the person changed since is refused as the ledger refuses an id another write changed
func (s state) reverseTarget(in *trace.Intervention) error {
	x, ok := s.holds(in.Target)
	if !ok || x.In.Action == trace.ActionDigest {
		return fmt.Errorf("%w: %q names no intervention of a person that holds", ErrTargetInvalid, in.Target)
	}
	in.Reverses = in.Target
	for _, y := range []intervention{s.carried(x), x} {
		if y.In.Value == nil || y.In.Before == nil {
			continue
		}
		if s.ints.setSince(y) {
			return fmt.Errorf("%w: %w: a later setting of %s holds", ErrTargetInvalid, knowledge.ErrReverseStale, y.In.Knob)
		}
		in.Knob, in.Value, in.Before = y.In.Knob, y.In.Before, y.In.Value
	}
	return nil
}

// The intervention a yes of the answer of x carried out and the zero intervention for none
func (s state) carried(x intervention) intervention {
	for _, a := range s.ints.answers {
		if a.ID == x.Out.Answer && a.Out.Carried != "" {
			y, _ := s.holds(a.Out.Carried)
			return y
		}
	}
	return intervention{}
}

// The ids whose records a reverse of x takes back: x, the answer it wrote and the intervention that answer carried out
func (s state) stamps(x intervention) []string {
	out := []string{x.ID}
	if x.Out.Answer != "" {
		out = append(out, x.Out.Answer)
	}
	if y := s.carried(x); y.ID != "" {
		out = append(out, y.ID)
	}
	return out
}

// The draw envelope of a record the person chose under the newest knob event
func (s state) selfDraw() (trace.Draw, error) {
	ev, ok := s.knobEvent()
	if !ok {
		return trace.Draw{}, fmt.Errorf("%w: no cycle has started so no question is open", ErrTargetInvalid)
	}
	return trace.Draw{CycleID: ev.CycleID, PolicyVersion: ev.PolicyVersion}, nil
}

// Records the digest with the cursor after the person's last decision
func (iv *Intervener) digest(ctx context.Context, in trace.Intervention, session string, by Submitter, s state) (Intervened, error) {
	out := trace.InterventionOutput{Since: iv.since(s)}
	tr, err := iv.record(ctx, "", in, session, by, out)
	return Intervened{Trace: tr, Since: out.Since}, err
}

// The cursor after the person's last decision on record and empty before the first
// 1. a decision is a person's intervention but a digest, a person's answer or a ledger record a person wrote
// So an approval at a terminal that no trace names counts too
// 2. a trace and a ledger record of one time give the trace since the trace names the records its write made
func (iv *Intervener) since(s state) string {
	last := s.decidedLast()
	at, id := last.Time, ""
	for _, x := range s.ints.person {
		if x.In.Action != trace.ActionDigest && !x.Time.Before(at) {
			at, id = x.Time, x.ID
		}
	}
	for _, a := range s.ints.answers {
		if a.Answer.Authority == trace.AuthorityPerson && !a.Time.Before(at) {
			at, id = a.Time, a.ID
		}
	}
	switch {
	case id != "" && iv.s.Cursor != nil:
		return iv.s.Cursor(at, id)
	case id == "" && last.ID != "" && iv.s.KnowledgeCursor != nil:
		return iv.s.KnowledgeCursor(last.Record())
	}
	return ""
}

// The newest ledger record a person wrote and the zero record for none
// Every status but a candidate names who decided it
func (s state) decidedLast() knowledge.Knowledge {
	var last knowledge.Knowledge
	for _, k := range s.all {
		if k.Status != knowledge.StatusCandidate && k.Approver != knowledge.ApproverNodloop && k.Time.After(last.Time) {
			last = k
		}
	}
	return last
}

// Records an agent's proposal and makes its question due at once where curate.json is at hand
// Where it is not or another pass holds it the next pass makes the question
func (iv *Intervener) propose(ctx context.Context, in trace.Intervention, session string, by Submitter, s state) (Intervened, error) {
	tr, err := iv.record(ctx, "", in, session, by, trace.InterventionOutput{})
	if err != nil {
		return Intervened{}, err
	}
	decide, err := iv.due(ctx, intervention{ID: tr.ID, Time: tr.Time, Session: session, In: in}, s)
	return Intervened{Trace: tr, Decide: decide}, err
}

// The decide of the proposal appended and its question added to the questions due
// Only while curate.json names the cycle of the knob event in force so the decide draws under the knobs a pass would read
// A cycle that ended with no pass since has no knob event yet and its first pass prepares the question
func (iv *Intervener) due(ctx context.Context, p intervention, s state) (string, error) {
	ev, _, open := s.knobs.current(iv.now())
	if iv.s.Marks == nil || !open {
		return "", nil
	}
	held, err := iv.s.Marks.Hold()
	if err != nil || held == nil {
		return "", err
	}
	defer func() { _ = held.Release() }()
	m := held.Mark
	if m.CycleID != ev.CycleID {
		return "", nil
	}
	tr, due, err := ev.proposal(p, s.anchors.Version, s.all, iv.now())
	if err != nil {
		return "", err
	}
	if err := iv.traces.Append(ctx, tr.Record()); err != nil {
		return "", err
	}
	m.Due = append(m.Due, due)
	return tr.ID, held.Write(m)
}

// Takes the question of the decide off the questions due once its answer is recorded
// So a stop whose session has no question left due reads no record of curate
// Where curate.json is not at hand or another pass holds it the next pass leaves the question out
func (iv *Intervener) settled(decide string) error {
	if iv.s.Marks == nil {
		return nil
	}
	held, err := iv.s.Marks.Hold()
	if err != nil || held == nil {
		return err
	}
	defer func() { _ = held.Release() }()
	m := held.Mark
	open := slices.DeleteFunc(slices.Clone(m.Due), func(d Due) bool { return d.Decide == decide })
	if len(open) == len(m.Due) {
		return nil
	}
	m.Due = open
	return held.Write(m)
}

// The decide that asks the person about an agent's proposal and the question it leaves due
// It carries out nothing so nothing is audited and the draw records p 0
func (k KnobEvent) proposal(p intervention, anchor int, all knowledge.Set, now time.Time) (trace.Trace, Due, error) {
	id := trace.NewID(now)
	c := Change{Kind: ChangeIntervention, ID: p.In.ID, Version: p.In.Version, Compaction: p.In.Compaction, Anchor: anchor, Proposal: p.ID}
	d := Decision{Tier: topTier, RuleCode: RuleProposal, Action: ActionAsk}
	tr, err := Event{ID: id, Source: trace.SourcePush, Draw: k.AuditDraw(id, 0)}.Decide(c, d, nil, now)
	if err != nil {
		return trace.Trace{}, Due{}, err
	}
	due := Due{
		Kind: QuestionDirection, Decide: id, Change: ChangeIntervention, Ref: knowledge.Ref{ID: c.ID, Version: c.Version}, Rule: RuleProposal, At: now,
		Proposal: p.ID, Session: p.Session,
	}
	if v, err := all.Version(c.ID, c.Version); err == nil {
		due.Place = v.Run
	}
	return tr, due, nil
}

// Appends the trace of the intervention with what it wrote
func (iv *Intervener) record(
	ctx context.Context, id string, in trace.Intervention, session string, by Submitter, out trace.InterventionOutput,
) (trace.Trace, error) {
	tr, err := in.Trace(id, session, by.Source, out, iv.now())
	if err != nil {
		return trace.Trace{}, err
	}
	return tr, iv.traces.Append(ctx, tr.Record())
}

// The intervention of a proposal as its payload so a yes resolves it again against the records of that moment
func (p intervention) request(session string) Request {
	return Request{Action: p.In.Action, Target: p.In.Target, Choice: p.In.Choice, Labels: p.In.Labels, Value: p.In.Value, Session: session}
}

// The proposal of the id with its payload
func (s state) proposalOf(id string) (intervention, bool) {
	i := slices.IndexFunc(s.ints.proposals, func(p intervention) bool { return p.ID == id })
	if i < 0 {
		return intervention{}, false
	}
	return s.ints.proposals[i], true
}
