package curate

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// Carries out a person's intervention and records it under an id its records name
// 1. the ledger write comes first and names in decision the intervention or the answer an answer writes
// 2. the intervention follows since it names the records the write made
// 3. a judgment whose earlier try wrote the ledger and failed to record its intervention takes that write so both stand once
// A reverse or a judgment of a compaction goes on under the decision of its earlier try and writes only what that try left
// 4. a knob raised above the knobs of the open cycle takes effect at once in a knob event of the cycle
// A failed export rides on the error after the intervention is recorded
func (iv *Intervener) carry(ctx context.Context, in trace.Intervention, session string, by Submitter, s state) (Intervened, error) {
	id := trace.NewID(iv.now())
	var res Intervened
	var err error
	if earlier, wrote, ok := s.earlier(in.ID, judgmentStatus(in), by.Person); ok {
		id, res = earlier, Intervened{Wrote: wrote}
	} else {
		id = cmp.Or(s.untraced(s.touched(in), by.Person), id)
		res, err = iv.effect(ctx, id, in, session, by, s)
	}
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
		wrote, err := iv.judge(ctx, id, in, s)
		return Intervened{Wrote: wrote}, err
	case trace.ActionAnswer:
		return iv.answer(ctx, in, session, by, s)
	case trace.ActionReverse:
		wrote, err := iv.reverse(ctx, id, in, s)
		return Intervened{Wrote: wrote}, err
	}
	return Intervened{}, nil
}

// The ids a reverse or a judgment of a compaction writes several records on
func (s state) touched(in trace.Intervention) []string {
	switch in.Action {
	case trace.ActionReverse:
		x, _ := s.holds(in.Reverses)
		return s.idsUnder(s.stamps(x)...)
	case trace.ActionJudge:
		return s.compacted(in.Compaction)
	}
	return nil
}

// The ledger write of a judgment under the person's name
// 1. keep approves a candidate or a compaction, reaffirms an approved version and restores a retired one as its next version
// 2. undo retires an approved version or drops a candidate
// 3. scope approves a candidate in the labels picked once a recorded run of its producer carries each of them
func (iv *Intervener) judge(ctx context.Context, decision string, in trace.Intervention, s state) (knowledge.Set, error) {
	if in.Compaction != "" {
		return iv.compaction(ctx, decision, in.Compaction, in.Person)
	}
	// resolve found the version in the same records
	k, _ := s.all.Version(in.ID, in.Version)
	ledger := iv.ledger.Under(decision)
	var to knowledge.Knowledge
	var err error
	switch {
	case in.Choice == trace.ChoiceScope && k.Status == knowledge.StatusCandidate:
		if err := iv.scoped(ctx, in); err != nil {
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
func (iv *Intervener) scoped(ctx context.Context, in trace.Intervention) error {
	scope, err := iv.ledger.ScopeIn(ctx, in.ID, in.Version, in.Labels)
	if err != nil {
		return err
	}
	runs, err := iv.traces.List(ctx, record.TraceFilter{Name: string(trace.NameRun)})
	if err != nil {
		return err
	}
	return scope.Recorded(trace.RestoreAll(runs).Vocabulary(scope.Producer))
}

// The approval of a compaction with the coverage check compact keeps for it under the decision
// A compaction approved already gets only the records still missing so the decision names only what it wrote
func (iv *Intervener) compaction(ctx context.Context, decision, id, person string) (knowledge.Set, error) {
	if iv.s.Coverage == nil {
		return nil, fmt.Errorf("%w: no coverage check of compaction %s is at hand here", ErrTargetInvalid, id)
	}
	cov, err := iv.s.Coverage(ctx, id)
	if err != nil {
		return nil, err
	}
	c, err := iv.ledger.Under(decision).ApproveCompaction(ctx, id, person, cov)
	if err != nil && !errors.Is(err, knowledge.ErrExport) {
		return nil, err
	}
	return slices.DeleteFunc(slices.Concat(c.Items, c.Replaced), func(k knowledge.Knowledge) bool { return k.Decision != decision }), err
}

// Carries out the choice of an open question under the person's name and records the answer
// The answer names the expose that showed the question when one did and the decide alone otherwise
func (iv *Intervener) answer(ctx context.Context, in trace.Intervention, session string, by Submitter, s state) (Intervened, error) {
	draw, err := s.selfDraw()
	if err != nil {
		return Intervened{}, err
	}
	x, shown := s.exposeOf(in.Target, in.Decide)
	return iv.answered(ctx, s.decides[in.Decide], Choice(in.Choice), Event{Session: session, Source: by.Source, Draw: draw}, x, shown, by, s)
}

// Carries out the choice of the question of the decide and appends its answer under the event
// 1. the answer is checked before the ledger write so a refused answer writes nothing
// 2. the ledger write comes next and names the answer in decision
// 3. a choice whose earlier try wrote the ledger and failed to append its answer takes that write so both stand once
// A merge or an undo of a compaction goes on under the decision of its earlier try and writes only what that try left
// 4. the answer names the expose that showed the question when one did with the delay since it
// 5. the question leaves the questions due once its answer is recorded
func (iv *Intervener) answered(
	ctx context.Context, d Decided, choice Choice, ev Event, x trace.Trace, shown bool, by Submitter, s state,
) (Intervened, error) {
	ev.ID = trace.NewID(iv.now())
	a := Answer{Choice: choice, Decide: d.ID, Hidden: true, Overturn: d.overturned(choice), Proof: by.Proof, Person: by.Person}
	if d.Change.ID != "" {
		a.Change = &knowledge.Ref{ID: d.Change.ID, Version: d.Change.Version}
	}
	if shown {
		ev.Ref, a.DelayMS = x.ID, iv.now().Sub(x.Time).Milliseconds()
	}
	if _, err := ev.Answer(a, AnswerOutput{}, iv.now()); err != nil {
		return Intervened{}, err
	}
	if choice == ChoiceMine {
		refs, err := iv.reanchored(ctx, d, s)
		if err != nil {
			return Intervened{}, err
		}
		a.Refs = refs
	}
	var res Intervened
	var err error
	if earlier, wrote, ok := s.earlier(d.Change.ID, d.leaves(choice), by.Person); ok {
		ev.ID, res = earlier, Intervened{Wrote: wrote}
	} else {
		ev.ID = cmp.Or(s.untraced(s.writes(d, choice), by.Person), ev.ID)
		res, err = iv.chosen(ctx, ev.ID, d, choice, ev.Session, by, s)
	}
	if err != nil && !errors.Is(err, knowledge.ErrExport) {
		return Intervened{}, err
	}
	// The same answer passed its check above and its output holds only the records and the intervention written
	tr, _ := ev.Answer(a, AnswerOutput{Records: res.Wrote.Written(), Carried: res.Trace.ID}, iv.now())
	if terr := iv.traces.Append(ctx, tr.Record()); terr != nil {
		return Intervened{}, terr
	}
	return Intervened{Wrote: res.Wrote, Answer: ev.ID}, errors.Join(err, iv.settled(d.ID))
}

// The ids a merge or an undo of a compaction writes several records on
// 1. a merge: the items of its compaction and the change with the items of the folder it needs room in
// The folder holds the items the compaction approved once it is approved
// 2. an undo of a compaction curate applied: the ids the decide wrote
func (s state) writes(d Decided, choice Choice) []string {
	switch {
	case choice == ChoiceMerge:
		ids := append(s.compacted(d.Change.Compaction), d.Change.ID)
		// A compaction names no version so it has no folder
		f, _, _ := s.all.Fits(d.Change.ID, d.Change.Version)
		for _, k := range f.Carried {
			ids = append(ids, k.ID)
		}
		return ids
	case choice == ChoiceUndo && d.Change.Kind == ChangeCompaction:
		return s.idsUnder(d.ID)
	}
	return nil
}

// What the choice writes under the answer
// 1. keep approves the change, drop retires it and merge approves the prepared compaction and then the change
// 2. undo of an audit retires what curate approved and restores what it dropped or withdrew as an approved next version
// A compaction curate applied is reversed whole so the items it merged come back as runs had them
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
	case choice == ChoiceUndo && c.Kind == ChangeCompaction && s.takenBack(d.ID, id):
		return Intervened{Wrote: s.under(id)}, nil
	case choice == ChoiceUndo && c.Kind == ChangeCompaction:
		wrote, err := ledger.Reverse(ctx, d.ID, by.Person)
		return Intervened{Wrote: wrote}, err
	case choice == ChoiceUndo && d.Decision.Action == ActionApply:
		to, err = ledger.Retire(ctx, c.ID, c.Version, by.Person)
	case choice == ChoiceUndo:
		to, err = ledger.Restore(ctx, c.ID, c.Version, by.Person)
	case choice == ChoiceMerge:
		wrote, err := iv.merged(ctx, id, d, by.Person, s)
		return Intervened{Wrote: wrote}, err
	case choice == ChoiceYes && c.Kind == ChangeIntervention:
		return iv.accepted(ctx, d.ID, c.Proposal, session, by, s)
	default:
		return Intervened{}, nil
	}
	wrote, err := written(to, err)
	return Intervened{Wrote: wrote}, err
}

// The status the ledger write of a choice leaves on the id of the change and none for a choice that writes no single record
func (d Decided) leaves(choice Choice) knowledge.Status {
	switch {
	case choice == ChoiceKeep, choice == ChoiceUndo && d.Decision.Action != ActionApply:
		return knowledge.StatusApproved
	case choice == ChoiceDrop, choice == ChoiceUndo:
		return knowledge.StatusRetired
	}
	return ""
}

// The status the ledger write of a judgment leaves on its id
// None for a judgment of a compaction, a choice no judgment takes or another action
func judgmentStatus(in trace.Intervention) knowledge.Status {
	switch {
	case in.Action != trace.ActionJudge || in.Compaction != "" || !in.Choice.Judges():
		return ""
	case in.Choice == trace.ChoiceUndo:
		return knowledge.StatusRetired
	}
	return knowledge.StatusApproved
}

// The decision and the record of an earlier try of the person's write on the id that the ledger holds and no event names
// 1. a try whose event failed to append after its ledger write leaves its record under a decision no trace holds
// 2. the record has the status the write leaves and the person as approver and is still the newest of its version
// So a retry takes the decision and the record and appends its event alone and both stand written once
func (s state) earlier(id string, status knowledge.Status, person string) (string, knowledge.Set, bool) {
	if id == "" || status == "" {
		return "", nil, false
	}
	i := slices.IndexFunc(s.all, func(k knowledge.Knowledge) bool {
		return k.ID == id && k.Status == status && k.Approver == person && k.Decision != "" && !s.traced(k.Decision)
	})
	if i < 0 {
		return "", nil, false
	}
	k := s.all[i]
	if newest, _ := s.all.Version(k.ID, k.Version); newest.Decision != k.Decision || !newest.Time.Equal(k.Time) {
		return "", nil, false
	}
	return k.Decision, knowledge.Set{k}, true
}

// The decision an earlier try of the person's write left on the ids with no event that names it and empty for none
// 1. a try whose event failed to append leaves the newest record of an id it wrote under a decision no trace holds
// 2. a record of that decision names the person as approver
// A candidate a reverse restates names no approver so the decision is read over all its records
// So a retry of a write of several records goes on under that decision and the write and its event each stand once
func (s state) untraced(ids []string, person string) string {
	for _, id := range ids {
		k, ok := s.all.Find(id)
		if !ok || k.Decision == "" || s.traced(k.Decision) {
			continue
		}
		if slices.ContainsFunc(s.all, func(r knowledge.Knowledge) bool { return r.Decision == k.Decision && r.Approver == person }) {
			return k.Decision
		}
	}
	return ""
}

// The ids of the records the decisions wrote
func (s state) idsUnder(decisions ...string) []string {
	var out []string
	for _, k := range s.all {
		if k.Decision != "" && slices.Contains(decisions, k.Decision) && !slices.Contains(out, k.ID) {
			out = append(out, k.ID)
		}
	}
	return out
}

// The ids of the items of the compaction and none for no compaction
func (s state) compacted(compaction string) []string {
	var out []string
	for _, k := range s.all {
		if compaction != "" && k.Compaction == compaction && !slices.Contains(out, k.ID) {
			out = append(out, k.ID)
		}
	}
	return out
}

// The records of the decision in the order they were appended
func (s state) under(decision string) knowledge.Set {
	var out knowledge.Set
	for _, k := range slices.Backward(s.all) {
		if k.Decision == decision {
			out = append(out, k)
		}
	}
	return out
}

// Whether a reverse under the decision took back what the stamp wrote
// One ledger write takes back every record of a stamp so one record of the decision on an id the stamp wrote shows it
func (s state) takenBack(stamp, decision string) bool {
	ids := s.idsUnder(stamp)
	return slices.ContainsFunc(s.all, func(k knowledge.Knowledge) bool { return k.Decision == decision && slices.Contains(ids, k.ID) })
}

// Whether a decide, an answer or a person's intervention of the records has the id
func (s state) traced(id string) bool {
	_, decided := s.decides[id]
	return decided || slices.ContainsFunc(s.ints.answers, func(a answered) bool { return a.ID == id }) ||
		slices.ContainsFunc(s.ints.person, func(in intervention) bool { return in.ID == id })
}

// The prepared compaction approved and then the change of the question
// 1. a compaction approved already under the decision gets only the records still missing
// 2. a change an earlier try approved under the decision is taken as it is
func (iv *Intervener) merged(ctx context.Context, decision string, d Decided, person string, s state) (knowledge.Set, error) {
	id, err := s.mergeOf(d, decision)
	if err != nil {
		return nil, err
	}
	merged, err := iv.compaction(ctx, decision, id, person)
	if err != nil && !errors.Is(err, knowledge.ErrExport) || d.Change.Kind == ChangeCompaction {
		return merged, err
	}
	if k, ok := s.all.Find(d.Change.ID); ok && k.Decision == decision {
		return slices.Concat(merged, knowledge.Set{k}), err
	}
	to, aerr := iv.ledger.Under(decision).Approve(ctx, d.Change.ID, d.Change.Version, person)
	wrote, aerr := written(to, aerr)
	return slices.Concat(merged, wrote), errors.Join(err, aerr)
}

// The compaction a merge approves
// 1. a compaction names itself and an earlier try under the decision names the one it approved
// Its approval made the room so the folder names no pending compaction any more
// 2. a change that needs room names the pending compaction of its folder
func (s state) mergeOf(d Decided, decision string) (string, error) {
	if d.Change.Kind == ChangeCompaction {
		return d.Change.Compaction, nil
	}
	if i := slices.IndexFunc(s.all, func(k knowledge.Knowledge) bool { return k.Decision == decision && k.Compaction != "" }); i >= 0 {
		return s.all[i].Compaction, nil
	}
	room, err := s.all.Room(d.Change.ID, d.Change.Version)
	if err != nil {
		return "", err
	}
	if room.Pending == "" {
		return "", fmt.Errorf("%w: no compaction was prepared for %s v%d", ErrTargetInvalid, d.Change.ID, d.Change.Version)
	}
	return room.Pending, nil
}

// The proposal of the agent carried out as the person's intervention that names the decide of the question in decide
// An earlier try that carried it out and failed to append its answer left that intervention so the answer names it
func (iv *Intervener) accepted(ctx context.Context, decide, proposal, session string, by Submitter, s state) (Intervened, error) {
	p, ok := s.proposalOf(proposal)
	if !ok {
		return Intervened{}, fmt.Errorf("%w: the proposal %s is no longer recorded", ErrTargetInvalid, proposal)
	}
	if y, ok := s.carriedBefore(decide, p, by.Person); ok {
		found, err := iv.traces.Get(ctx, y.ID)
		return Intervened{Trace: trace.Restore(found), Wrote: s.under(y.ID)}, err
	}
	in, err := iv.resolve(ctx, p.request(session), s)
	if err != nil {
		return Intervened{}, err
	}
	// An answer or a dismiss keeps the decide of the question it closes
	in.Proof, in.Person, in.Decide = by.Proof, by.Person, cmp.Or(in.Decide, decide)
	return iv.carry(ctx, in, session, by, s)
}

// The person's intervention a yes to the question of the decide carried out that no answer names in carried
// It carries the payload of the proposal and still holds
func (s state) carriedBefore(decide string, p intervention, person string) (intervention, bool) {
	for _, y := range s.ints.person {
		named := slices.ContainsFunc(s.ints.answers, func(a answered) bool { return a.Out.Carried == y.ID })
		if y.In.Decide == decide && y.In.Person == person && !named && !s.ints.Reversed(y.ID) && reflect.DeepEqual(y.request(""), p.request("")) {
			return y, true
		}
	}
	return intervention{}, false
}

// The versions nodloop approved that reach a run of the place of the run the decide cites
// A run the records no longer hold names no place so a mine takes nothing
func (iv *Intervener) reanchored(ctx context.Context, d Decided, s state) ([]knowledge.Ref, error) {
	found, err := iv.traces.List(ctx, record.TraceFilter{ID: d.Ref, Name: string(trace.NameRun)})
	if err != nil {
		return nil, err
	}
	runs := trace.RestoreAll(found)
	i := slices.IndexFunc(runs, func(r trace.Trace) bool { return r.ID == d.Ref })
	if i < 0 {
		return nil, nil
	}
	return s.all.ApprovedBy(knowledge.ApproverNodloop, time.Time{}, runs[i].Producer, runs[i].Labels).Refs(), nil
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

// The records a reverse writes under the decision to return every id the reversed intervention wrote to where it was
// 1. the intervention, the answer it wrote and the intervention that answer carried out each name their records
// 2. what an earlier try under the decision took back is not taken back again and its records count as written
func (iv *Intervener) reverse(ctx context.Context, decision string, in trace.Intervention, s state) (knowledge.Set, error) {
	x, _ := s.holds(in.Reverses)
	ledger := iv.ledger.Under(decision)
	wrote := s.under(decision)
	for _, id := range s.stamps(x) {
		if !s.all.HasDecision(id) || s.takenBack(id, decision) {
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
// as a knob event of cause narrow in the same cycle since it only narrows
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
