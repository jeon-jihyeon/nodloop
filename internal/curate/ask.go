package curate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// What the stop of one session asks with
type Asking struct {
	Session string
	// The run the stop recorded and empty on a host that records none at its stop
	Run string
	// The place of the stop
	Place knowledge.RunScope
	// Whether the mode infers so the questions curate prepared are pushed beside the proposals the session made
	Pushed bool
	Mark   Mark
	// The ledger as the stop read it so curate never reads it a second time
	All knowledge.Set
	// The HMAC of a text under the key of the record directory
	Seal func(string) string
	// The person's own words on the runs
	Words func(ctx context.Context, runs []string) ([]string, error)
}

// What the stop shows with the expose that records it
// The zero Shown shows nothing
type Shown struct {
	Question Question
	// The one line of a notice
	Notice string
	Expose trace.Trace
}

// An expose with what it showed
type shownExpose struct {
	trace.Trace
	Exposure
}

// Whether the expose showed a question the person answers
func (x shownExpose) asks() bool {
	return x.Kind != QuestionNotice
}

// The exposes of the records in their order with what each showed
type shownExposes []shownExpose

// The exposes of the traces that decode under a draw
func shownOf(ts trace.Traces) shownExposes {
	var out shownExposes
	for _, t := range ts {
		var x Exposure
		if json.Unmarshal(t.Input, &x) == nil && t.Draw != nil {
			out = append(out, shownExpose{Trace: t, Exposure: x})
		}
	}
	return out
}

// The purposes of the draws of a question curate pushes beside the audit and the shadow pick
const (
	drawDirection = "direction"
	drawNotice    = "notice"
)

// The question the stop of the session asks with its expose appended before the stop prints it
// 1. a proposal the session made that no expose showed comes first whatever was asked before since the person asked for it
// 2. else in a mode that infers one question per session in the order of question kinds
// 3. a question two other sessions showed with no answer is recorded as no_answer and left as it was
func (iv *Intervener) Ask(ctx context.Context, a Asking) (Shown, error) {
	if len(a.Mark.Due) == 0 || a.Session == "" {
		return Shown{}, nil
	}
	s, err := iv.readOver(ctx, a.All)
	if err != nil {
		return Shown{}, err
	}
	a.Mark = a.Mark.inForce(s.knobs, iv.now())
	open := s.open(a.Mark.Due)
	if due, ok := s.proposalDue(open, a.Session); ok {
		return iv.show(ctx, a, s, due, trace.Draw{CycleID: a.Mark.CycleID, PolicyVersion: a.Mark.PolicyVersion}, trace.SourceManual)
	}
	if !a.Pushed || s.spent(a.Session) {
		return Shown{}, nil
	}
	pushed, err := iv.expire(ctx, s, s.reaching(open, a.Place), a.Session)
	if err != nil {
		return Shown{}, err
	}
	due, draw, ok, err := iv.pick(ctx, a, s, pushed)
	if err != nil || !ok {
		return Shown{}, err
	}
	return iv.show(ctx, a, s, due, draw, trace.SourcePush)
}

// The mark with the policy version of the knob event in force in its cycle
// A person's raise appends a knob event inside the cycle at once and the mark keeps the version before it until the next pass
func (m Mark) inForce(knobs knobTraces, now time.Time) Mark {
	if ev, _, ok := knobs.current(now); ok && ev.CycleID == m.CycleID {
		m.PolicyVersion = ev.PolicyVersion
	}
	return m
}

// The due questions in their order that no answer that holds settled and no dismiss closed
func (s state) open(due []Due) []Due {
	return slices.DeleteFunc(slices.Clone(due), func(d Due) bool {
		_, decided := s.decides[d.Decide]
		_, dismissed := s.ints.Dismissed(d.Decide)
		return !decided || dismissed || s.answered(d.Decide)
	})
}

// The question of a proposal the session made that no expose showed yet
func (s state) proposalDue(open []Due, session string) (Due, bool) {
	i := slices.IndexFunc(open, func(d Due) bool { return d.Proposal != "" && d.Session == session && len(s.exposed(d.Decide)) == 0 })
	if i < 0 {
		return Due{}, false
	}
	return open[i], true
}

// Whether the session showed a question curate pushed so its one question is spent
func (s state) spent(session string) bool {
	return slices.ContainsFunc(s.shown, func(x shownExpose) bool {
		return x.SessionID == session && x.asks() && x.Source == trace.SourcePush
	})
}

// The questions curate pushes that reach the place
// A proposal is no question curate pushes
func (s state) reaching(open []Due, place knowledge.RunScope) []Due {
	return slices.DeleteFunc(slices.Clone(open), func(d Due) bool {
		return d.Proposal != "" || d.Place != nil && !d.Place.Admits(place.Producer, place.Labels)
	})
}

// The exposes of the question of the decide newest first
func (s state) exposed(decide string) []shownExpose {
	var out []shownExpose
	for _, x := range s.shown {
		if x.Ref == decide && x.asks() {
			out = append(out, x)
		}
	}
	return out
}

// The questions left to ask after each one two other sessions showed with no answer is recorded as no_answer
func (iv *Intervener) expire(ctx context.Context, s state, open []Due, session string) ([]Due, error) {
	var out []Due
	var errs []error
	for _, d := range open {
		shown := slices.DeleteFunc(s.exposed(d.Decide), func(x shownExpose) bool { return x.SessionID == session })
		if len(shown) < 2 {
			out = append(out, d)
			continue
		}
		errs = append(errs, iv.unanswered(ctx, s.decides[d.Decide], shown[0]))
	}
	return out, errors.Join(errs...)
}

// The no_answer of the question the expose showed last as code records it under the draw of that expose with no proof
func (iv *Intervener) unanswered(ctx context.Context, d Decided, x shownExpose) error {
	a := Answer{Choice: ChoiceNoAnswer, Decide: d.ID, Change: x.Change, Hidden: true, Proof: trace.ProofNone}
	tr, err := Event{Session: x.SessionID, Ref: x.ID, Source: x.Source, Draw: *x.Draw}.Answer(a, AnswerOutput{}, iv.now())
	if err != nil {
		return err
	}
	return iv.traces.Append(ctx, tr.Record())
}

// The question curate pushes at the stop and its draw in the order of question kinds
// Session relevance orders the direction questions alone so it never moves the probability of an audit or a shadow pick
func (iv *Intervener) pick(ctx context.Context, a Asking, s state, open []Due) (Due, trace.Draw, bool, error) {
	var direction, audit []Due
	for _, d := range open {
		switch d.Kind {
		case QuestionDirection:
			direction = append(direction, d)
		case QuestionAudit:
			audit = append(audit, d)
		}
	}
	if len(direction) > 0 {
		d, err := iv.ownFirst(ctx, a.Session, direction)
		return d, trace.NewDraw(a.Mark.CycleID, a.Mark.PolicyVersion, a.Mark.Seed, d.Decide, drawDirection, 1), true, err
	}
	for _, d := range audit {
		if draw, ok := s.draws[d.Decide]; ok {
			return d, draw, true, nil
		}
	}
	d, draw, ok := a.Mark.ShadowPick(a.Session, open)
	return d, draw, ok, nil
}

// The first direction question whose change cites a run of the session and else the oldest
func (iv *Intervener) ownFirst(ctx context.Context, session string, direction []Due) (Due, error) {
	if len(direction) == 1 {
		return direction[0], nil
	}
	found, err := iv.traces.List(ctx, record.TraceFilter{Name: string(trace.NameRun), SessionID: session})
	if err != nil {
		return Due{}, err
	}
	own := map[string]bool{}
	for _, r := range found {
		own[r.ID] = true
	}
	if i := slices.IndexFunc(direction, func(d Due) bool { return own[d.Run] }); i >= 0 {
		return direction[i], nil
	}
	return direction[0], nil
}

// Appends the expose of the question of the due entry under the draw and returns what the stop shows
// 1. its key is unique among the questions no answer closed
// 2. its round counts the sessions that showed it before
func (iv *Intervener) show(ctx context.Context, a Asking, s state, due Due, draw trace.Draw, source trace.Source) (Shown, error) {
	d := s.decides[due.Decide]
	q, err := iv.question(ctx, a, s, due, d)
	if err != nil {
		return Shown{}, err
	}
	q.Key = questionKey(d.ID, s.keys(d.ID))
	text := q.Text()
	x := Exposure{
		Kind: due.Kind, Key: q.Key, Surface: SurfaceContext, Rules: QuestionRules, Choices: q.Choices[:], Text: a.Seal(text),
		Lines: q.lines().sealed(a.Seal), Round: len(s.exposed(d.ID)) + 1, Run: a.Run, Runes: utf8.RuneCountInString(text),
	}
	if d.Change.ID != "" {
		x.Change = &knowledge.Ref{ID: d.Change.ID, Version: d.Change.Version}
	}
	tr, err := Event{Session: a.Session, Ref: d.ID, Source: source, Draw: draw}.Expose(x, iv.now())
	if err != nil {
		return Shown{}, err
	}
	if err := iv.traces.Append(ctx, tr.Record()); err != nil {
		return Shown{}, err
	}
	return Shown{Question: q, Expose: tr}, nil
}

// The keys of the other questions no answer closed
// A question asked again may take its own key back
func (s state) keys(decide string) []string {
	var out []string
	for _, x := range s.shown {
		if x.asks() && x.Ref != decide && !s.answered(x.Ref) {
			out = append(out, x.Key)
		}
	}
	return out
}

// The question of the decide in plain words with the person's own words on the runs its change cites
func (iv *Intervener) question(ctx context.Context, a Asking, s state, due Due, d Decided) (Question, error) {
	q := Question{Kind: due.Kind, Choices: d.Choices(), Why: s.why(due.Kind, d)}
	k, err := s.all.Version(d.Change.ID, d.Change.Version)
	known := err == nil
	if known {
		runs, extraction := k.Evidence.FeedbackTraceIDs, k.Extract
		// A withdrawal concerns its signal rather than the correction that drafted the version
		if d.Change.Kind == ChangeWithdraw {
			runs, extraction = []string{d.Ref}, ""
		}
		if a.Words != nil {
			if q.Words, err = a.Words(ctx, runs); err != nil {
				return Question{}, err
			}
		}
		if q.Excerpt, err = iv.excerpt(ctx, extraction); err != nil {
			return Question{}, err
		}
	}
	switch {
	case due.Kind == QuestionAudit:
		q.Change, q.Lessons = carriedWords(d, k)
	case d.Change.Kind == ChangeIntervention:
		q.Change, q.Prepared = s.proposalWords(d.Change.Proposal), []string{"asked for in this conversation"}
	case d.Change.Kind.offered():
		q.Change, q.Prepared = s.offerWords(d)
	case d.reanchors():
		q.Change, q.Prepared = "take the lessons nodloop added in this place as your own", []string{"/nodloop:nod shows them first"}
	case d.Change.Kind == ChangeCompaction:
		q.Change = "approve the merge of lessons that was prepared"
	case known:
		q.Change, q.Lessons = s.changeWords(d, k)
	}
	if d.Change.Kind == ChangeCompaction || d.Decision.RuleCode == RuleRoom {
		q.Prepared = append(q.Prepared, iv.coverageWords(ctx, s, d))
	}
	if len(q.Words)+len(q.Prepared)+len(q.Excerpt) == 0 {
		q.Prepared = []string{"drafted from a correction"}
	}
	return q, nil
}

// The part of the answer the extraction that drafted the version quoted and empty for none
// A trace the store lacks or one that names no excerpt shows none so the question still asks
func (iv *Intervener) excerpt(ctx context.Context, extract string) (string, error) {
	if extract == "" {
		return "", nil
	}
	found, err := iv.traces.Get(ctx, extract)
	if errors.Is(err, record.ErrTraceNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var out struct {
		Excerpt string `json:"excerpt"`
	}
	if found.Name != string(trace.NameExtract) || json.Unmarshal(found.Output, &out) != nil {
		return "", nil
	}
	return out.Excerpt, nil
}

// Why the change needs the person in the words of SPEC-curate Questions
func (s state) why(kind QuestionKind, d Decided) string {
	switch {
	case kind == QuestionAudit:
		return "nodloop checks a share of what it carried out with you"
	case kind == QuestionShadow:
		return "nodloop asks you before it carries out such changes on its own"
	case d.Change.Kind == ChangeWiden:
		return "only your yes lets nodloop carry out more on its own"
	case d.Change.Kind == ChangeActivate:
		return "until you say yes nothing reaches a run without your approval"
	case d.Change.Kind == ChangeIntervention:
		return "an agent proposed it and only you decide it"
	case d.reanchors():
		return "lessons nodloop added here outnumber the ones you confirmed"
	}
	why := map[RuleCode]string{
		RulePersonItem: "it changes a lesson you approved", RuleScopeWider: "it reaches a place you have not decided on",
		RuleVeto: "it touches a block rule", RuleRoom: "it needs room, so lessons would be merged",
		RulePaused: "you paused what nodloop carries out here",
	}
	if w, ok := why[d.Decision.RuleCode]; ok {
		return w
	}
	return "your recorded decisions do not settle it"
}

// The change of a version in one line with the lessons it quotes
func (s state) changeWords(d Decided, k knowledge.Knowledge) (string, []string) {
	where := escape(placeWords(k.Run))
	switch {
	case d.Change.Kind == ChangeWithdraw:
		return "retire the lesson %s you approved", []string{k.Content}
	case d.Change.Kind == ChangeUpdate:
		if cur, ok := s.all.Approved().Find(k.ID); ok {
			return "replace the lesson %s with %s " + where, []string{cur.Content, k.Content}
		}
	case d.Decision.RuleCode == RuleRoom:
		return "add the lesson %s " + where + " after merging lessons to make room", []string{k.Content}
	}
	return "add the lesson %s " + where, []string{k.Content}
}

// What curate carried out in one line with the lesson it quotes
func carriedWords(d Decided, k knowledge.Knowledge) (string, []string) {
	switch d.Decision.Action {
	case ActionDrop:
		return "nodloop dropped the lesson %s since " + reasonWords(d.Decision.Reason), []string{k.Content}
	case ActionWithdraw:
		return "nodloop withdrew the lesson %s since " + reasonWords(d.Decision.Reason), []string{k.Content}
	case ActionJoin:
		return "nodloop joined the lesson %s to an older one that says the same", []string{k.Content}
	}
	return "nodloop added the lesson %s " + escape(placeWords(k.Run)) + " from your earlier decisions", []string{k.Content}
}

// Why curate retired a version in plain words
func reasonWords(r knowledge.Reason) string {
	words := map[knowledge.Reason]string{
		knowledge.ReasonDuplicate: "a lesson already says it", knowledge.ReasonNoEvidence: "the correction behind it no longer stands",
		knowledge.ReasonTempScope: "it was scoped to a temporary folder", knowledge.ReasonConflict: "a later correction went against it",
		knowledge.ReasonWords: "you asked to stop following it",
	}
	if w, ok := words[r]; ok {
		return w
	}
	return "your earlier decisions settled it"
}

// The proposal of the agent in plain words with its first letter lowered for the line it continues
func (s state) proposalWords(id string) string {
	p, ok := s.proposalOf(id)
	if !ok {
		return "carry out what an agent proposed"
	}
	text := s.describe(p.In)
	r, size := utf8.DecodeRuneInString(text)
	return escape(string(unicode.ToLower(r)) + text[size:])
}

// The widening or the activation the knobs offered with what was prepared for it
func (s state) offerWords(d Decided) (string, []string) {
	if d.Change.Kind == ChangeActivate {
		return "let lessons nodloop approves from your earlier decisions reach your sessions", []string{
			fmt.Sprintf("your record holds %s you approved and %s you set as direction", counted(len(s.anchors.Refs), "lesson"), counted(len(s.anchors.Files), "file")),
			"/nodloop:nod shows them first",
		}
	}
	return "let nodloop carry out " + openingWords(*d.Change.Opens) + " without asking you",
		[]string{"your answers on such changes agreed with what nodloop would have done"}
}

// The kind and bin of a widening in plain words
func openingWords(o Opening) string {
	kinds := map[ChangeKind]string{
		ChangeAdd: "new lessons", ChangeUpdate: "updates of lessons", ChangeWithdraw: "withdrawals of lessons it approved",
	}
	bins := map[Bin]string{Bin1: "almost certain of", Bin2: "quite sure of", Bin3: "fairly sure of", Bin4: "unsure of"}
	if b, ok := bins[o.Bin]; ok {
		return kinds[o.Kind] + " it is " + b
	}
	return kinds[o.Kind]
}

// Whether the coverage check of the compaction the change needs passed
func (iv *Intervener) coverageWords(ctx context.Context, s state, d Decided) string {
	id := d.Change.Compaction
	if room, err := s.all.Room(d.Change.ID, d.Change.Version); err == nil && id == "" {
		id = room.Pending
	}
	c, err := iv.ledger.Compaction(ctx, id)
	if err != nil || iv.s.Coverage == nil {
		return "the merge waits for its coverage check"
	}
	cov, err := iv.s.Coverage(ctx, id)
	if err != nil || cov.Passes(c) != nil {
		return "the merge waits for its coverage check"
	}
	return "coverage passed, nothing lost"
}

// Where a lesson reaches in plain words
// The root dir of a repository goes without saying
func placeWords(r *knowledge.RunScope) string {
	if r == nil {
		return "everywhere"
	}
	var parts []string
	for _, key := range slices.Sorted(maps.Keys(r.Labels)) {
		values := r.Labels[key]
		if key != "dir" || !slices.Equal(values, []string{"."}) || len(r.Labels) == 1 {
			parts = append(parts, key+" "+strings.Join(values, " or "))
		}
	}
	if len(parts) == 0 {
		return "everywhere"
	}
	return "in " + strings.Join(parts, " and ")
}

// The text with every percent sign doubled so it stays as written in the format of a change line
func escape(text string) string {
	return strings.ReplaceAll(text, "%", "%%")
}

// The notice of the session with its expose appended before the stop prints it
// 1. once per session and only at a stop that asked no question
// 2. the versions curate approved or withdrew that reach the place since the newest notice shown in the place
// A drop or a join changes no run so it gets none
func (iv *Intervener) Notice(ctx context.Context, a Asking) (Shown, error) {
	if a.Session == "" || a.Mark.CycleID == "" || !Noticeable(a.All, a.Place) {
		return Shown{}, nil
	}
	found, err := iv.traces.List(ctx, record.TraceFilter{Name: string(trace.NameExpose)})
	if err != nil {
		return Shown{}, err
	}
	since, ok, err := iv.lastNotice(ctx, trace.RestoreAll(found), a)
	changed := carriedOut(a.All, a.Place, since)
	if err != nil || !ok || len(changed) == 0 {
		return Shown{}, err
	}
	knobs, err := iv.traces.List(ctx, record.TraceFilter{Name: string(trace.NameKnob)})
	if err != nil {
		return Shown{}, err
	}
	m := a.Mark.inForce(knobTraces(trace.RestoreAll(knobs)), iv.now())
	line := noticeLine(changed)
	x := Exposure{Kind: QuestionNotice, Surface: SurfaceMessage, Rules: QuestionRules, Text: a.Seal(line), Round: 1, Run: a.Run, Runes: utf8.RuneCountInString(line)}
	draw := trace.NewDraw(m.CycleID, m.PolicyVersion, m.Seed, a.Session, drawNotice, 1)
	tr, err := Event{Session: a.Session, Source: trace.SourcePush, Draw: draw}.Expose(x, iv.now())
	if err != nil {
		return Shown{}, err
	}
	if err := iv.traces.Append(ctx, tr.Record()); err != nil {
		return Shown{}, err
	}
	return Shown{Notice: line, Expose: tr}, nil
}

// When the newest notice shown in the place was shown and false when the session had one
// A notice whose run is gone counts as one of another place
func (iv *Intervener) lastNotice(ctx context.Context, exposes trace.Traces, a Asking) (time.Time, bool, error) {
	place := knowledge.RunScope{Producer: a.Place.Producer, Labels: a.Place.Labels.Place()}.String()
	var since time.Time
	for _, x := range exposes {
		var shown Exposure
		if x.Subject != string(QuestionNotice) || json.Unmarshal(x.Input, &shown) != nil {
			continue
		}
		if x.SessionID == a.Session {
			return time.Time{}, false, nil
		}
		if !since.IsZero() || shown.Run == "" {
			continue
		}
		found, err := iv.traces.Get(ctx, shown.Run)
		if errors.Is(err, record.ErrTraceNotFound) {
			continue
		}
		if err != nil {
			return time.Time{}, false, err
		}
		if run := trace.Restore(found); (knowledge.RunScope{Producer: run.Producer, Labels: run.Labels.Place()}).String() == place {
			since = x.Time
		}
	}
	return since, true, nil
}

// Whether curate approved or withdrew a version that reaches the place so a stop may owe its session a notice
func Noticeable(all knowledge.Set, place knowledge.RunScope) bool {
	return len(carriedOut(all, place, time.Time{})) > 0
}

// The versions curate approved or withdrew after the time that reach the place newest first
func carriedOut(all knowledge.Set, place knowledge.RunScope, since time.Time) knowledge.Set {
	var out knowledge.Set
	for _, k := range all {
		withdrawn := k.Status == knowledge.StatusRetired && (k.Reason == knowledge.ReasonConflict || k.Reason == knowledge.ReasonWords)
		if k.Approver == knowledge.ApproverNodloop && k.Decision != "" && k.Time.After(since) && k.Run != nil &&
			k.Run.Admits(place.Producer, place.Labels) && (k.Status == knowledge.StatusApproved || withdrawn) {
			out = append(out, k)
		}
	}
	return out
}

// The words a notice ends with so the person always reads how to take a change back
const noticeTail = ". /nodloop:nod shows them and takes any back"

// The one line of a notice within MaxRunes with the lessons it names in quotes
// Lessons that do not fit are cut before noticeTail
func noticeLine(changed knowledge.Set) string {
	var applied, withdrew, lessons []string
	for _, k := range changed {
		if k.Status == knowledge.StatusApproved {
			applied = append(applied, k.Content)
		} else {
			withdrew = append(withdrew, k.Content)
		}
		lessons = append(lessons, quoted(k.Content, quoteRunes))
	}
	var did []string
	if len(applied) > 0 {
		did = append(did, "applied "+lessonsOf(len(applied)))
	}
	if len(withdrew) > 0 {
		did = append(did, "withdrew "+lessonsOf(len(withdrew)))
	}
	head := fmt.Sprintf("From your earlier decisions nodloop %s here: %s", strings.Join(did, " and "), strings.Join(lessons, " · "))
	if r, room := []rune(head), MaxRunes-utf8.RuneCountInString(noticeTail); len(r) > room {
		head = string(r[:room-1]) + "…"
	}
	return head + noticeTail
}

// The count of lessons a notice names with one as a lesson
func lessonsOf(n int) string {
	if n == 1 {
		return "a lesson"
	}
	return counted(n, "lesson")
}

// The count and the thing with its plural past one
func counted(n int, thing string) string {
	if n == 1 {
		return "1 " + thing
	}
	return fmt.Sprintf("%d %ss", n, thing)
}

// The person's own input to a question the stop showed
type Reply struct {
	Session string
	// The run whose answer the person read before the input
	// A typed word answers only the question shown after that answer since that is the reply the question asked for
	Run string
	// The key of an AskUserQuestion header and empty for a typed word
	Key string
	// The question of the AskUserQuestion call a click answers and its options as the transcript holds them
	Question string
	Options  []Option
	// The HMAC of a text under the key of the record directory that the question of a click is checked with
	Seal func(string) string
	// The choice clicked or the first word typed with case and trailing punctuation ignored
	Word   string
	Person string
}

// What a reply matched and recorded
type Replied struct {
	// Whether the input names a choice of a question shown in the session so no reader takes it for a verdict
	Matched bool
	Choice  Choice
	// The answer it recorded and empty when the question was closed already
	Intervened
}

// Records the answer the person's own input gives to a question the stop showed and carries out its choice under the person's name
// 1. the capture reads the first word typed and the AskUserQuestion click from the transcript and both prove the person with proof reply
// 2. the answer names the expose and takes its source and draw so a pushed answer adds no intervention trace
// 3. a question closed since matches with nothing recorded so the input is still no verdict
// 4. the exposes of the session are read first and the rest of the records only once the input names a choice of one
// So an input that answers nothing reads one kind of trace
func (iv *Intervener) Reply(ctx context.Context, r Reply) (Replied, error) {
	found, err := iv.traces.List(ctx, record.TraceFilter{Name: string(trace.NameExpose), SessionID: r.Session})
	if err != nil {
		return Replied{}, err
	}
	x, choice, ok := shownOf(trace.RestoreAll(found)).replied(r)
	if !ok {
		return Replied{}, nil
	}
	release, err := iv.turn()
	if err != nil {
		return Replied{}, err
	}
	defer release()
	s, err := iv.read(ctx)
	if err != nil {
		return Replied{}, err
	}
	out := Replied{Matched: true, Choice: choice}
	d := s.decides[x.Ref]
	if _, dismissed := s.ints.Dismissed(d.ID); dismissed || s.answered(d.ID) {
		return out, nil
	}
	if err := knowledge.CheckApprover(r.Person); err != nil {
		return Replied{}, err
	}
	by := Submitter{Proof: trace.ProofReply, Person: r.Person, Source: x.Source}
	out.Intervened, err = iv.answered(ctx, d, choice, Event{Session: x.SessionID, Source: x.Source, Draw: *x.Draw}, x.Trace, true, by, s)
	return out, err
}

// The question of the session the input answers with the choice it names
// 1. a click names its question by the key of its header and holds every line the stop showed
// So a model that asks a question of its own under that header never records an answer
// 2. a typed word answers the question shown after the answer the person read
func (xs shownExposes) replied(r Reply) (shownExpose, Choice, bool) {
	for _, x := range xs {
		named := r.Key != "" && x.Key == r.Key && x.shows(r) || r.Key == "" && r.Run != "" && x.Run == r.Run
		if choice, ok := x.Names(r.Word); x.SessionID == r.Session && x.asks() && named && ok {
			return x, choice, true
		}
	}
	return shownExpose{}, "", false
}
