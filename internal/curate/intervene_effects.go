package curate

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// Carries out a person's intervention and records it under an id its records name
// 1. the ledger write comes first and names the intervention, or the answer an answer writes, in decision
// 2. the intervention follows since it names the records the write made
// 3. a knob raised above the knobs of the open cycle takes effect at once in a knob event of the cycle
// A failed export rides on the error after the intervention is recorded
func (iv *Intervener) carry(ctx context.Context, in trace.Intervention, session string, by Submitter, s state) (Intervened, error) {
	id := trace.NewID(iv.now())
	res, err := iv.effect(ctx, id, in, session, by, s)
	if err != nil && !errors.Is(err, knowledge.ErrExport) {
		return Intervened{}, err
	}
	tr, rerr := iv.record(ctx, id, in, session, by, trace.InterventionOutput{Records: res.Wrote.Written(), Answer: res.Answer})
	if rerr != nil {
		return Intervened{}, errors.Join(err, rerr)
	}
	res.Trace = tr
	return res, errors.Join(err, iv.raise(ctx, in, s))
}

// What the action of a person's intervention writes
// A dismiss, a pause, a resume, a knob and a reanchor write no record and take effect through what curate folds
func (iv *Intervener) effect(ctx context.Context, id string, in trace.Intervention, session string, by Submitter, s state) (Intervened, error) {
	switch in.Action {
	case trace.ActionJudge:
		wrote, err := iv.judge(ctx, iv.ledger.Under(id), in, s)
		return Intervened{Wrote: wrote}, err
	case trace.ActionAnswer:
		return iv.answer(ctx, in, session, by, s)
	case trace.ActionReverse:
		wrote, err := iv.reverse(ctx, iv.ledger.Under(id), in, s)
		return Intervened{Wrote: wrote}, err
	}
	return Intervened{}, nil
}

// The ledger write of a judgment under the person's name
// 1. keep approves a candidate or a compaction, reaffirms an approved version and restores a retired one as its next version
// 2. undo retires an approved version or drops a candidate
// 3. scope approves a candidate in the labels picked once a recorded run of its producer carries each of them
func (iv *Intervener) judge(ctx context.Context, ledger *knowledge.Ledger, in trace.Intervention, s state) (knowledge.Set, error) {
	if in.Compaction != "" {
		return iv.compaction(ctx, ledger, in.Compaction, in.Person)
	}
	k, err := s.all.Version(in.ID, in.Version)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrTargetInvalid, err)
	}
	var to knowledge.Knowledge
	switch {
	case in.Choice == trace.ChoiceScope && k.Status == knowledge.StatusCandidate:
		if err := iv.scoped(ctx, in, s); err != nil {
			return nil, err
		}
		to, err = ledger.ApproveIn(ctx, in.ID, in.Version, in.Labels, in.Person)
	case in.Choice == trace.ChoiceKeep && k.Status == knowledge.StatusCandidate:
		to, err = ledger.Approve(ctx, in.ID, in.Version, in.Person)
	case in.Choice == trace.ChoiceKeep && k.Status == knowledge.StatusApproved:
		to, err = ledger.Reaffirm(ctx, in.ID, in.Version, in.Person)
	case in.Choice == trace.ChoiceKeep && k.Status == knowledge.StatusRetired:
		to, err = ledger.Restore(ctx, in.ID, in.Version, in.Person)
	case in.Choice == trace.ChoiceUndo && (k.Status == knowledge.StatusApproved || k.Status == knowledge.StatusCandidate):
		to, err = ledger.Retire(ctx, in.ID, in.Version, in.Person)
	default:
		return nil, fmt.Errorf("%w: %s v%d is %s so %s does not apply", ErrTargetInvalid, in.ID, in.Version, k.Status, in.Choice)
	}
	return written(to, err)
}

// The record a write made with its error
// A refused write made none and a failed export after it made one
func written(to knowledge.Knowledge, err error) (knowledge.Set, error) {
	if err != nil && !errors.Is(err, knowledge.ErrExport) {
		return nil, err
	}
	return knowledge.Set{to}, err
}

// Every label of the scope a candidate takes in the labels is one a recorded run of its producer carries
func (iv *Intervener) scoped(ctx context.Context, in trace.Intervention, s state) error {
	scope, err := iv.ledger.ScopeIn(ctx, in.ID, in.Version, in.Labels)
	if err != nil {
		return err
	}
	return scope.Recorded(s.runs.Vocabulary(scope.Producer))
}

// The approval of a compaction with the coverage check compact keeps for it
func (iv *Intervener) compaction(ctx context.Context, ledger *knowledge.Ledger, id, person string) (knowledge.Set, error) {
	if iv.s.Coverage == nil {
		return nil, fmt.Errorf("%w: no coverage check of compaction %s is at hand here", ErrTargetInvalid, id)
	}
	cov, err := iv.s.Coverage(ctx, id)
	if err != nil {
		return nil, err
	}
	c, err := ledger.ApproveCompaction(ctx, id, person, cov)
	if err != nil && !errors.Is(err, knowledge.ErrExport) {
		return nil, err
	}
	return slices.Concat(c.Items, c.Replaced), err
}

// Carries out the choice of an open question under the person's name and records the answer
// The answer names the expose that showed the question when one did and the decide alone otherwise
func (iv *Intervener) answer(ctx context.Context, in trace.Intervention, session string, by Submitter, s state) (Intervened, error) {
	d := s.decides[in.Decide]
	id := trace.NewID(iv.now())
	choice := Choice(in.Choice)
	a := Answer{Choice: choice, Decide: d.ID, Hidden: true, Overturn: d.overturned(choice), Proof: in.Proof, Person: in.Person}
	if d.Change.ID != "" {
		a.Change = &knowledge.Ref{ID: d.Change.ID, Version: d.Change.Version}
	}
	res, err := iv.chosen(ctx, id, d, choice, session, by, s)
	if err != nil && !errors.Is(err, knowledge.ErrExport) {
		return Intervened{}, err
	}
	if choice == ChoiceMine {
		a.Refs = s.reanchored(d)
	}
	draw, derr := s.selfDraw()
	if derr != nil {
		return Intervened{}, derr
	}
	ev := Event{ID: id, Session: session, Source: by.Source, Draw: draw}
	if x, ok := s.exposeOf(in.Target, d.ID); ok {
		ev.Ref, a.DelayMS = x.ID, iv.now().Sub(x.Time).Milliseconds()
	}
	tr, terr := ev.Answer(a, AnswerOutput{Records: res.Wrote.Written(), Carried: res.Trace.ID}, iv.now())
	if terr != nil {
		return Intervened{}, terr
	}
	if terr := iv.traces.Append(ctx, tr.Record()); terr != nil {
		return Intervened{}, terr
	}
	return Intervened{Wrote: res.Wrote, Answer: id}, err
}

// What the choice writes under the answer
// 1. keep approves the change, drop retires it and merge approves the prepared compaction and then the change
// 2. undo of an audit retires what curate approved and restores what it dropped or withdrew as an approved next version
// 3. yes to a proposal carries out its payload as the person's own intervention resolved against the records of now
// 4. same, separate, mine, review and notyet write no record and a yes to a widening or the activation moves the knobs at the next cycle start
func (iv *Intervener) chosen(
	ctx context.Context, id string, d Decided, choice Choice, session string, by Submitter, s state,
) (Intervened, error) {
	ledger, c := iv.ledger.Under(id), d.Change
	var to knowledge.Knowledge
	var err error
	switch {
	case choice == ChoiceKeep:
		to, err = ledger.Approve(ctx, c.ID, c.Version, by.Person)
	case choice == ChoiceDrop:
		to, err = ledger.Retire(ctx, c.ID, c.Version, by.Person)
	case choice == ChoiceUndo && d.Decision.Action == ActionApply:
		to, err = ledger.Retire(ctx, c.ID, c.Version, by.Person)
	case choice == ChoiceUndo:
		to, err = ledger.Restore(ctx, c.ID, c.Version, by.Person)
	case choice == ChoiceMerge:
		wrote, err := iv.merged(ctx, ledger, d, by.Person, s)
		return Intervened{Wrote: wrote}, err
	case choice == ChoiceYes && c.Kind == ChangeIntervention:
		return iv.accepted(ctx, c.Proposal, session, by, s)
	default:
		return Intervened{}, nil
	}
	wrote, err := written(to, err)
	return Intervened{Wrote: wrote}, err
}

// The prepared compaction approved and then the change of the question
// A change that needs room names the pending compaction of its folder and a compaction names itself
func (iv *Intervener) merged(ctx context.Context, ledger *knowledge.Ledger, d Decided, person string, s state) (knowledge.Set, error) {
	id := d.Change.Compaction
	if d.Change.Kind != ChangeCompaction {
		room, err := s.all.Room(d.Change.ID, d.Change.Version)
		if err != nil {
			return nil, err
		}
		if room.Pending == "" {
			return nil, fmt.Errorf("%w: no compaction was prepared for %s v%d", ErrTargetInvalid, d.Change.ID, d.Change.Version)
		}
		id = room.Pending
	}
	merged, err := iv.compaction(ctx, ledger, id, person)
	if err != nil && !errors.Is(err, knowledge.ErrExport) || d.Change.Kind == ChangeCompaction {
		return merged, err
	}
	to, aerr := ledger.Approve(ctx, d.Change.ID, d.Change.Version, person)
	wrote, aerr := written(to, aerr)
	return slices.Concat(merged, wrote), errors.Join(err, aerr)
}

// The proposal of the agent carried out as the person's intervention
func (iv *Intervener) accepted(ctx context.Context, proposal, session string, by Submitter, s state) (Intervened, error) {
	p, ok := s.proposalOf(proposal)
	if !ok {
		return Intervened{}, fmt.Errorf("%w: the proposal %s is no longer recorded", ErrTargetInvalid, proposal)
	}
	in, err := iv.resolve(ctx, p.request(session), s)
	if err != nil {
		return Intervened{}, err
	}
	in.Proof, in.Person = by.Proof, by.Person
	return iv.carry(ctx, in, session, by, s)
}

// The versions nodloop approved that reach a run of the place of the run the decide cites
func (s state) reanchored(d Decided) []knowledge.Ref {
	i := slices.IndexFunc(s.runs, func(r trace.Trace) bool { return r.ID == d.Ref })
	if i < 0 {
		return nil
	}
	return s.all.ApprovedBy(knowledge.ApproverNodloop, time.Time{}, s.runs[i].Producer, s.runs[i].Labels).Refs()
}

// The expose the target names or else the newest expose of the decide
func (s state) exposeOf(target, decide string) (trace.Trace, bool) {
	if x, ok := s.exposes[target]; ok {
		return x, true
	}
	var newest trace.Trace
	for _, x := range s.exposes {
		if x.Ref == decide && x.Time.After(newest.Time) {
			newest = x
		}
	}
	return newest, newest.ID != ""
}

// The records a reverse writes to return every id the reversed intervention wrote to where it was
// The intervention, the answer it wrote and the intervention that answer carried out each name their records
func (iv *Intervener) reverse(ctx context.Context, ledger *knowledge.Ledger, in trace.Intervention, s state) (knowledge.Set, error) {
	x, _ := s.holds(in.Reverses)
	var wrote knowledge.Set
	for _, id := range s.stamps(x) {
		if !s.all.HasDecision(id) {
			continue
		}
		w, err := ledger.Reverse(ctx, id, in.Person)
		wrote = append(wrote, w...)
		if err != nil {
			return wrote, err
		}
	}
	return wrote, nil
}

// A knob intervention or a reverse that raises an audit p above the knobs of the open cycle takes effect at once
// since it only narrows, as a knob event of cause narrow in the same cycle
// Every other knob takes effect at the next cycle start
func (iv *Intervener) raise(ctx context.Context, in trace.Intervention, s state) error {
	n := KnobName(in.Knob)
	if in.Value == nil || n != KnobAuditTier0 && n != KnobAuditTier1 {
		return nil
	}
	ev, id, ok := s.knobs.current(iv.now())
	if !ok || *in.Value <= ev.Knobs.value(n) {
		return nil
	}
	ev.Knobs = ev.Knobs.with(n, *in.Value)
	ev.Cause, ev.Before, ev.Changes = KnobNarrow, id, []KnobChange{{Rule: KnobPerson, Knob: n, P: *in.Value}}
	tr, err := ev.Trace(iv.s.Build, iv.now())
	if err != nil {
		return err
	}
	return iv.traces.Append(ctx, tr.Record())
}
