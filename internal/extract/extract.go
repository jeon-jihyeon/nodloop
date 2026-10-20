// Package extract turns a correction on a run into a lesson checked against the items and candidates the run reaches
package extract

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/classify"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/llm"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// Author of a lesson drafted without a named author
const defaultAuthor = "claude"

type TraceStore interface {
	Get(ctx context.Context, id string) (record.Trace, error)
	Append(ctx context.Context, tr record.Trace) error
}

// Newest first
type FeedbackStore interface {
	List(ctx context.Context, f record.FeedbackFilter) ([]record.Feedback, error)
}

// Answers the critic questions of a draft
type Classifier interface {
	Classify(ctx context.Context, req classify.Request) (classify.Answers, error)
}

// Drafts and checks and proposes lessons through the ledger and records every extraction as an extract trace
// Nothing is kept between calls
type Extractor struct {
	ledger   *knowledge.Ledger
	traces   TraceStore
	verdicts FeedbackStore
	now      func() time.Time
}

func New(ledger *knowledge.Ledger, traces TraceStore, verdicts FeedbackStore, now func() time.Time) *Extractor {
	return &Extractor{ledger: ledger, traces: traces, verdicts: verdicts, now: now}
}

// What a lesson is drafted from
type Reaction struct {
	Run trace.Trace
	// The latest verdict on the run, an edit or a reject
	Verdict feedback.Feedback
	// The approved items the producer and labels of the run reach now
	Reached knowledge.Set
	// The candidates the run reaches in the order they wait
	Waiting knowledge.Set
}

// What an extraction left for the person
type Result struct {
	Relation Relation
	Content  string
	// The item or candidate an update or a duplicate or a conflict names
	Related *knowledge.Knowledge
	// The candidate of an add or an update or the candidate a duplicate corroborated
	Candidate *knowledge.Knowledge
	Overlaps  knowledge.Set
}

// A duplicate of a waiting candidate joined the run to its evidence
func (res Result) Corroborated() bool {
	return res.Relation == RelationDuplicate && res.Candidate != nil
}

// The run with its latest verdict and the items and candidates it reaches
// A session verdict counts because the user picked it before it was recorded
func (e *Extractor) Reaction(ctx context.Context, runID string) (Reaction, error) {
	found, err := e.traces.Get(ctx, runID)
	if err != nil {
		return Reaction{}, err
	}
	run := trace.Restore(found)
	if err := run.CheckRun(); err != nil {
		return Reaction{}, err
	}
	verdicts, err := e.verdicts.List(ctx, record.FeedbackFilter{TraceID: runID})
	if err != nil {
		return Reaction{}, err
	}
	latest := feedback.RestoreAll(verdicts).Latest()
	if len(latest) == 0 || !latest[0].Corrects() {
		return Reaction{}, fmt.Errorf("%w: %s", ErrNotCorrected, runID)
	}
	all, err := e.ledger.All(ctx)
	if err != nil {
		return Reaction{}, err
	}
	return Reaction{Run: run, Verdict: latest[0], Reached: all.For(run.Producer, run.Labels), Waiting: all.Waiting(run.Producer, run.Labels)}, nil
}

// The waiting candidate of id or else its approved item that the run reaches
// A candidate beside an approved version is the newer text and the one a person decides next
func (r Reaction) related(id string) (knowledge.Knowledge, bool) {
	if k, ok := r.Waiting.Find(id); ok {
		return k, true
	}
	return r.Reached.Find(id)
}

// One model call drafts the lesson and the critic judges it
// 1. the code checks run before the critic so a draft code refuses never costs a critic call
// 2. a refusal of either is sent back once with its text and the second refusal is returned
// 3. ledger refusals such as a widened scope are returned at once
// 4. the critic is ClaudeCritic or the plan of classifiers the user set up for the critic point
// 5. every extraction of a run that exists is recorded with its drafts and how it ended
// 6. the trace carries the usage the meter of the drafter took and the wall time from the reaction on
func (e *Extractor) Extract(ctx context.Context, drafter ClaudeDrafter, critic Classifier, runID, author string) (Result, error) {
	start := e.now()
	r, err := e.Reaction(ctx, runID)
	if err != nil {
		return Result{}, err
	}
	var rec Record
	res, err := e.extract(ctx, r, drafter, critic, author, &rec)
	taken := drafter.meter.Take()
	spent := cost{usage: taken.Usage(), model: taken.Model, start: start}
	return res, e.record(ctx, r, PathModel, rec.end(res, err), spent, err)
}

func (e *Extractor) extract(ctx context.Context, r Reaction, drafter ClaudeDrafter, critic Classifier, author string, rec *Record) (Result, error) {
	prompt := r.String()
	d, err := drafter.Draft(ctx, prompt)
	if err != nil {
		rec.add(Attempt{}.refused(RefusalModel, err))
		return Result{}, err
	}
	a, err := r.review(ctx, critic, d)
	rec.add(a)
	if fixable.has(err) {
		if d, err = drafter.Draft(ctx, d.redraftPrompt(prompt, err)); err != nil {
			rec.add(Attempt{}.refused(RefusalModel, err))
			return Result{}, err
		}
		a, err = r.review(ctx, critic, d)
		rec.add(a)
	}
	if err != nil {
		return Result{}, err
	}
	res, err := e.propose(ctx, r, d, author)
	if err != nil {
		rec.refuseLast(err)
	}
	return res, err
}

// Refusals the drafter can fix by writing another draft
type refusals []error

var fixable = refusals{ErrRelationInvalid, ErrNotLesson, ErrCaseList, ErrKeyUnknown, ErrCriticRefused}

func (rs refusals) has(err error) bool {
	return slices.ContainsFunc(rs, func(r error) bool { return errors.Is(err, r) })
}

// The code checks and then the critique of one draft
// No critique checks the code alone
func (r Reaction) judge(d Draft, c *Critique) (Attempt, error) {
	a := Attempt{Draft: d, Critique: c}
	if err := r.check(d); err != nil {
		return a.refused(RefusalCode, err), err
	}
	if c == nil {
		return a, nil
	}
	if err := c.check(); err != nil {
		a.Questions = c.failed()
		return a.refused(RefusalCritic, err), err
	}
	return a, nil
}

// The code checks and then the critic asked about one draft
func (r Reaction) review(ctx context.Context, critic Classifier, d Draft) (Attempt, error) {
	if a, err := r.judge(d, nil); err != nil {
		return a, err
	}
	answers, err := critic.Classify(ctx, classify.Request{Ref: r.Run.ID, State: r.critiquePrompt(d), Questions: criticQuestions})
	if err != nil {
		return Attempt{Draft: d}.refused(RefusalModel, err), err
	}
	c := newCritique(answers)
	return r.judge(d, &c)
}

func complete[T any](ctx context.Context, client llm.Client, req llm.Request) (T, error) {
	var out T
	res, err := client.Complete(ctx, req)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(res.Output, &out); err != nil {
		return out, fmt.Errorf("%w: %w", ErrDraftInvalid, err)
	}
	return out, nil
}

// Checks the draft and the critique the conversation wrote and proposes an add or an update
// A duplicate of a candidate corroborates it and any other duplicate or conflict proposes nothing and answers what it names
// The conversation called no model here so the trace carries no usage
func (e *Extractor) Propose(ctx context.Context, r Reaction, d Draft, c Critique, author string) (Result, error) {
	spent := cost{start: e.now()}
	var rec Record
	a, err := r.judge(d, &c)
	rec.add(a)
	if err != nil {
		return Result{}, e.record(ctx, r, PathConversation, rec.end(Result{}, err), spent, err)
	}
	res, err := e.propose(ctx, r, d, author)
	if err != nil {
		rec.refuseLast(err)
	}
	return res, e.record(ctx, r, PathConversation, rec.end(res, err), spent, err)
}

// The candidate of a judged draft through the ledger
// 1. a duplicate of a waiting candidate joins the run to its evidence and keeps its text
// 2. any other duplicate and every conflict leave the decision to a person
func (e *Extractor) propose(ctx context.Context, r Reaction, d Draft, author string) (Result, error) {
	res := Result{Relation: d.Relation, Content: d.Content}
	related, ok := r.related(d.RelatesTo)
	if ok {
		res.Related = &related
	}
	switch {
	case d.Relation == RelationDuplicate && ok && related.Status == knowledge.StatusCandidate:
		k, err := e.ledger.Corroborate(ctx, related.ID, related.Version, r.Run, feedback.Records{r.Verdict})
		if err != nil {
			return Result{}, err
		}
		res.Candidate = &k
		return res, nil
	case !d.Relation.proposes():
		return res, nil
	}
	k, overlaps, err := e.ledger.Propose(ctx, r.candidate(d, res.Related, cmp.Or(author, defaultAuthor)))
	if err != nil {
		return Result{}, err
	}
	res.Candidate, res.Overlaps = &k, overlaps
	return res, nil
}

// The draft of an add scoped to the run or of the next version of the item or candidate an update names
// An update keeps the scope and the veto and the evidence of what it names so approval never widens or lifts it
func (r Reaction) candidate(d Draft, related *knowledge.Knowledge, author string) knowledge.Knowledge {
	if related == nil {
		scope := d.scope(r.Run)
		return knowledge.Knowledge{
			Kind: d.Kind, Content: d.Content, Run: &scope, Author: author, Drafted: true,
			Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{r.Run.ID}},
		}
	}
	evidence := related.Evidence
	evidence.FeedbackTraceIDs = slices.Clone(evidence.FeedbackTraceIDs)
	if !slices.Contains(evidence.FeedbackTraceIDs, r.Run.ID) {
		evidence.FeedbackTraceIDs = append(evidence.FeedbackTraceIDs, r.Run.ID)
	}
	return knowledge.Knowledge{
		ID: related.ID, Kind: related.Kind, Content: d.Content, Run: related.Run, Veto: related.Veto, Basis: related.Basis,
		Author: author, Drafted: true, Evidence: evidence,
	}
}

// The code checks of a draft
// 1. the relation is valid and an add names nothing while every other relation names an item or a candidate the run reaches
// 2. the content is a one sentence lesson that copies no long line of the output or the edit
// 3. an update adds no case to the item it updates
// 4. every key is one the run carries
func (r Reaction) check(d Draft) error {
	if !d.Relation.Valid() {
		return fmt.Errorf("%w: %q", ErrRelationInvalid, d.Relation)
	}
	item, reached := r.related(d.RelatesTo)
	switch {
	case d.Relation == RelationAdd && d.RelatesTo != "":
		return fmt.Errorf("%w: add names %s", ErrRelationInvalid, d.RelatesTo)
	// The redraft reads this error as what to fix
	// A small local model kept a conflict with the run itself until the error named add
	case d.Relation != RelationAdd && len(r.Reached) == 0 && len(r.Waiting) == 0:
		return fmt.Errorf("%w: %s names %q, but the run reaches no approved item or waiting candidate, so the relation is add with relates_to left out", ErrRelationInvalid, d.Relation, d.RelatesTo)
	case d.Relation != RelationAdd && !reached:
		return fmt.Errorf("%w: %s names %q, which is no approved item or waiting candidate the run reaches", ErrRelationInvalid, d.Relation, d.RelatesTo)
	}
	if err := d.checkLesson(text(r.Run.Output), text(r.Verdict.Edited)); err != nil {
		return err
	}
	if d.Relation == RelationUpdate {
		if err := d.checkUpdate(item.Content); err != nil {
			return err
		}
	}
	for _, key := range d.Keys {
		if _, ok := r.Run.Labels[key]; !ok {
			return fmt.Errorf("%w: %s, and keys lists only %s or nothing", ErrKeyUnknown, key, strings.Join(slices.Sorted(maps.Keys(r.Run.Labels)), " or "))
		}
	}
	return nil
}

// A JSON string as its text and any other value as written
func text(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

// The reaction as the drafter reads it
func (r Reaction) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Run %s of %s\n\nLabels: %s\n\n## Output\n\n%s\n\n## Verdict\n\n%s", r.Run.ID, r.Run.Producer,
		knowledge.RunScope{Producer: r.Run.Producer, Labels: r.Run.Labels}, text(r.Run.Output), r.Verdict.Verdict)
	if r.Verdict.ReasonCode != "" {
		fmt.Fprintf(&b, ", %s", r.Verdict.ReasonCode)
	}
	if r.Verdict.Reason != "" {
		fmt.Fprintf(&b, ": %s", r.Verdict.Reason)
	}
	if len(r.Verdict.Edited) > 0 {
		fmt.Fprintf(&b, "\n\n## Edited output\n\n%s", text(r.Verdict.Edited))
	}
	// Approved items first and then the candidates with the status on each line so the drafter and the critic tell them apart
	for _, part := range []struct {
		title string
		set   knowledge.Set
	}{{"Approved items this run reaches", r.Reached}, {"Waiting candidates this run reaches", r.Waiting}} {
		fmt.Fprintf(&b, "\n\n## %s\n", part.title)
		if len(part.set) == 0 {
			b.WriteString("\nnone")
		}
		for _, k := range part.set {
			fmt.Fprintf(&b, "\n[%s v%d %s %s] %s\nScope: %s", k.ID, k.Version, k.Kind, k.Status, k.Content, k.Run)
		}
	}
	b.WriteString("\n")
	return b.String()
}

// The draft and the reaction as the critic reads them
// The draft comes first since an encoder endpoint truncates a long state from its end
func (r Reaction) critiquePrompt(d Draft) string {
	draft, _ := json.Marshal(d)
	return fmt.Sprintf("## Draft lesson\n\n%s\n\n%s", draft, r)
}
