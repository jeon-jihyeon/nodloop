package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/classify"
	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/otel"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
)

// The trace names an export sends as model steps
var callNames = []trace.Name{
	trace.NameExtract, trace.NameClassify, trace.NameCheck, trace.NameCompact, trace.NameReplay, trace.NameConsolidate,
}

// The trace names an export sends as questions and as evaluations of curate and of the write side
var curateNames = []trace.Name{trace.NameExpose, trace.NameAnswer, trace.NameIntervention, trace.NameDecide}

// The records an export sends
type exportRecords struct {
	// The runs, the model steps and the curate events newest first
	traces   trace.Traces
	verdicts feedback.Records
	outcomes feedback.Outcomes
	// Decisions of knowledge versions
	items knowledge.Set
	// Every knowledge record whatever its time for the text of a version a question names
	ledger knowledge.Set
}

// Every record from the time on newest first as the stores list them
func (c exportCommand) read(ctx context.Context, since time.Time) (exportRecords, error) {
	traces, err := c.readTraces(ctx, since)
	if err != nil {
		return exportRecords{}, err
	}
	recs := exportRecords{traces: traces}
	if recs.verdicts, recs.outcomes, err = c.readJudgments(ctx, since); err != nil {
		return exportRecords{}, err
	}
	ledger, err := c.app.ledger()
	if err != nil {
		return exportRecords{}, err
	}
	if recs.ledger, err = ledger.All(ctx); err != nil {
		return exportRecords{}, err
	}
	for _, k := range recs.ledger {
		if !k.Time.Before(since) && k.Decided() {
			recs.items = append(recs.items, k)
		}
	}
	return recs, nil
}

// The runs, the model step traces and the curate events from the time on
func (c exportCommand) readTraces(ctx context.Context, since time.Time) (trace.Traces, error) {
	traces, err := c.app.traces()
	if err != nil {
		return nil, err
	}
	found, err := traces.List(ctx, record.TraceFilter{Since: since})
	if err != nil {
		return nil, err
	}
	exported := slices.Concat([]trace.Name{trace.NameRun}, callNames, curateNames)
	var out trace.Traces
	for _, t := range trace.RestoreAll(found) {
		if slices.Contains(exported, t.Name) {
			out = append(out, t)
		}
	}
	return out, nil
}

// The verdicts and the outcomes from the time on
func (c exportCommand) readJudgments(ctx context.Context, since time.Time) (feedback.Records, feedback.Outcomes, error) {
	store, err := c.app.feedback()
	if err != nil {
		return nil, nil, err
	}
	verdicts, err := store.List(ctx, record.FeedbackFilter{Since: since})
	if err != nil {
		return nil, nil, err
	}
	checks, err := c.app.outcomes()
	if err != nil {
		return nil, nil, err
	}
	all, err := checks.List(ctx, "")
	if err != nil {
		return nil, nil, err
	}
	var outcomes feedback.Outcomes
	for _, o := range feedback.RestoreOutcomes(all) {
		if !o.Time.Before(since) {
			outcomes = append(outcomes, o)
		}
	}
	return feedback.RestoreAll(verdicts), outcomes, nil
}

// The records strictly after the mark since the mark itself was sent
func (r exportRecords) after(mark time.Time) exportRecords {
	return exportRecords{
		traces:   slices.DeleteFunc(slices.Clone(r.traces), func(t trace.Trace) bool { return !t.Time.After(mark) }),
		verdicts: slices.DeleteFunc(slices.Clone(r.verdicts), func(v feedback.Feedback) bool { return !v.Time.After(mark) }),
		outcomes: slices.DeleteFunc(slices.Clone(r.outcomes), func(o feedback.Outcome) bool { return !o.Time.After(mark) }),
		items:    slices.DeleteFunc(slices.Clone(r.items), func(k knowledge.Knowledge) bool { return !k.Time.After(mark) }),
		ledger:   r.ledger,
	}
}

// The time of the newest record and the mark when no record is newer
func (r exportRecords) newest(mark time.Time) time.Time {
	times := []time.Time{mark}
	for _, t := range r.traces {
		times = append(times, t.Time)
	}
	for _, v := range r.verdicts {
		times = append(times, v.Time)
	}
	for _, o := range r.outcomes {
		times = append(times, o.Time)
	}
	for _, k := range r.items {
		times = append(times, k.Time)
	}
	return slices.MaxFunc(times, time.Time.Compare)
}

// The batch of the records with what each names of the records before them
func (c exportCommand) batch(ctx context.Context, recs exportRecords) (otel.Batch, error) {
	traces, err := c.app.traces()
	if err != nil {
		return otel.Batch{}, err
	}
	store, err := c.app.feedback()
	if err != nil {
		return otel.Batch{}, err
	}
	verdicts, err := store.List(ctx, record.FeedbackFilter{})
	if err != nil {
		return otel.Batch{}, err
	}
	l := lookup{ctx: ctx, traces: traces, verdicts: feedback.RestoreAll(verdicts), ledger: recs.ledger, byID: map[string]trace.Trace{}}
	for _, t := range recs.traces {
		l.byID[t.ID] = t
	}
	return l.batch(recs)
}

// What the records of an export name from earlier records
type lookup struct {
	ctx    context.Context
	traces *tracefile.Store
	// Every verdict newest first
	verdicts feedback.Records
	// Every knowledge record
	ledger knowledge.Set
	// The traces read by id with the records of the export first
	// A trace the records lack is kept as the zero trace so it is read once
	byID map[string]trace.Trace
	// Every extract trace newest first
	// Read once a decision needs it
	extracts trace.Traces
	read     bool
}

func (l *lookup) batch(recs exportRecords) (otel.Batch, error) {
	var b otel.Batch
	var errs []error
	for _, v := range recs.verdicts {
		run, err := l.trace(v.TraceID)
		errs = append(errs, err)
		b.Evaluations = append(b.Evaluations, verdictEvaluation(v, run.SessionID))
	}
	for _, o := range recs.outcomes {
		run, err := l.trace(o.TraceID)
		errs = append(errs, err)
		b.Evaluations = append(b.Evaluations, outcomeEvaluation(o, run.SessionID))
	}
	for _, k := range recs.items {
		ev, err := l.decision(k)
		errs = append(errs, err)
		b.Evaluations = append(b.Evaluations, ev)
	}
	for _, t := range recs.traces {
		errs = append(errs, l.add(&b, t))
	}
	return b, errors.Join(errs...)
}

// The span or the evaluation of one trace in the batch
func (l *lookup) add(b *otel.Batch, t trace.Trace) error {
	switch {
	case t.Name == trace.NameRun:
		b.Runs = append(b.Runs, exportRun(t))
	case t.Name == trace.NameExpose:
		b.Questions = append(b.Questions, l.question(t))
	case slices.Contains(callNames, t.Name):
		call, err := l.call(t)
		b.Calls = append(b.Calls, call)
		return err
	default:
		ev, err := l.evaluation(t)
		b.Evaluations = append(b.Evaluations, ev)
		return err
	}
	return nil
}

// The trace of the id and the zero trace when the records hold none
// A trace the retention removed still names its span through the derived ids
func (l *lookup) trace(id string) (trace.Trace, error) {
	if t, ok := l.byID[id]; ok || id == "" {
		return t, nil
	}
	found, err := l.traces.Get(l.ctx, id)
	if err != nil && !errors.Is(err, record.ErrTraceNotFound) {
		return trace.Trace{}, err
	}
	var t trace.Trace
	if err == nil {
		t = trace.Restore(found)
	}
	l.byID[id] = t
	return t, nil
}

// The newest extract trace whose candidate is the version
func (l *lookup) drafted(id string, version int) (trace.Trace, error) {
	if !l.read {
		found, err := l.traces.List(l.ctx, record.TraceFilter{Name: string(trace.NameExtract)})
		if err != nil {
			return trace.Trace{}, err
		}
		l.extracts, l.read = trace.RestoreAll(found), true
	}
	for _, t := range l.extracts {
		if out := extractOutput(t.Output); out.Candidate != nil && out.Candidate.ID == id && out.Candidate.Version == version {
			return t, nil
		}
	}
	return trace.Trace{}, nil
}

// The model step of a trace with the run it judged and the correction that caused it
func (l *lookup) call(t trace.Trace) (otel.Call, error) {
	duration := time.Duration(t.DurationMS) * time.Millisecond
	c := otel.Call{
		ID: t.ID, Name: string(t.Name), Time: t.Time, Duration: duration, Subject: t.Subject, Model: t.Model, Error: t.Error,
		Usage: otel.Usage{
			Input: t.Usage.InputTokens, Output: t.Usage.OutputTokens, Thinking: t.Usage.ThinkingTokens,
			CacheRead: t.Usage.CacheReadTokens, CacheWrite: t.Usage.CacheCreateTokens, CostUSD: t.Usage.CostUSD,
		},
	}
	switch t.Name {
	case trace.NameCheck, trace.NameCompact:
		c.Compaction = t.SessionID
	case trace.NameClassify:
		c.Point, c.Judge = t.Subject, exportJudge(t)
	case trace.NameExtract:
		out := extractOutput(t.Output)
		c.Conclusion = out.Conclusion
		if out.Candidate != nil {
			c.Candidate = &otel.Ref{ID: out.Candidate.ID, Version: out.Candidate.Version}
		}
	}
	if t.Ref == "" {
		return c, nil
	}
	run, err := l.trace(t.Ref)
	c.SessionID, c.Links = run.SessionID, []otel.SpanRef{otel.RunSpan(t.Ref)}
	if t.Name != trace.NameExtract {
		return c, err
	}
	start := t.Time.Add(-duration)
	for _, v := range l.verdicts {
		if v.TraceID == t.Ref && !v.Time.After(start) {
			c.Links = append(c.Links, otel.VerdictSpan(t.Ref, v.Time))
			break
		}
	}
	return c, err
}

// What the members of a judge classify answered and found as the decide read it
// Nil for a classify trace of another point or one whose members do not decode
func exportJudge(t trace.Trace) *otel.Judge {
	ballot, ok := classify.RestoreBallot(t)
	if !ok || t.Subject != string(classify.PointJudge) {
		return nil
	}
	judged := curate.Votes(ballot.Votes).Judgment(t.ID)
	j := &otel.Judge{Outcome: string(judged.Outcome), Votes: judged.Share, Bin: string(judged.Bin)}
	for _, v := range ballot.Votes {
		j.Models, j.Efforts = append(j.Models, v.Model), append(j.Efforts, v.Effort)
	}
	return j
}

// The fields of an extract trace output the export reads
type extractRecord struct {
	Conclusion string         `json:"conclusion"`
	Candidate  *knowledge.Ref `json:"candidate"`
}

func extractOutput(raw json.RawMessage) extractRecord {
	var out extractRecord
	_ = json.Unmarshal(raw, &out)
	return out
}

// The run with the items, the draw and the plugin of its input
// An input that does not decode names no item so the run has no arm
func exportRun(r trace.Trace) otel.Run {
	var in knowledge.RunInput
	if json.Unmarshal(r.Input, &in) != nil {
		in = knowledge.RunInput{}
	}
	return otel.Run{
		ID: r.ID, Time: r.Time, Duration: time.Duration(r.DurationMS) * time.Millisecond, Producer: r.Producer, SessionID: r.SessionID,
		Subject: r.Subject, Model: r.Model, Labels: r.Labels, Applied: otelRefs(in.Applied), Withheld: otelRefs(in.Withheld),
		Drawn: in.Drawn, Unit: in.Unit, Share: in.Share, Plugin: in.Plugin, Output: r.Output, Error: r.Error,
	}
}

// A named verdict is a person's whether or not its call proved one, and a verdict the session or the capture inferred is a model's
func verdictEvaluation(v feedback.Feedback, sessionID string) otel.Evaluation {
	ev := otel.Evaluation{
		Name: otel.EvaluationVerdict, Time: v.Time, SessionID: sessionID, Label: string(v.Verdict), Explanation: v.Reason,
		ReasonCode: string(v.ReasonCode), Reviewer: v.Reviewer, Annotator: otel.AnnotatorHuman, Proof: string(v.Proof),
		Span: otel.VerdictSpan(v.TraceID, v.Time), Target: otel.RunSpan(v.TraceID),
	}
	if v.Implicit() {
		ev.Annotator = otel.AnnotatorLLM
	}
	if len(v.Edited) > 0 {
		ev.Edited = string(v.Edited)
		var s string
		if json.Unmarshal(v.Edited, &s) == nil {
			ev.Edited = s
		}
	}
	return ev
}

func outcomeEvaluation(o feedback.Outcome, sessionID string) otel.Evaluation {
	return otel.Evaluation{
		Name: otel.EvaluationOutcome, Time: o.Time, SessionID: sessionID, Label: string(o.Result),
		Explanation: cmp.Or(o.ConfirmedCause, o.Note), Reviewer: o.Reviewer, Annotator: otel.AnnotatorHuman,
		Span: otel.OutcomeSpan(o.TraceID, o.Time), Target: otel.RunSpan(o.TraceID),
	}
}

func otelRefs(refs []knowledge.Ref) []otel.Ref {
	out := make([]otel.Ref, 0, len(refs))
	for _, r := range refs {
		out = append(out, otel.Ref(r))
	}
	return out
}
