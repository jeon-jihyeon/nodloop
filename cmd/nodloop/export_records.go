package main

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/classify"
	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/otel"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// The trace names an export sends as model steps
var callNames = []trace.Name{
	trace.NameExtract, trace.NameClassify, trace.NameCheck, trace.NameCompact, trace.NameReplay, trace.NameConsolidate,
}

// The trace names an export sends as questions and as evaluations of curate and of the write side
var curateNames = []trace.Name{trace.NameExpose, trace.NameAnswer, trace.NameIntervention, trace.NameDecide}

// The records of one store newest first in the order the store appended them with the key that names each
type appended[T any] struct {
	records []T
	key     func(T) string
	time    func(T) time.Time
}

// The records stamped at the time or later
func (a appended[T]) stamped(since time.Time) []T {
	return slices.DeleteFunc(slices.Clone(a.records), func(r T) bool { return a.time(r).Before(since) })
}

// The records appended after the one the cursor names
// 1. a cursor of no record the store holds falls back to the records stamped after the time of the mark since the mark itself was sent
// 2. that holds before the first export that kept a cursor and once a prune removed the record a cursor names
func (a appended[T]) after(cursor string, mark time.Time) []T {
	if i := slices.IndexFunc(a.records, func(r T) bool { return a.key(r) == cursor }); cursor != "" && i >= 0 {
		return slices.Clone(a.records[:i])
	}
	return slices.DeleteFunc(slices.Clone(a.records), func(r T) bool { return !a.time(r).After(mark) })
}

// The key of the newest record appended and the cursor given for a store that holds none
func (a appended[T]) cursor(given string) string {
	if len(a.records) == 0 {
		return given
	}
	return a.key(a.records[0])
}

// The names of the stores in the cursors of the mark
const (
	storeTraces   = "traces"
	storeVerdicts = "verdicts"
	storeOutcomes = "outcomes"
	storeLedger   = "knowledge"
)

// Every record of the stores of the record directory
type stored struct {
	traces   appended[trace.Trace]
	verdicts appended[feedback.Feedback]
	outcomes appended[feedback.Outcome]
	ledger   appended[knowledge.Knowledge]
}

// Reads every store whole
// A read that stops at the first record older than a time would end at a record a slower process appended late with an older time
func (c exportCommand) stored(ctx context.Context) (stored, error) {
	traces, err := c.traces.List(ctx, record.TraceFilter{})
	if err != nil {
		return stored{}, err
	}
	verdicts, err := c.verdicts.List(ctx, record.FeedbackFilter{})
	if err != nil {
		return stored{}, err
	}
	outcomes, err := c.outcomes.List(ctx, "")
	if err != nil {
		return stored{}, err
	}
	ledger, err := c.ledger.All(ctx)
	if err != nil {
		return stored{}, err
	}
	// A verdict and an outcome are named by their run and time and a knowledge record by its version, status and time
	return stored{
		traces: appended[trace.Trace]{trace.RestoreAll(traces), func(t trace.Trace) string { return t.ID }, func(t trace.Trace) time.Time { return t.Time }},
		verdicts: appended[feedback.Feedback]{feedback.RestoreAll(verdicts),
			func(v feedback.Feedback) string { return fmt.Sprintf("%s:%d", v.TraceID, v.Time.UnixNano()) },
			func(v feedback.Feedback) time.Time { return v.Time }},
		outcomes: appended[feedback.Outcome]{feedback.RestoreOutcomes(outcomes),
			func(o feedback.Outcome) string { return fmt.Sprintf("%s:%d", o.TraceID, o.Time.UnixNano()) },
			func(o feedback.Outcome) time.Time { return o.Time }},
		ledger: appended[knowledge.Knowledge]{ledger,
			func(k knowledge.Knowledge) string {
				return fmt.Sprintf("%s:%d:%s:%d", k.ID, k.Version, k.Status, k.Time.UnixNano())
			},
			func(k knowledge.Knowledge) time.Time { return k.Time }},
	}, nil
}

// The records stamped at the time or later
func (s stored) since(at time.Time) exportRecords {
	return exportRecords{traces: s.traces.stamped(at), verdicts: s.verdicts.stamped(at), outcomes: s.outcomes.stamped(at), items: s.ledger.stamped(at)}.sent()
}

// The records each store appended after where the mark left it
func (s stored) after(m otel.Mark) exportRecords {
	return exportRecords{
		traces: s.traces.after(m.After[storeTraces], m.At), verdicts: s.verdicts.after(m.After[storeVerdicts], m.At),
		outcomes: s.outcomes.after(m.After[storeOutcomes], m.At), items: s.ledger.after(m.After[storeLedger], m.At),
	}.sent()
}

// The newest record of each store as the next export reads on from it and no cursor for a store that never held one
func (s stored) cursors(given map[string]string) map[string]string {
	out := map[string]string{
		storeTraces: s.traces.cursor(given[storeTraces]), storeVerdicts: s.verdicts.cursor(given[storeVerdicts]),
		storeOutcomes: s.outcomes.cursor(given[storeOutcomes]), storeLedger: s.ledger.cursor(given[storeLedger]),
	}
	maps.DeleteFunc(out, func(_, cursor string) bool { return cursor == "" })
	return out
}

// What the records of an export name from every record the stores hold
func (s stored) lookup() lookup {
	l := lookup{verdicts: s.verdicts.records, ledger: s.ledger.records, byID: map[string]trace.Trace{}}
	for _, t := range s.traces.records {
		if _, ok := l.byID[t.ID]; !ok {
			l.byID[t.ID] = t
		}
		if t.Name == trace.NameExtract {
			l.extracts = append(l.extracts, t)
		}
	}
	return l
}

// The records an export sends
type exportRecords struct {
	// The runs, the model steps and the curate events newest first
	traces   trace.Traces
	verdicts feedback.Records
	outcomes feedback.Outcomes
	// Decisions of knowledge versions
	items knowledge.Set
}

// The records of the kinds an export sends
func (r exportRecords) sent() exportRecords {
	exported := slices.Concat([]trace.Name{trace.NameRun}, callNames, curateNames)
	r.traces = slices.DeleteFunc(r.traces, func(t trace.Trace) bool { return !slices.Contains(exported, t.Name) })
	r.items = slices.DeleteFunc(r.items, func(k knowledge.Knowledge) bool { return !k.Decided() })
	return r
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

// What the records of an export name from earlier records
// A record the records lack reads as the zero record since a trace the retention removed still names its span through the derived ids
type lookup struct {
	// Every trace by id with the newest record of each
	byID map[string]trace.Trace
	// Every verdict newest first
	verdicts feedback.Records
	// Every knowledge record
	ledger knowledge.Set
	// Every extract trace newest first
	extracts trace.Traces
}

func (l lookup) batch(recs exportRecords) otel.Batch {
	var b otel.Batch
	for _, v := range recs.verdicts {
		b.Evaluations = append(b.Evaluations, verdictEvaluation(v, l.byID[v.TraceID].SessionID))
	}
	for _, o := range recs.outcomes {
		b.Evaluations = append(b.Evaluations, outcomeEvaluation(o, l.byID[o.TraceID].SessionID))
	}
	for _, k := range recs.items {
		b.Evaluations = append(b.Evaluations, l.decision(k))
	}
	for _, t := range recs.traces {
		l.add(&b, t)
	}
	return b
}

// The span or the evaluation of one trace in the batch
func (l lookup) add(b *otel.Batch, t trace.Trace) {
	switch {
	case t.Name == trace.NameRun:
		b.Runs = append(b.Runs, exportRun(t))
	case t.Name == trace.NameExpose:
		b.Questions = append(b.Questions, l.question(t))
	case slices.Contains(callNames, t.Name):
		b.Calls = append(b.Calls, l.call(t))
	default:
		b.Evaluations = append(b.Evaluations, l.evaluation(t))
	}
}

// The newest extract trace whose candidate is the version
func (l lookup) drafted(id string, version int) trace.Trace {
	for _, t := range l.extracts {
		if out := extractOutput(t.Output); out.Candidate != nil && out.Candidate.ID == id && out.Candidate.Version == version {
			return t
		}
	}
	return trace.Trace{}
}

// The model step of a trace with the run it judged and the correction that caused it
func (l lookup) call(t trace.Trace) otel.Call {
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
		return c
	}
	c.SessionID, c.Links = l.byID[t.Ref].SessionID, []otel.SpanRef{otel.RunSpan(t.Ref)}
	if t.Name != trace.NameExtract {
		return c
	}
	start := t.Time.Add(-duration)
	for _, v := range l.verdicts {
		if v.TraceID == t.Ref && !v.Time.After(start) {
			c.Links = append(c.Links, otel.VerdictSpan(t.Ref, v.Time))
			break
		}
	}
	return c
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
