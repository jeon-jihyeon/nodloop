package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/otel"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
)

// The records an export sends
type exportRecords struct {
	runs     trace.Traces
	verdicts feedback.Records
	outcomes feedback.Outcomes
}

// Every record from the time on newest first as the stores list them
func (c exportCommand) read(ctx context.Context, since time.Time) (exportRecords, error) {
	recs, err := c.readTraces(ctx, since)
	if err != nil {
		return exportRecords{}, err
	}
	recs.verdicts, recs.outcomes, err = c.readJudgments(ctx, since)
	return recs, err
}

// The runs from the time on
func (c exportCommand) readTraces(ctx context.Context, since time.Time) (exportRecords, error) {
	traces, err := c.app.traces()
	if err != nil {
		return exportRecords{}, err
	}
	found, err := traces.List(ctx, record.TraceFilter{Name: string(trace.NameRun), Since: since})
	if err != nil {
		return exportRecords{}, err
	}
	return exportRecords{runs: trace.RestoreAll(found)}, nil
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

// The batch of the records with what each names of the records before them
func (c exportCommand) batch(ctx context.Context, recs exportRecords) (otel.Batch, error) {
	traces, err := c.app.traces()
	if err != nil {
		return otel.Batch{}, err
	}
	l := lookup{ctx: ctx, traces: traces, runs: map[string]trace.Trace{}}
	for _, r := range recs.runs {
		l.runs[r.ID] = r
	}
	return l.batch(recs)
}

// What the records of an export name from earlier records
type lookup struct {
	ctx    context.Context
	traces *tracefile.Store
	runs   map[string]trace.Trace
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
