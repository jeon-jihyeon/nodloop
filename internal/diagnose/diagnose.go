// Package diagnose builds the review context and validates the review an AI wrote and records both as traces
package diagnose

//go:generate go run ./gen -out ../../plugin/skills/review/SKILL.md

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/analysis"
	"github.com/jeon-jihyeon/nodloop/internal/evidence"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/llm"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

type Status string

const (
	StatusNoAction       Status = "no_action"
	StatusReadyForReview Status = "ready_for_review"
	StatusHold           Status = "hold"
)

// Who writes the review
type Mode string

const (
	ModeInteractive Mode = "interactive" // Claude Code in the conversation
	ModeBatch       Mode = "batch"       // `claude -p` in the batch path
)

func (m Mode) valid() bool {
	return m == ModeInteractive || m == ModeBatch
}

const promptVersion = "diagnose/v9"

// Trace tag when the citation gate turned a review into a hold
const TagGateHold = "gate:hold"

// Strings and lists of strings only so encoding never fails
type Diagnosis struct {
	Status Status `json:"status"`
	// Observation summaries the review relies on
	// Copied so the trace is self contained
	Observations []string `json:"observations"`
	// Ordered by likelihood
	// Empty when Status is no_action or hold
	Causes []Cause `json:"causes"`
	// Ordered steps
	// Each cites the paragraphs it follows and says what it settles
	// On a hold they are the steps that would lift it
	Checks Checks `json:"checks"`
	// What the reviewer still needs to know
	OpenQuestions []string `json:"open_questions"`
	// Set when Status is hold
	HoldReasons []string `json:"hold_reasons,omitempty"`
}

type Cause struct {
	Summary      string   `json:"summary"`
	ParagraphIDs []string `json:"paragraph_ids"`
}

type Check struct {
	Step         string   `json:"step"`
	Purpose      string   `json:"purpose"`
	ParagraphIDs []string `json:"paragraph_ids"`
}

type Checks []Check

// Every paragraph id a check cites
func (cs Checks) Paragraphs() []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.ParagraphIDs...)
	}
	return out
}

// Knowledge ids the Input of a diagnose trace names as given to the model
// Nothing for an input that is not a diagnose input
func KnowledgeApplied(input json.RawMessage) []string {
	var in recordInput
	if json.Unmarshal(input, &in) != nil {
		return nil
	}
	var out []string
	for _, k := range in.Knowledge {
		out = append(out, k.ID)
	}
	return out
}

// Copied to every trace of one review so they group and filter together
type Session struct {
	ID   string
	Tags []string
}

// The batch path in one call
type BatchOptions struct {
	// Past edit and reject feedback taken as examples
	// Zero means none
	Examples int
	// Empty means none
	Knowledge KnowledgeMode
	// Empty means the llm default
	Model   string
	Session Session
	// Events whose reviews never become example candidates
	// eval names its holdout half so no holdout answer reaches another review
	Exclude []string
}

// What Prepare hands to the AI and records
// Candidates are offered and their text comes through Select
type Context struct {
	PendingID     string
	EventID       string
	Mode          Mode
	PolicyVersion string
	PromptVersion string
	ChangeContext evidence.Context
	Session       Session
	Observations  analysis.Observations
	ParagraphIDs  []string
	// Each list stops at the candidate cap
	KnowledgeCandidates []KnowledgeCandidate
	ExampleCandidates   []ExampleCandidate
	// Set when a candidate list was cut at the cap
	CandidatesOmitted bool
	// Observations and paragraphs rendered for the model
	// Empty when loaded back from a trace
	Text string
}

// Whether select had something to choose from
func (c Context) offered() bool {
	return len(c.KnowledgeCandidates) > 0 || len(c.ExampleCandidates) > 0
}

func (c Context) citable() citable {
	known := make(citable, len(c.ParagraphIDs))
	for _, id := range c.ParagraphIDs {
		known[id] = struct{}{}
	}
	return known
}

type Result struct {
	Diagnosis Diagnosis
	TraceID   string
	Forced    bool
	// Reasons the review was sent back instead of recorded
	// Set once per context
	// The next review of the same context is recorded whatever it says
	Revisions []string
}

// Stored as the context trace Input
type contextInput struct {
	Mode          Mode     `json:"mode"`
	PolicyVersion string   `json:"policy_version"`
	PromptVersion string   `json:"prompt_version"`
	ParagraphIDs  []string `json:"paragraph_ids"`
}

// Stored as the context trace Output
// Everything Select and Record need without the text
type contextOutput struct {
	ChangeContext       evidence.Context      `json:"change_context"`
	Metrics             []string              `json:"metrics"`
	Observations        analysis.Observations `json:"observations"`
	KnowledgeCandidates []KnowledgeCandidate  `json:"knowledge_candidates"`
	ExampleCandidates   []ExampleCandidate    `json:"example_candidates"`
	CandidatesOmitted   bool                  `json:"candidates_omitted"`
}

// Every trace of one review copies the session and the event and refers to the pending id
// The context trace itself has no pending id yet so its Ref stays empty
func (c Context) newTrace(name trace.Name, now time.Time) trace.Trace {
	return trace.Trace{
		ID: trace.NewID(now), Name: name, SessionID: c.Session.ID, Subject: c.EventID, Ref: c.PendingID, Time: now.UTC(),
		Tags: slices.Clone(c.Session.Tags),
	}
}

// A trace of the model call that run describes
func (c Context) runTrace(name trace.Name, now time.Time, run modelRun) trace.Trace {
	tr := c.newTrace(name, now)
	tr.Model, tr.Usage, tr.DurationMS = run.model, run.usage, run.durationMS
	return tr
}

// Candidate lists are never nil so the record reads as lists
func (c Context) trace(now time.Time) (trace.Trace, error) {
	tr := c.newTrace(trace.NameContext, now)
	var err error
	in := contextInput{Mode: c.Mode, PolicyVersion: c.PolicyVersion, PromptVersion: c.PromptVersion, ParagraphIDs: c.ParagraphIDs}
	if tr.Input, err = json.Marshal(in); err != nil {
		return trace.Trace{}, err
	}
	out := contextOutput{
		ChangeContext: c.ChangeContext, Metrics: c.Observations.Metrics(), Observations: c.Observations,
		KnowledgeCandidates: c.KnowledgeCandidates, ExampleCandidates: c.ExampleCandidates, CandidatesOmitted: c.CandidatesOmitted,
	}
	if out.KnowledgeCandidates == nil {
		out.KnowledgeCandidates = []KnowledgeCandidate{}
	}
	if out.ExampleCandidates == nil {
		out.ExampleCandidates = []ExampleCandidate{}
	}
	if tr.Output, err = json.Marshal(out); err != nil {
		return trace.Trace{}, err
	}
	return tr, nil
}

// Text is not stored so it stays empty
func contextFrom(tr trace.Trace) (Context, error) {
	if tr.Name != trace.NameContext {
		return Context{}, fmt.Errorf("%w: %s", ErrNotContext, tr.ID)
	}
	var in contextInput
	if err := json.Unmarshal(tr.Input, &in); err != nil {
		return Context{}, fmt.Errorf("%w: context %s: %w", ErrMalformed, tr.ID, err)
	}
	var out contextOutput
	if err := json.Unmarshal(tr.Output, &out); err != nil {
		return Context{}, fmt.Errorf("%w: context %s: %w", ErrMalformed, tr.ID, err)
	}
	return Context{
		PendingID: tr.ID, EventID: tr.Subject, Mode: in.Mode, PolicyVersion: in.PolicyVersion, PromptVersion: in.PromptVersion,
		ChangeContext: out.ChangeContext, Session: Session{ID: tr.SessionID, Tags: tr.Tags}, Observations: out.Observations,
		ParagraphIDs: in.ParagraphIDs, KnowledgeCandidates: out.KnowledgeCandidates, ExampleCandidates: out.ExampleCandidates,
		CandidatesOmitted: out.CandidatesOmitted,
	}, nil
}

// Stored as the diagnose trace Input
// Holds no float and no map so encoding never fails
type recordInput struct {
	// From the context trace so a review names the policy and prompt it was built with
	Mode          Mode   `json:"mode"`
	PolicyVersion string `json:"policy_version"`
	PromptVersion string `json:"prompt_version"`
	// Copied so example candidates read diagnose traces alone
	ChangeContext evidence.Context `json:"change_context"`
	Metrics       []string         `json:"metrics"`
	Selector      Selector         `json:"select_mode,omitempty"`
	// Copied from the select trace with their sizes so eval reads one record
	Knowledge []AppliedKnowledge `json:"knowledge"`
	Examples  []appliedExample   `json:"examples"`
	// Set when the select cut an item at a cap
	Omitted bool `json:"omitted"`
}

// Same change context and at least one shared metric
func (in recordInput) matches(changeContext evidence.Context, metrics []string) bool {
	return in.ChangeContext == changeContext && slices.ContainsFunc(in.Metrics, func(m string) bool { return slices.Contains(metrics, m) })
}

// The diagnose trace without Output
// A context without a select trace names no knowledge and no examples
func (c Context) diagnoseTrace(now time.Time, selected *selectInput, run modelRun) trace.Trace {
	if selected == nil {
		selected = &selectInput{}
	}
	tr := c.runTrace(trace.NameDiagnose, now, run)
	in := recordInput{
		Mode: c.Mode, PolicyVersion: c.PolicyVersion, PromptVersion: c.PromptVersion, ChangeContext: c.ChangeContext,
		Metrics: c.Observations.Metrics(), Selector: selected.Selector, Knowledge: selected.givenKnowledge(),
		Examples: slices.DeleteFunc(append([]appliedExample{}, selected.Examples...), appliedExample.dropped), Omitted: selected.Omitted,
	}
	tr.Input, _ = json.Marshal(in)
	return tr
}

// The reference data a review reads
type Source interface {
	Event(ctx context.Context, id string) (evidence.Event, error)
	Paragraphs(ctx context.Context) ([]evidence.Paragraph, error)
}

// What a review writes and reads back
// Append only and newest first
type TraceStore interface {
	Append(ctx context.Context, t trace.Trace) error
	Get(ctx context.Context, id string) (trace.Trace, error)
	List(ctx context.Context, f trace.Filter) (trace.Traces, error)
}

// Past verdicts a review may learn from
// Newest first
type FeedbackStore interface {
	List(ctx context.Context, f feedback.Filter) ([]feedback.Feedback, error)
}

// The open check and the append after it run under mu
// Two Records or a Select and a Record on one pending id would otherwise both pass the check
// The guard holds within one process only
// The revise and appendReview and openSelect methods expect their caller to hold mu
type Diagnoser struct {
	src      Source
	policy   analysis.Policy
	client   llm.Client
	traces   TraceStore
	feedback FeedbackStore
	ledger   *knowledge.Ledger
	limits   Limits
	now      func() time.Time
	mu       sync.Mutex
}

func New(
	src Source, policy analysis.Policy, limits Limits, client llm.Client, traces TraceStore, verdicts FeedbackStore,
	ledger *knowledge.Ledger, now func() time.Time,
) *Diagnoser {
	return &Diagnoser{
		src: src, policy: policy, client: client, traces: traces, feedback: verdicts, ledger: ledger, limits: limits.withDefaults(), now: now,
	}
}

// Builds the context and records it
// The context trace id is the pending id
func (d *Diagnoser) Prepare(ctx context.Context, eventID string, mode Mode, session Session) (Context, error) {
	return d.prepare(ctx, eventID, mode, session, nil)
}

// excluded names events besides eventID whose reviews are never offered as examples
func (d *Diagnoser) prepare(ctx context.Context, eventID string, mode Mode, session Session, excluded []string) (Context, error) {
	if !mode.valid() {
		return Context{}, fmt.Errorf("%w: %q", ErrUnknownMode, mode)
	}
	ev, err := d.src.Event(ctx, eventID)
	if err != nil {
		return Context{}, err
	}
	paragraphs, err := d.src.Paragraphs(ctx)
	if err != nil {
		return Context{}, err
	}
	obs, err := d.policy.Analyze(ev)
	if err != nil {
		return Context{}, err
	}
	c := Context{
		EventID: eventID, Mode: mode, PolicyVersion: d.policy.Version, PromptVersion: promptVersion, ChangeContext: ev.ChangeContext,
		Session: session, Observations: obs, ParagraphIDs: make([]string, 0, len(paragraphs)),
	}
	for _, p := range paragraphs {
		c.ParagraphIDs = append(c.ParagraphIDs, string(p.ID))
	}
	var examplesOmitted, knowledgeOmitted bool
	excluded = slices.Concat([]string{eventID}, excluded)
	if c.ExampleCandidates, examplesOmitted, err = d.exampleCandidates(ctx, excluded, ev.ChangeContext, obs.Metrics()); err != nil {
		return Context{}, err
	}
	if c.KnowledgeCandidates, knowledgeOmitted, err = d.knowledgeCandidates(ctx, ev.ChangeContext, obs.Moved(), ev.Dims()); err != nil {
		return Context{}, err
	}
	c.CandidatesOmitted = examplesOmitted || knowledgeOmitted
	c.Text = c.render(paragraphs)
	tr, err := c.trace(d.now())
	if err != nil {
		return Context{}, err
	}
	if err := d.traces.Append(ctx, tr); err != nil {
		return Context{}, err
	}
	c.PendingID = tr.ID
	return c, nil
}

func (d *Diagnoser) pending(ctx context.Context, pendingID string) (Context, error) {
	tr, err := d.traces.Get(ctx, pendingID)
	if err != nil {
		return Context{}, err
	}
	return contextFrom(tr)
}

// Validates a review against its context and records it
// 1. the pending id must name a context trace without a diagnose trace
// 2. a context that offered candidates must have a select trace
// 3. the gate runs before the trace is written so Output is always gated
func (d *Diagnoser) Record(ctx context.Context, pendingID string, diag Diagnosis) (Result, error) {
	c, err := d.pending(ctx, pendingID)
	if err != nil {
		return Result{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	selected, err := d.openSelect(ctx, c.PendingID)
	if err != nil {
		return Result{}, err
	}
	if selected == nil && c.offered() {
		return Result{}, fmt.Errorf("%w: %s", ErrNotSelected, c.PendingID)
	}
	cited := diag.cited(c.citable())
	res, err := d.revise(ctx, c, diag, cited.revisions(c.firstStep(cited.leadRunbook())), modelRun{})
	if err != nil || len(res.Revisions) > 0 {
		return res, err
	}
	return d.appendReview(ctx, c, selected, cited, modelRun{})
}

// Usage of the batch path
// Empty in the conversation
type modelRun struct {
	usage      trace.Usage
	model      string
	durationMS int64
}

// Closes the context of the batch path with the review of its own selection
// The selection is never read back from the store so a store that lost it fails as a store and never as a missing select
func (d *Diagnoser) recordRun(ctx context.Context, c Context, selected selectInput, cited Diagnosis, run modelRun) (Result, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.ensureOpen(ctx, c.PendingID); err != nil {
		return Result{}, err
	}
	return d.appendReview(ctx, c, &selected, cited, run)
}

// Gates the cited review and closes the context with it
// The caller has checked that the context is open
func (d *Diagnoser) appendReview(ctx context.Context, c Context, selected *selectInput, cited Diagnosis, run modelRun) (Result, error) {
	tr := c.diagnoseTrace(d.now(), selected, run)
	gated, forced := cited.gate()
	if forced {
		tr.Tags = append(tr.Tags, TagGateHold)
	}
	tr.Output, _ = json.Marshal(gated)
	if err := d.traces.Append(ctx, tr); err != nil {
		return Result{}, err
	}
	return Result{Diagnosis: gated, TraceID: tr.ID, Forced: forced}, nil
}

// Closes the context with a diagnose trace that carries the failure and returns the failure with that trace id
// selected is nil when the failure came before the selection was recorded
// A failure that cannot be recorded comes back without a trace id so the caller sees a store failure
func (d *Diagnoser) recordFailure(ctx context.Context, c Context, selected *selectInput, run modelRun, failure error) (Result, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.ensureOpen(ctx, c.PendingID); err != nil {
		return Result{}, errors.Join(failure, err)
	}
	tr := c.diagnoseTrace(d.now(), selected, run)
	tr.Error = failure.Error()
	if err := d.traces.Append(ctx, tr); err != nil {
		return Result{}, errors.Join(failure, err)
	}
	return Result{TraceID: tr.ID}, failure
}

// ErrRecorded when a diagnose trace already refers to the pending id
func (d *Diagnoser) ensureOpen(ctx context.Context, pendingID string) error {
	recorded, err := d.traces.List(ctx, trace.Filter{Name: trace.NameDiagnose, Ref: pendingID, Limit: 1})
	if err != nil {
		return err
	}
	if len(recorded) > 0 {
		return fmt.Errorf("%w: %s", ErrRecorded, pendingID)
	}
	return nil
}

// Prepare → code side selection → one model call → Record
// The batch path for eval
// No model sits between the tools so the knowledge mode decides
// 1. selected takes the offered candidates in order
// 2. all takes every approved item
// 3. none takes nothing
// 4. examples follow the candidate order up to the count
// 5. the select trace is written before the model call even when nothing was chosen
// 6. the review is recorded with the selection held in memory so the batch path never fails with ErrNotSelected
// 7. a failed selection or model call still records a diagnose trace with Error so the context closes and failures are counted
// 8. that failure comes back with the id of the failed trace
// 9. an error without a trace id means the store failed or no context was built
// 10. a review with defects the model can fix is sent back once through a second model call
// 11. the revise trace keeps the first call and its cost
func (d *Diagnoser) Run(ctx context.Context, eventID string, opts BatchOptions) (Result, error) {
	if d.client == nil {
		return Result{}, ErrNoClient
	}
	knowledgeMode := cmp.Or(opts.Knowledge, KnowledgeNone)
	if !knowledgeMode.valid() {
		return Result{}, fmt.Errorf("%w: %q", ErrUnknownKnowledgeMode, knowledgeMode)
	}
	c, err := d.prepare(ctx, eventID, ModeBatch, opts.Session, opts.Exclude)
	if err != nil {
		return Result{}, err
	}
	selected, sel, err := d.batchSelection(ctx, c, knowledgeMode, opts.Examples)
	if err != nil {
		return d.recordFailure(ctx, c, nil, modelRun{}, err)
	}
	cited, run, err := d.review(ctx, c, c.Text+sel.Text, opts.Model)
	if err != nil {
		return d.recordFailure(ctx, c, &selected, run, err)
	}
	return d.recordRun(ctx, c, selected, cited, run)
}

// The code side selection of the batch path
// Recorded before the model call
func (d *Diagnoser) batchSelection(ctx context.Context, c Context, mode KnowledgeMode, examples int) (selectInput, Selection, error) {
	var items []chosenKnowledge
	var err error
	switch mode {
	case KnowledgeSelected:
		items, err = d.loadKnowledge(ctx, c.knowledgeRefs())
	case KnowledgeAll:
		items, err = d.approvedKnowledge(ctx)
	}
	if err != nil {
		return selectInput{}, Selection{}, err
	}
	chosen, err := d.loadExamples(ctx, c.leadingExamples(examples))
	if err != nil {
		return selectInput{}, Selection{}, err
	}
	selected, sel := d.limits.fit(SelectByCode, items, chosen)
	if err := d.recordSelection(ctx, c, selected); err != nil {
		return selectInput{}, Selection{}, err
	}
	return selected, sel, nil
}

// One model call and a second one when the first review is sent back
// Returns the cited review of the last call
// The run of a failure is the call that failed so its cost is counted
func (d *Diagnoser) review(ctx context.Context, c Context, prompt, model string) (Diagnosis, modelRun, error) {
	diag, run, err := d.complete(ctx, prompt, model)
	if err != nil {
		return Diagnosis{}, run, err
	}
	known := c.citable()
	cited := diag.cited(known)
	d.mu.Lock()
	res, err := d.revise(ctx, c, diag, cited.revisions(c.firstStep(cited.leadRunbook())), run)
	d.mu.Unlock()
	if err != nil || len(res.Revisions) == 0 {
		return cited, run, err
	}
	diag, run, err = d.complete(ctx, revisePrompt(prompt, diag, res.Revisions), model)
	return diag.cited(known), run, err
}

// One model call and its decoded review
// The run carries usage on failure too so the failure trace counts its cost
func (d *Diagnoser) complete(ctx context.Context, prompt, model string) (Diagnosis, modelRun, error) {
	start := d.now()
	res, err := d.client.Complete(ctx, llm.Request{System: Rules, Prompt: prompt, Schema: json.RawMessage(Schema), Model: model})
	run := modelRun{model: model, durationMS: d.now().Sub(start).Milliseconds()}
	if err != nil {
		var re *llm.ResultError
		if errors.As(err, &re) {
			run.usage.CostUSD = re.CostUSD
		}
		return Diagnosis{}, run, err
	}
	run.usage = trace.Usage{
		InputTokens: res.InputTokens, OutputTokens: res.OutputTokens, CacheReadTokens: res.CacheRead, CacheCreateTokens: res.CacheCreate,
		CostUSD: res.CostUSD,
	}
	var diag Diagnosis
	if err := json.Unmarshal(res.Output, &diag); err != nil {
		return Diagnosis{}, run, fmt.Errorf("%w: %w", ErrBadOutput, err)
	}
	return diag, run, nil
}
