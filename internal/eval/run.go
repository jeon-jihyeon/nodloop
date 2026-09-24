package eval

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/jeon-jihyeon/nodloop/internal/diagnose"
	"github.com/jeon-jihyeon/nodloop/internal/evidence"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// The labeled set a run reviews and scores
type Source interface {
	Labels(ctx context.Context) ([]evidence.Label, error)
}

// Traces of a session read back for scoring and traces of every session read for the holdout leak check
// Newest first
type TraceStore interface {
	Get(ctx context.Context, id string) (trace.Trace, error)
	List(ctx context.Context, f trace.Filter) (trace.Traces, error)
}

// Verdicts joined with the traces they judge
// Newest first
type FeedbackStore interface {
	List(ctx context.Context, f feedback.Filter) ([]feedback.Feedback, error)
}

// Runs the labeled set through the batch path and scores the traces
// Nothing is kept between commands
type Runner struct {
	src       Source
	diagnoser *diagnose.Diagnoser
	traces    TraceStore
	feedback  FeedbackStore
	ledger    *knowledge.Ledger
}

func New(src Source, diagnoser *diagnose.Diagnoser, traces TraceStore, feedback FeedbackStore, ledger *knowledge.Ledger) *Runner {
	return &Runner{src: src, diagnoser: diagnoser, traces: traces, feedback: feedback, ledger: ledger}
}

type RunOptions struct {
	SessionID string
	Model     string
	// Examples injected in the examples condition
	// Zero means none
	Examples int
	// Conditions to run in holdout
	// Empty means all four
	Conditions []Condition
	// Event ids to run
	// Empty means every event of the set
	Events []string
	// Reviews in flight at once
	// Zero means 4 and a negative value is refused
	Parallel int
	// Progress lines
	// Nil means silent
	Log io.Writer
}

const defaultParallel = 4

// One job per event and condition in event order then condition order
// No job takes a review of an excluded event as an example
func (o RunOptions) jobs(eventIDs []string, conditions []Condition, excluded []string) []job {
	out := make([]job, 0, len(eventIDs)*len(conditions))
	for _, id := range eventIDs {
		for _, cond := range conditions {
			batch := cond.options(o.Examples)
			batch.Model, batch.Session, batch.Exclude = o.Model, diagnose.Session{ID: o.SessionID, Tags: []string{string(cond)}}, excluded
			out = append(out, job{id, cond, batch})
		}
	}
	return out
}

// Holdout conditions to run in the given order
// An unknown or seed condition is an error so a typo fails before any review
func (o RunOptions) conditions() ([]Condition, error) {
	if len(o.Conditions) == 0 {
		return holdoutConditions(), nil
	}
	for _, cond := range o.Conditions {
		if !slices.Contains(holdoutConditions(), cond) {
			return nil, fmt.Errorf("%w: %q", ErrUnknownCondition, cond)
		}
	}
	return o.Conditions, nil
}

// One review to run
// The condition is the tag that groups its traces
type job struct {
	eventID   string
	condition Condition
	opts      diagnose.BatchOptions
}

// Labels of the source in source order
type labelSet []evidence.Label

// Events that donate feedback
func (ls labelSet) seeds() labelSet {
	var out labelSet
	for _, l := range ls {
		if l.Seed {
			out = append(out, l)
		}
	}
	return out
}

// Events that are scored
func (ls labelSet) holdout() labelSet {
	var out labelSet
	for _, l := range ls {
		if !l.Seed {
			out = append(out, l)
		}
	}
	return out
}

// The labels a run reviews
// 1. an empty set is an error since the run would review nothing
// 2. events narrow the set when given
// 3. an event outside the set is an error so a typo never runs silently as nothing
func (ls labelSet) pick(events []string) (labelSet, error) {
	if len(ls) == 0 {
		return nil, ErrNoLabels
	}
	if len(events) == 0 {
		return ls, nil
	}
	var out labelSet
	for _, l := range ls {
		if slices.Contains(events, l.EventID) {
			out = append(out, l)
		}
	}
	picked := out.ids()
	for _, id := range events {
		if !slices.Contains(picked, id) {
			return nil, fmt.Errorf("%w: %s", ErrEventOutsideSet, id)
		}
	}
	return out, nil
}

// Event ids in source order
func (ls labelSet) ids() []string {
	out := make([]string, 0, len(ls))
	for _, l := range ls {
		out = append(out, l.EventID)
	}
	return out
}

// The first record that lets a holdout event reach a holdout review
// The labels are the whole holdout half and the records span every session since example candidates come from feedback of any session
// 1. no trace of a holdout event carries feedback
// 2. no approved knowledge cites a trace of a holdout event
func (ls labelSet) leak(traces trace.Traces, records feedback.Records, approved knowledge.Set) error {
	events := make(map[string]struct{}, len(ls))
	for _, l := range ls {
		events[l.EventID] = struct{}{}
	}
	holdoutTraces := map[string]string{}
	for _, tr := range traces {
		if _, ok := events[tr.Subject]; ok {
			holdoutTraces[tr.ID] = tr.Subject
		}
	}
	for _, fb := range records {
		if subject, ok := holdoutTraces[fb.TraceID]; ok {
			return fmt.Errorf("%w: %s of %s", ErrHoldoutFeedback, fb.TraceID, subject)
		}
	}
	for _, k := range approved {
		for _, id := range k.Evidence.TraceIDs() {
			if subject, ok := holdoutTraces[id]; ok {
				return fmt.Errorf("%w: %s cites %s of %s", ErrHoldoutKnowledge, k.ID, id, subject)
			}
		}
	}
	return nil
}

// Reviews every seed event without examples so a reviewer can annotate the results
// Seed runs only the seed condition so a condition list is refused instead of ignored
func (r *Runner) Seed(ctx context.Context, opts RunOptions) ([]trace.Trace, error) {
	if len(opts.Conditions) > 0 {
		return nil, ErrSeedConditions
	}
	all, err := r.src.Labels(ctx)
	if err != nil {
		return nil, err
	}
	labels, err := labelSet(all).seeds().pick(opts.Events)
	if err != nil {
		return nil, err
	}
	jobs := opts.jobs(labels.ids(), []Condition{ConditionSeed}, labelSet(all).holdout().ids())
	return r.reviewAll(ctx, jobs, opts.Parallel, opts.Log)
}

// Reviews every holdout event under each condition
// 1. refuses when any trace of a holdout event in any session has feedback or when approved knowledge cites one
// 2. no review takes a review of a holdout event as an example even when feedback on one arrives during the run
func (r *Runner) Holdout(ctx context.Context, opts RunOptions) ([]trace.Trace, error) {
	conditions, err := opts.conditions()
	if err != nil {
		return nil, err
	}
	all, err := r.src.Labels(ctx)
	if err != nil {
		return nil, err
	}
	holdout := labelSet(all).holdout()
	labels, err := holdout.pick(opts.Events)
	if err != nil {
		return nil, err
	}
	traces, err := r.traces.List(ctx, trace.Filter{})
	if err != nil {
		return nil, err
	}
	records, err := r.feedback.List(ctx, feedback.Filter{})
	if err != nil {
		return nil, err
	}
	set, err := r.ledger.All(ctx)
	if err != nil {
		return nil, err
	}
	if err := holdout.leak(traces, records, set.Approved()); err != nil {
		return nil, err
	}
	return r.reviewAll(ctx, opts.jobs(labels.ids(), conditions, holdout.ids()), opts.Parallel, opts.Log)
}

// Runs the jobs with parallel reviews in flight
// Zero parallel means the default and a nil log means silent
// 1. a negative parallel is refused before any review
// 2. traces come back in job order
// 3. the first error stops new reviews while those in flight finish so none is cut into a false model failure
// 4. the traces finished so far are returned with the first error in job order
// 5. a context cancelled before every job started is the error when no job failed
// 6. a panic becomes the error of its job instead of taking the whole run down
func (r *Runner) reviewAll(ctx context.Context, jobs []job, parallel int, log io.Writer) ([]trace.Trace, error) {
	if parallel < 0 {
		return nil, fmt.Errorf("%w: %d", ErrNegativeParallel, parallel)
	}
	progress := &lockedWriter{w: cmp.Or(log, io.Discard)}
	results := make([]trace.Trace, len(jobs))
	errs := make([]error, len(jobs))
	var failed atomic.Bool
	var wg sync.WaitGroup
	var stopped error
	slots := make(chan struct{}, cmp.Or(parallel, defaultParallel))
	for i, j := range jobs {
		slots <- struct{}{}
		if stopped = ctx.Err(); stopped != nil || failed.Load() {
			break
		}
		wg.Go(func() {
			defer func() {
				if p := recover(); p != nil {
					errs[i] = fmt.Errorf("%w: %s: %v", ErrReviewPanicked, j.eventID, p)
				}
				if errs[i] != nil {
					failed.Store(true)
				}
				<-slots
			}()
			results[i], errs[i] = r.review(ctx, j, progress)
		})
	}
	wg.Wait()
	out := make([]trace.Trace, 0, len(jobs))
	for i := range jobs {
		if results[i].ID != "" {
			out = append(out, results[i])
		}
	}
	for _, err := range errs {
		if err != nil {
			return out, err
		}
	}
	return out, stopped
}

// Progress lines from several workers must not interleave inside one line
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// 1. a review that fails after its context was built comes back with its failed trace and the run continues
// 2. a cancelled context is the error of that job and never a model failure
// 3. a failure without a trace is a store failure or a review that never built its context and ends the run
func (r *Runner) review(ctx context.Context, j job, log io.Writer) (trace.Trace, error) {
	res, err := r.diagnoser.Run(ctx, j.eventID, j.opts)
	if err != nil {
		switch {
		case ctx.Err() != nil:
			return trace.Trace{}, ctx.Err()
		case res.TraceID == "":
			return trace.Trace{}, fmt.Errorf("%w: %s: %w", ErrNoFailedTrace, j.eventID, err)
		}
		fmt.Fprintf(log, "%s\t%s\tfailed: %v\n", j.eventID, j.condition, err)
		return r.traces.Get(ctx, res.TraceID)
	}
	tr, err := r.traces.Get(ctx, res.TraceID)
	if err != nil {
		return trace.Trace{}, err
	}
	fmt.Fprintf(log, "%s\t%s\t%s\tforced=%t\t$%.4f\t%dms\n", j.eventID, j.condition, res.Diagnosis.Status, res.Forced, tr.Usage.CostUSD, tr.DurationMS)
	return tr, nil
}

type Report struct {
	SessionID string    `json:"session_id"`
	Summaries []Summary `json:"summaries"`
	// Per condition against the baseline
	// Empty when the baseline was not run
	AgainstBaseline map[Condition]Comparison `json:"against_baseline"`
	Scores          []Score                  `json:"scores"`
}

// Markdown table with one row per condition
// 1. not applicable cells print as a dash
// 2. verdicts print as approve:edit:reject counts
// 3. each condition is compared with the baseline event by event or marked not compared without one
func (rep Report) Table() string {
	var b strings.Builder
	b.WriteString("| condition | events | status acc | hold acc | hold precision | citation p | citation r | required checks | first check | knowledge hit | misapplied | revised | forced holds | failures | annotated | approve:edit:reject | edit rate | mean edit width | mean cost usd | mean input tokens | mean output tokens | p50 ms | p95 ms |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, s := range rep.Summaries {
		fmt.Fprintf(&b, "| %s | %d | %s | %s | %s | %s | %s | %s | %s | %s | %d | %d | %d | %d | %d | %d:%d:%d | %s | %s | %.4f | %.0f | %.0f | %d | %d |\n",
			s.Condition, s.Events, ratio(s.StatusAccuracy), ratio(s.HoldRecall), ratio(s.HoldPrecision), ratio(s.CitationPrecision), ratio(s.CitationRecall), ratio(s.RequiredChecks), ratio(s.FirstCheck), ratio(s.KnowledgeHit),
			s.Misapplications, s.Revisions, s.ForcedHolds, s.Failures, s.Annotated, s.Approvals, s.Edits, s.Rejections, ratio(s.EditRate), ratio(s.MeanEditWidth),
			s.MeanCostUSD, s.MeanInputTokens, s.MeanOutputTokens, s.P50DurationMS, s.P95DurationMS)
	}
	b.WriteString("\nAgainst " + string(ConditionBaseline))
	if len(rep.AgainstBaseline) == 0 {
		b.WriteString(": not compared\n")
		return b.String()
	}
	b.WriteString("\n\n| condition | fixed | regressed | weakened |\n|---|---|---|---|\n")
	for _, cond := range allConditions() {
		c, ok := rep.AgainstBaseline[cond]
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", cond, eventIDs(c.Fixed), eventIDs(c.Regressed), eventIDs(c.Weakened))
	}
	return b.String()
}

// Events that moved in one table cell
type eventIDs []string

// A dash when no event moved
func (ids eventIDs) String() string {
	if len(ids) == 0 {
		return "-"
	}
	return strings.Join(ids, ", ")
}

// A metric in one table cell
type ratio float64

// Two decimals or a dash when not applicable
func (r ratio) String() string {
	if r == notApplicable {
		return "-"
	}
	return fmt.Sprintf("%.2f", float64(r))
}

// Joins the session's diagnose traces with labels and the latest feedback per trace
func (r *Runner) Report(ctx context.Context, sessionID string) (Report, error) {
	labels, err := r.src.Labels(ctx)
	if err != nil {
		return Report{}, err
	}
	traces, err := r.traces.List(ctx, trace.Filter{Name: trace.NameDiagnose, SessionID: sessionID})
	if err != nil {
		return Report{}, err
	}
	if len(traces) == 0 {
		return Report{}, fmt.Errorf("%w: %q", ErrNoTraces, sessionID)
	}
	revised, err := r.traces.List(ctx, trace.Filter{Name: trace.NameRevise, SessionID: sessionID})
	if err != nil {
		return Report{}, err
	}
	rs, revisedIDs := reviews(traces).withRevisions(revised)
	all, err := r.feedback.List(ctx, feedback.Filter{})
	if err != nil {
		return Report{}, err
	}
	return labelSet(labels).score(rs.newest(), feedback.Records(all).Latest(), revisedIDs).report(sessionID), nil
}

// The diagnose traces of one session
type reviews []trace.Trace

// Each review that record sent back once carries the usage and duration of both model calls
// The revise trace refers to the same context as the review
// The ids of the revised reviews come back so the score knows
func (rs reviews) withRevisions(revised []trace.Trace) (reviews, map[string]struct{}) {
	byRef := map[string]trace.Trace{}
	for _, tr := range revised {
		byRef[tr.Ref] = tr
	}
	out := make(reviews, 0, len(rs))
	ids := map[string]struct{}{}
	for _, tr := range rs {
		if first, ok := byRef[tr.Ref]; ok && tr.Ref != "" {
			tr.Usage = tr.Usage.Add(first.Usage)
			tr.DurationMS += first.DurationMS
			ids[tr.ID] = struct{}{}
		}
		out = append(out, tr)
	}
	return out, ids
}

// The newest trace per event and condition so a re-run replaces an earlier one
// 1. the first tag that names a condition places the trace
// 2. traces without a condition tag are left out
func (rs reviews) newest() map[Condition][]trace.Trace {
	type key struct {
		eventID   string
		condition Condition
	}
	seen := map[key]struct{}{}
	out := map[Condition][]trace.Trace{}
	for _, tr := range rs {
		for _, tag := range tr.Tags {
			cond := Condition(tag)
			if !cond.valid() {
				continue
			}
			if _, dup := seen[key{tr.Subject, cond}]; !dup {
				seen[key{tr.Subject, cond}] = struct{}{}
				out[cond] = append(out[cond], tr)
			}
			break
		}
	}
	return out
}

// One score per trace of a labeled event joined with the latest feedback on that trace
// Traces of events outside the set are left out
func (ls labelSet) score(byCondition map[Condition][]trace.Trace, latest feedback.Records, revised map[string]struct{}) conditionScores {
	byEvent := map[string]evidence.Label{}
	for _, l := range ls {
		byEvent[l.EventID] = l
	}
	byTrace := map[string]feedback.Feedback{}
	for _, fb := range latest {
		byTrace[fb.TraceID] = fb
	}
	out := conditionScores{}
	for cond, traces := range byCondition {
		for _, tr := range traces {
			l, ok := byEvent[tr.Subject]
			if !ok {
				continue
			}
			fb := byTrace[tr.ID]
			_, isRevised := revised[tr.ID]
			out[cond] = append(out[cond], newScore(cond, l, tr, isRevised, fb.Verdict, fb.Edited))
		}
	}
	return out
}

// Scores of a session per condition
// Owns the report policy so Runner's Report method only loads
type conditionScores map[Condition]scores

// 1. conditions in report order and scores in event order
// 2. every holdout condition but the baseline is compared with the baseline when the baseline was run
func (cs conditionScores) report(sessionID string) Report {
	rep := Report{SessionID: sessionID, AgainstBaseline: map[Condition]Comparison{}}
	base := cs[ConditionBaseline].byEvent()
	for _, cond := range allConditions() {
		if len(cs[cond]) == 0 {
			continue
		}
		ss := scores(slices.SortedFunc(slices.Values(cs[cond]), Score.compare))
		rep.Summaries = append(rep.Summaries, ss.summary())
		rep.Scores = append(rep.Scores, ss...)
		if cond == ConditionSeed || cond == ConditionBaseline || len(cs[ConditionBaseline]) == 0 {
			continue
		}
		rep.AgainstBaseline[cond] = ss.comparedTo(base)
	}
	return rep
}
