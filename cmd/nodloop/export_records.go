package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

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

// The records an export sends
type exportRecords struct {
	runs     trace.Traces
	calls    trace.Traces
	verdicts feedback.Records
	outcomes feedback.Outcomes
	// Decisions of knowledge versions
	items knowledge.Set
}

// Every record from the time on newest first as the stores list them
func (c exportCommand) read(ctx context.Context, since time.Time) (exportRecords, error) {
	recs, err := c.readTraces(ctx, since)
	if err != nil {
		return exportRecords{}, err
	}
	if recs.verdicts, recs.outcomes, err = c.readJudgments(ctx, since); err != nil {
		return exportRecords{}, err
	}
	ledger, err := c.app.ledger()
	if err != nil {
		return exportRecords{}, err
	}
	all, err := ledger.All(ctx)
	if err != nil {
		return exportRecords{}, err
	}
	for _, k := range all {
		if !k.Time.Before(since) && k.Decided() {
			recs.items = append(recs.items, k)
		}
	}
	return recs, nil
}

// The runs and the model step traces from the time on
func (c exportCommand) readTraces(ctx context.Context, since time.Time) (exportRecords, error) {
	traces, err := c.app.traces()
	if err != nil {
		return exportRecords{}, err
	}
	found, err := traces.List(ctx, record.TraceFilter{Since: since})
	if err != nil {
		return exportRecords{}, err
	}
	var recs exportRecords
	for _, t := range trace.RestoreAll(found) {
		switch {
		case t.Name == trace.NameRun:
			recs.runs = append(recs.runs, t)
		case slices.Contains(callNames, t.Name):
			recs.calls = append(recs.calls, t)
		}
	}
	return recs, nil
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
		runs:     slices.DeleteFunc(slices.Clone(r.runs), func(t trace.Trace) bool { return !t.Time.After(mark) }),
		calls:    slices.DeleteFunc(slices.Clone(r.calls), func(t trace.Trace) bool { return !t.Time.After(mark) }),
		verdicts: slices.DeleteFunc(slices.Clone(r.verdicts), func(v feedback.Feedback) bool { return !v.Time.After(mark) }),
		outcomes: slices.DeleteFunc(slices.Clone(r.outcomes), func(o feedback.Outcome) bool { return !o.Time.After(mark) }),
		items:    slices.DeleteFunc(slices.Clone(r.items), func(k knowledge.Knowledge) bool { return !k.Time.After(mark) }),
	}
}

// The time of the newest record and the mark when no record is newer
func (r exportRecords) newest(mark time.Time) time.Time {
	times := []time.Time{mark}
	for _, t := range slices.Concat(r.runs, r.calls) {
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
	l := lookup{ctx: ctx, traces: traces, verdicts: feedback.RestoreAll(verdicts), runs: map[string]trace.Trace{}}
	for _, r := range recs.runs {
		l.runs[r.ID] = r
	}
	return l.batch(recs)
}

// What the records of an export name from earlier records
type lookup struct {
	ctx    context.Context
	traces *tracefile.Store
	// Every verdict newest first
	verdicts feedback.Records
	runs     map[string]trace.Trace
	// Every extract trace newest first
	// Read once a decision needs it
	extracts trace.Traces
	read     bool
}

func (l *lookup) batch(recs exportRecords) (otel.Batch, error) {
	var b otel.Batch
	var errs []error
	for _, r := range recs.runs {
		b.Runs = append(b.Runs, exportRun(r))
	}
	for _, v := range recs.verdicts {
		run, err := l.run(v.TraceID)
		errs = append(errs, err)
		b.Evaluations = append(b.Evaluations, verdictEvaluation(v, run.SessionID))
	}
	for _, o := range recs.outcomes {
		run, err := l.run(o.TraceID)
		errs = append(errs, err)
		b.Evaluations = append(b.Evaluations, outcomeEvaluation(o, run.SessionID))
	}
	for _, k := range recs.items {
		ev, err := l.decision(k)
		errs = append(errs, err)
		b.Evaluations = append(b.Evaluations, ev)
	}
	for _, t := range recs.calls {
		call, err := l.call(t)
		errs = append(errs, err)
		b.Calls = append(b.Calls, call)
	}
	return b, errors.Join(errs...)
}

// The run of the id and the zero trace when the records hold none
// A run the retention removed still names its span through the derived ids
func (l *lookup) run(id string) (trace.Trace, error) {
	if r, ok := l.runs[id]; ok || id == "" {
		return r, nil
	}
	found, err := l.traces.Get(l.ctx, id)
	if errors.Is(err, record.ErrTraceNotFound) {
		return trace.Trace{}, nil
	}
	if err != nil {
		return trace.Trace{}, err
	}
	r := trace.Restore(found)
	l.runs[id] = r
	return r, nil
}

// The decision on a version linked to the extraction that drafted it
func (l *lookup) decision(k knowledge.Knowledge) (otel.Evaluation, error) {
	ref := otel.Ref{ID: k.ID, Version: k.Version}
	ev := otel.Evaluation{
		Name: otel.EvaluationReview, Time: k.Time, Label: string(k.Status), Reviewer: k.Approver, Human: true,
		Span: otel.DecisionSpan(ref, string(k.Status), k.Time), Candidate: &ref, Content: k.Content,
	}
	extract, err := l.drafted(k.ID, k.Version)
	if err != nil || extract.ID == "" {
		return ev, err
	}
	run, err := l.run(extract.Ref)
	ev.SessionID, ev.Target = run.SessionID, otel.CallSpan(extract.ID)
	return ev, err
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
		c.Point = t.Subject
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
	run, err := l.run(t.Ref)
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

func verdictEvaluation(v feedback.Feedback, sessionID string) otel.Evaluation {
	ev := otel.Evaluation{
		Name: otel.EvaluationVerdict, Time: v.Time, SessionID: sessionID, Label: string(v.Verdict), Explanation: v.Reason,
		ReasonCode: string(v.ReasonCode), Reviewer: v.Reviewer, Human: !v.Implicit(),
		Span: otel.VerdictSpan(v.TraceID, v.Time), Target: otel.RunSpan(v.TraceID),
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
		Explanation: cmp.Or(o.ConfirmedCause, o.Note), Reviewer: o.Reviewer, Human: true,
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
