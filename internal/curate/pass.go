package curate

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/classify"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/llm"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

type TraceStore interface {
	Get(ctx context.Context, id string) (record.Trace, error)
	List(ctx context.Context, f record.TraceFilter) ([]record.Trace, error)
	Append(ctx context.Context, tr record.Trace) error
}

// Newest first
type FeedbackStore interface {
	List(ctx context.Context, f record.FeedbackFilter) ([]record.Feedback, error)
}

// Sorts what the background prepared by whether the person's recorded judgment settles it
// It writes only through the ledger and the trace store and keeps nothing between passes
type Curator struct {
	ledger   *knowledge.Ledger
	traces   TraceStore
	verdicts FeedbackStore
	client   llm.Client
	// Members of another model family that join the claude variants of the judge
	endpoints []classify.Member
	// The HMAC under the key of the record directory of what the judge read
	seal classify.Seal
	now  func() time.Time
}

func New(
	ledger *knowledge.Ledger, traces TraceStore, verdicts FeedbackStore, client llm.Client, endpoints []classify.Member,
	seal classify.Seal, now func() time.Time,
) *Curator {
	return &Curator{ledger: ledger, traces: traces, verdicts: verdicts, client: client, endpoints: endpoints, seal: seal, now: now}
}

// What a pass reads besides the records
type Setting struct {
	// The build the policy version names
	Build string
	// What a judge configuration the pass starts runs with
	Judge JudgeSettings
	// The seed of a cycle the pass starts
	Seed string
	// The person's judgment on record with its newest version and when that version was recorded
	Anchor  Anchor
	Version AnchorVersion
	Since   time.Time
	// The start of the window whose runs make the places and whose extractions name signals
	Window time.Time
	Temp   TempDirs
	// The text of an anchor file read at check time
	Read func(path string) (string, error)
	// What the curation report of loop found that the knobs move on
	Numbers Numbers
}

// One decide as a pass made it or reads it back
type Decided struct {
	ID   string
	Time time.Time
	// The run the change cites
	Ref      string
	Change   Change
	Decision Decision
	// Whether the audit drew it
	Drawn bool
}

// The decide a trace holds and false for a trace that does not decode
func RestoreDecide(t trace.Trace) (Decided, bool) {
	d := Decided{ID: t.ID, Time: t.Time, Ref: t.Ref, Drawn: t.Draw != nil && t.Draw.Drawn}
	if t.Name != trace.NameDecide || json.Unmarshal(t.Input, &d.Change) != nil || json.Unmarshal(t.Output, &d.Decision) != nil {
		return Decided{}, false
	}
	return d, true
}

// What the decide is about: the version of a candidate, the signal of a withdrawal or the kind and bin an offer opens
// A newer decide of the same subject replaces an older one
func (d Decided) subject() string {
	switch {
	case d.Change.Kind == ChangeWithdraw:
		return "signal " + d.Change.Signal
	case d.Change.Kind == ChangeIntervention:
		return "proposal " + d.Change.Proposal
	case d.Change.Opens != nil:
		return fmt.Sprintf("widen %s %s", d.Change.Opens.Kind, d.Change.Opens.Bin)
	case d.Change.Kind.offered():
		return string(d.Change.Kind)
	}
	return fmt.Sprintf("version %s v%d", d.Change.ID, d.Change.Version)
}

// Decides newest first
type Decides []Decided

// The newest decide of each subject
func (ds Decides) latest() map[string]Decided {
	out := map[string]Decided{}
	for _, d := range ds {
		if _, ok := out[d.subject()]; !ok {
			out[d.subject()] = d
		}
	}
	return out
}

// What a pass decided and the questions it leaves due
type Result struct {
	// The knob event of the cycle the pass ran in
	Event KnobEvent
	// The decides the pass made in their order
	Decided []Decided
	Due     []Due
}

// The mark the pass leaves for the hooks with the start of the last daily pass
func (r Result) Mark(at time.Time) Mark {
	return Mark{At: at, CycleID: r.Event.CycleID, PolicyVersion: r.Event.PolicyVersion, Seed: r.Event.Seed, Knobs: r.Event.Knobs, Due: r.Due}
}

// The state of one pass
type pass struct {
	*Curator
	s     Setting
	event KnobEvent
	// The widenings and the activation the knobs offered at the start of the cycle
	offers []Offer
	// Every knowledge record read again after each write
	all knowledge.Set
	// Every decide newest first with those of this pass
	decides Decides
	// The decides of this pass in their order
	made []Decided
	// The decides an answer that holds settled
	answered map[string]bool
	// The interventions and the answers of the records as curate keeps their effect
	ints Interventions
	// The runs of the window by id and the anchor of their places
	runs   map[string]trace.Trace
	places []AnchorPlace
	// Built at the first change that reaches the judge
	judge *Judge
}

// One pass over what the background prepared
// 1. the first pass of a cycle appends its knob event and every decide draws under its seed
// 2. the knobs move on the numbers of loop before anything is decided so a narrowing holds for the whole pass
// 3. a decide a crash left without its ledger write is carried out first under the same decide
// 4. each widening and the activation the knobs offered is prepared as a question
// 5. each proposal of an agent that no decide asks about yet is prepared as a question
// 6. a conflict an extraction found with a version nodloop approved is decided once as a withdrawal
// 7. each waiting candidate version is decided once and again only after a retry, after corroborate joined new evidence or once the shadow ended
// 8. a failure on one change joins the error and the pass goes on with the next
func (c *Curator) Pass(ctx context.Context, s Setting) (Result, error) {
	p, err := c.open(ctx, s)
	if err != nil {
		return Result{}, err
	}
	failed := errors.Join(p.recover(ctx), p.offered(ctx), p.proposals(ctx), p.withdrawals(ctx), p.candidates(ctx))
	return Result{Event: p.event, Decided: p.made, Due: p.due()}, failed
}

// The pass over the records as they stand with the knob event of its cycle
func (c *Curator) open(ctx context.Context, s Setting) (*pass, error) {
	p := &pass{Curator: c, s: s, answered: map[string]bool{}, runs: map[string]trace.Trace{}}
	knobs, err := c.list(ctx, record.TraceFilter{Name: string(trace.NameKnob)})
	if err != nil {
		return nil, err
	}
	decides, err := c.list(ctx, record.TraceFilter{Name: string(trace.NameDecide)})
	if err != nil {
		return nil, err
	}
	answers, err := c.list(ctx, record.TraceFilter{Name: string(trace.NameAnswer)})
	if err != nil {
		return nil, err
	}
	interventions, err := c.list(ctx, record.TraceFilter{Name: string(trace.NameIntervention)})
	if err != nil {
		return nil, err
	}
	runs, err := c.list(ctx, record.TraceFilter{Name: string(trace.NameRun), Since: s.Window})
	if err != nil {
		return nil, err
	}
	for _, t := range decides {
		if d, ok := RestoreDecide(t); ok {
			p.decides = append(p.decides, d)
		}
	}
	p.ints = RestoreInterventions(slices.Concat(interventions, answers, decides))
	for _, t := range answers {
		var a Answer
		if json.Unmarshal(t.Input, &a) == nil && a.Decide != "" && !p.ints.Reversed(t.ID) {
			p.answered[a.Decide] = true
		}
	}
	for _, r := range runs {
		p.runs[r.ID] = r
	}
	p.places = s.Anchor.Places(runs)
	if err := p.reload(ctx); err != nil {
		return nil, err
	}
	return p, p.cycle(ctx, knobTraces(knobs))
}

func (c *Curator) list(ctx context.Context, f record.TraceFilter) (trace.Traces, error) {
	found, err := c.traces.List(ctx, f)
	return trace.RestoreAll(found), err
}

func (p *pass) reload(ctx context.Context) error {
	all, err := p.ledger.All(ctx)
	p.all = all
	return err
}

// The knob event of the cycle that holds now with the move of the knobs on the numbers of loop
// 1. a pass inside the cycle hands the numbers to Alarm and appends a narrowing of the same cycle only when a knob moved
// 2. the first pass of a cycle hands them to Start and appends the knob event of the cycle with the move and its offers
// 3. a cycle that starts a new judge configuration reads no number since every answer was given under another judge
func (p *pass) cycle(ctx context.Context, knobs knobTraces) error {
	now := p.now()
	if ev, id, ok := knobs.current(now); ok {
		move := ev.Knobs.Alarm(p.s.Numbers)
		if len(move.Changes) == 0 {
			p.event = ev
			return nil
		}
		ev.Cause, ev.Before = KnobNarrow, id
		return p.append(ctx, ev, move, now)
	}
	ev, started := knobs.next(now, p.s.Seed, p.s.Judge, p.s.Anchor.Shots(p.s.Judge.StartAt(now)))
	ev.Knobs = p.ints.Knobs(ev.Knobs, knobs.started())
	numbers := p.s.Numbers
	if started {
		numbers = Numbers{}
	}
	return p.append(ctx, ev, ev.Knobs.Start(numbers), now)
}

// Appends the knob event with the knobs and the changes of the move and runs the pass under it
// The offers wait for the pass to prepare them once the event is on record
func (p *pass) append(ctx context.Context, ev KnobEvent, m Move, now time.Time) error {
	ev.Knobs, ev.Changes = m.Knobs, m.Changes
	ev.PolicyVersion = ev.Policy(p.s.Build)
	tr, err := ev.Trace(p.s.Build, now)
	if err != nil {
		return err
	}
	if err := p.traces.Append(ctx, tr.Record()); err != nil {
		return err
	}
	p.event, p.offers = ev, m.Offers
	return nil
}

// Prepares each widening and the activation the knobs offered as a question of the person
// Only the person's yes moves a knob so the decide asks and carries out nothing
func (p *pass) offered(ctx context.Context) error {
	var errs []error
	for _, o := range p.offers {
		errs = append(errs, p.settle(ctx, "", ruling{change: o.Change(p.s.Version.Version), decision: Decision{Tier: topTier, Action: ActionAsk}}))
	}
	return errors.Join(errs...)
}

// Carries out the decides a crash left before their ledger write in the order they were made
// A version no longer in the state the decide found was decided since so it stays as it is
func (p *pass) recover(ctx context.Context) error {
	var errs []error
	for _, d := range slices.Backward(p.decides) {
		if d.Decision.carries() && !p.all.HasDecision(d.ID) && p.waits(d) {
			errs = append(errs, p.carry(ctx, d))
		}
	}
	return errors.Join(errs...)
}

// Whether what the decide is about is still in the state the decide found it in
// 1. a widening finds its kind and bin closed and the activation finds the shadow on
// 2. a proposal waits until the person answers or dismisses its question
// 3. a withdrawal finds a version nodloop approved and every other change a waiting candidate
func (p *pass) waits(d Decided) bool {
	switch {
	case d.Change.Opens != nil:
		return !p.event.Knobs.Opens(*d.Change.Opens)
	case d.Change.Kind == ChangeActivate:
		return !p.event.Knobs.Activated
	case d.Change.Kind == ChangeIntervention:
		return true
	}
	k, err := p.all.Version(d.Change.ID, d.Change.Version)
	if d.Change.Kind == ChangeWithdraw {
		return err == nil && k.Status == knowledge.StatusApproved && k.Approver == knowledge.ApproverNodloop
	}
	return err == nil && k.Status == knowledge.StatusCandidate
}

// The withdrawals of the conflicts the extractions of the window found with a version nodloop approved in the order they were found
// A conflict with a version a person approved needs nothing here since the extraction drafted its next version and that version asks the person
func (p *pass) withdrawals(ctx context.Context) error {
	extracts, err := p.list(ctx, record.TraceFilter{Name: string(trace.NameExtract), Since: p.s.Window})
	if err != nil {
		return err
	}
	latest := p.decides.latest()
	var errs []error
	for _, t := range slices.Backward(extracts) {
		ref, ok := conflicted(t)
		c := Change{Kind: ChangeWithdraw, ID: ref.ID, Version: ref.Version, Signal: t.ID, Anchor: p.s.Version.Version}
		d, decided := latest[(Decided{Change: c}).subject()]
		if !ok || decided && !p.unshadowed(d) || !p.waits(Decided{Change: c}) {
			continue
		}
		errs = append(errs, p.settle(ctx, t.Ref, ruling{change: c, decision: p.event.Knobs.withdrawal()}))
	}
	return errors.Join(errs...)
}

// The version a conflict of an extract trace names
// Read as a record of its own since curate never imports extract
func conflicted(t trace.Trace) (knowledge.Ref, bool) {
	var out struct {
		Conclusion string         `json:"conclusion"`
		Related    *knowledge.Ref `json:"related"`
	}
	if json.Unmarshal(t.Output, &out) != nil || out.Conclusion != "conflict" || out.Related == nil {
		return knowledge.Ref{}, false
	}
	return *out.Related, true
}

// Decides each waiting candidate version that is due a decision in the Waiting order
// A candidate that cites no run has no evidence a decide could name so it waits for the person as before
func (p *pass) candidates(ctx context.Context) error {
	latest := p.decides.latest()
	var errs []error
	for _, k := range p.all.AllWaiting() {
		retried, due := p.pending(k, latest)
		ref := cited(k.Evidence)
		if !due || ref == "" || k.Run == nil {
			continue
		}
		r, err := p.sort(ctx, k, ref, retried)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s v%d: %w", k.ID, k.Version, err))
			continue
		}
		errs = append(errs, p.settle(ctx, ref, r))
	}
	return errors.Join(errs...)
}

// The run a change of the candidate cites: the first that taught it and else the first an outcome confirmed
func cited(e knowledge.Evidence) string {
	if len(e.OutcomeTraceIDs) == 0 {
		return e.FirstFeedback()
	}
	return cmp.Or(e.FirstFeedback(), e.OutcomeTraceIDs[0])
}

// What a pass settles on one change
type ruling struct {
	change   Change
	decision Decision
	// The error of the judge call a retry carries
	failed error
}

// Whether the candidate version is due a decision and whether its last one was a retry
// 1. a version no decide named yet
// 2. a retry after a judge call that failed
// 3. a version corroborate joined new evidence to after its decide, or after the person dismissed its question
// 4. a version decided in shadow that no answer settled once the shadow ended
// A dismissed question stays closed without new evidence so nodloop never carries it out
func (p *pass) pending(k knowledge.Knowledge, latest map[string]Decided) (retried, due bool) {
	d, ok := latest[(Decided{Change: Change{ID: k.ID, Version: k.Version}}).subject()]
	dismissed, closed := p.ints.Dismissed(d.ID)
	switch {
	case !ok:
		return false, true
	case closed:
		return false, k.Time.After(dismissed)
	case d.Decision.Action == ActionRetry:
		return true, true
	case k.Time.After(d.Time):
		return false, true
	}
	return false, p.unshadowed(d)
}

// Whether the decide was made in shadow, no answer settled it and the shadow has ended so it is decided again
func (p *pass) unshadowed(d Decided) bool {
	_, dismissed := p.ints.Dismissed(d.ID)
	return d.Decision.Action == ActionShadow && p.event.Knobs.Activated && !p.answered[d.ID] && !dismissed
}

// Prepares each proposal of an agent no decide asks about yet as one question of the person in the order they were made
// The decide carries out nothing since only the person's answer carries out a proposal
func (p *pass) proposals(ctx context.Context) error {
	asked := map[string]bool{}
	for _, d := range p.decides {
		asked[d.Change.Proposal] = true
	}
	var errs []error
	for _, prop := range p.ints.proposed() {
		if asked[prop.ID] {
			continue
		}
		tr, _, err := p.event.proposal(prop, p.s.Version.Version, p.all, p.now())
		if err == nil {
			err = p.traces.Append(ctx, tr.Record())
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		d, _ := RestoreDecide(tr)
		p.made, p.decides = append(p.made, d), slices.Concat(Decides{d}, p.decides)
	}
	return errors.Join(errs...)
}

// The change a candidate version makes and the decision of its tier
// 1. tier 0 code rules carry out what the record already settles
// 2. tier 2 code rules send what only the person decides to a question before any judge call
// 3. the judge predicts the rest
func (p *pass) sort(ctx context.Context, k knowledge.Knowledge, ref string, retried bool) (ruling, error) {
	r := ruling{change: Change{Kind: p.kindOf(k), ID: k.ID, Version: k.Version, Extract: k.Extract, Anchor: p.s.Version.Version}}
	d, older, settled, err := p.settled(ctx, k)
	switch {
	case err != nil:
		return ruling{}, err
	case settled && older != nil:
		r.change.Kind, r.change.Old, r.decision = ChangeJoin, []knowledge.Ref{*older}, d
		return r, nil
	case settled:
		r.change.Kind, r.decision = ChangeDrop, d
		return r, nil
	}
	run := p.run(ctx, ref)
	place := p.place(run)
	rule, asks, err := p.asks(k, run, place)
	switch {
	case err != nil:
		return ruling{}, err
	case asks:
		r.decision = Decision{Tier: topTier, RuleCode: rule, Action: ActionAsk}
		return r, nil
	}
	return r, p.judged(ctx, &r, k, ref, run, place, retried)
}

// The judge on the change and the tier its outcome gives
// 1. a member that failed leaves the change for the next pass once with action retry and then it is decided as split
// 2. the outcome, the vote, the bin and the confidence it reads stay on the decide whatever the tier
func (p *pass) judged(ctx context.Context, r *ruling, k knowledge.Knowledge, ref string, run trace.Trace, place AnchorPlace, retried bool) error {
	cs, err := p.caseOf(ctx, k, r.change.Kind, run, place)
	if err != nil {
		return err
	}
	r.change.Diff = p.seal(cs.diff())
	judge, err := p.judging()
	if err != nil {
		return err
	}
	j, err := judge.Judge(ctx, ref, cs)
	r.failed = errors.Join(err, j.Err)
	if r.failed != nil && !retried {
		r.decision = Decision{Tier: 1, Judge: j.Trace, Action: ActionRetry, Failure: FailureRepairable, Reopen: ReopenNextPass}
		return nil
	}
	share := j.Share
	r.decision = Decision{Judge: j.Trace, Outcome: j.Outcome, Vote: &share, Bin: j.Bin, Confidence: j.Confidence}
	if r.failed != nil {
		r.decision.Outcome, r.failed = OutcomeSplit, nil
	}
	r.decision.Tier, r.decision.RuleCode, r.decision.Action = p.event.Knobs.tier(r.change.Kind, r.decision.Outcome, r.decision.Bin)
	return nil
}

// The judge of the configuration of the cycle with the members of another model family
func (p *pass) judging() (*Judge, error) {
	if p.judge != nil {
		return p.judge, nil
	}
	members, err := p.event.Judge.Members(p.client, p.s.Anchor.Shots(p.event.Judge.Start))
	if err != nil {
		return nil, err
	}
	p.judge = NewJudge(classify.NewPanel(classify.PointJudge, slices.Concat(members, p.endpoints), p.traces, p.seal, p.now))
	return p.judge, nil
}

// The run the change cites from the window or the store and the zero trace when neither holds it
// A run pruned or never recorded leaves the change with no place of its own
func (p *pass) run(ctx context.Context, id string) trace.Trace {
	if r, ok := p.runs[id]; ok {
		return r
	}
	found, err := p.traces.Get(ctx, id)
	if err != nil || found.Name != string(trace.NameRun) {
		return trace.Trace{}
	}
	return trace.Restore(found)
}

// The anchor of the place of the run
func (p *pass) place(run trace.Trace) AnchorPlace {
	if run.ID == "" {
		return AnchorPlace{}
	}
	scope := knowledge.RunScope{Producer: run.Producer, Labels: run.Labels.Place()}
	if i := slices.IndexFunc(p.places, func(a AnchorPlace) bool { return a.Place.String() == scope.String() }); i >= 0 {
		return p.places[i]
	}
	return p.s.Anchor.Places(trace.Traces{run})[0]
}

// Records the decide of the change and carries out its ledger write
// The decide lands first so a crash between the two leaves it for the next pass
func (p *pass) settle(ctx context.Context, ref string, r ruling) error {
	now := p.now()
	id := trace.NewID(now)
	ev := Event{ID: id, Ref: ref, Source: trace.SourcePush, Draw: p.event.AuditDraw(id, p.event.Knobs.audits(r.decision))}
	tr, err := ev.Decide(r.change, r.decision, r.failed, now)
	if err != nil {
		return err
	}
	if err := p.traces.Append(ctx, tr.Record()); err != nil {
		return err
	}
	made := Decided{ID: id, Time: now, Ref: ref, Change: r.change, Decision: r.decision, Drawn: ev.Draw.Drawn}
	p.made = append(p.made, made)
	p.decides = slices.Concat(Decides{made}, p.decides)
	if !r.decision.carries() {
		return nil
	}
	return p.carry(ctx, made)
}

// The ledger write the decide carries out under its id and the records read again after it
func (p *pass) carry(ctx context.Context, d Decided) error {
	c := d.Change
	var err error
	switch d.Decision.Action {
	case ActionApply:
		_, err = p.ledger.Apply(ctx, c.ID, c.Version, d.ID)
	case ActionWithdraw:
		_, err = p.ledger.Withdraw(ctx, c.ID, c.Version, d.Decision.Reason, d.ID)
	case ActionDrop:
		_, err = p.ledger.Drop(ctx, c.ID, c.Version, d.Decision.Reason, d.ID)
	case ActionJoin:
		err = p.join(ctx, d)
	}
	return errors.Join(err, p.reload(ctx))
}

// Joins the corrected runs of the candidate to the older candidate that says the same and then drops it
// A run that no longer carries a correction or that the older candidate does not reach joins nothing
func (p *pass) join(ctx context.Context, d Decided) error {
	newer, err := p.all.Version(d.Change.ID, d.Change.Version)
	if err != nil {
		return err
	}
	if len(d.Change.Old) != 1 {
		return fmt.Errorf("%w: join %s names %d older candidates", ErrDecideInvalid, d.ID, len(d.Change.Old))
	}
	older := d.Change.Old[0]
	for _, id := range newer.Evidence.FeedbackTraceIDs {
		found, err := p.traces.Get(ctx, id)
		if errors.Is(err, record.ErrTraceNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		verdicts, err := p.verdictsOf(ctx, id)
		if err != nil {
			return err
		}
		_, err = p.ledger.Corroborate(ctx, older.ID, older.Version, trace.Restore(found), verdicts)
		if err != nil && !errors.Is(err, knowledge.ErrNotCorrected) && !errors.Is(err, knowledge.ErrScopeWidened) {
			return err
		}
	}
	_, err = p.ledger.Drop(ctx, d.Change.ID, d.Change.Version, d.Decision.Reason, d.ID)
	return err
}

// The reconciled verdict of the run
func (p *pass) verdictsOf(ctx context.Context, run string) (feedback.Records, error) {
	list, err := p.verdicts.List(ctx, record.FeedbackFilter{TraceID: run})
	return feedback.RestoreAll(list).Reconciled(), err
}

// The questions the decides leave for the person in the order they were decided
// 1. the newest decide of each subject stands and an answer to it or a dismiss of it settles it
// 2. a drawn change that was carried out is due as an audit question
// 3. a question of a change asks while the version is in the state the decide found
// 4. a question of a proposal names it and the session that made it so the stop of that turn asks it
func (p *pass) due() []Due {
	latest := p.decides.latest()
	out := []Due{}
	for _, key := range slices.Sorted(maps.Keys(latest)) {
		d := latest[key]
		kind, ok := p.question(d)
		_, dismissed := p.ints.Dismissed(d.ID)
		if !ok || p.answered[d.ID] || dismissed {
			continue
		}
		due := Due{
			Kind: kind, Decide: d.ID, Change: d.Change.Kind, Ref: knowledge.Ref{ID: d.Change.ID, Version: d.Change.Version},
			Rule: d.Decision.RuleCode, Run: d.Ref, At: d.Time, Proposal: d.Change.Proposal, Session: p.ints.session(d.Change.Proposal),
		}
		if k, err := p.all.Version(d.Change.ID, d.Change.Version); err == nil {
			due.Place = k.Run
		}
		out = append(out, due)
	}
	slices.SortStableFunc(out, func(a, b Due) int { return a.At.Compare(b.At) })
	return out
}

// The kind of question a decide leaves and false for one that leaves none
// A question of a change that is not carried out asks only while what it is about waits
func (p *pass) question(d Decided) (QuestionKind, bool) {
	kind, ok := d.asks()
	if !ok || kind != QuestionAudit && !p.waits(d) {
		return "", false
	}
	return kind, true
}
