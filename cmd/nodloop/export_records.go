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
)

// The trace names an export sends as model steps
var callNames = []trace.Name{
	trace.NameExtract, trace.NameClassify, trace.NameCheck, trace.NameCompact, trace.NameReplay, trace.NameConsolidate,
}

// The trace names an export sends as questions and as evaluations of curate and of the write side
var curateNames = []trace.Name{trace.NameExpose, trace.NameAnswer, trace.NameIntervention, trace.NameDecide}

// The names of the stores in the positions of the mark
const (
	storeTraces   = "traces"
	storeVerdicts = "verdicts"
	storeOutcomes = "outcomes"
	storeLedger   = "knowledge"
)

// The records the stores appended after the positions of a mark and the position each store read up to
type appendedRead struct {
	records   exportRecords
	positions map[string]string
}

// Reads each store on from the position the mark keeps for it so a stop reads only what was appended since
// A store whose record a prune or a repair removed and one the mark names no position for read every record as readOn says
func (s recordStores) appended(ctx context.Context, m otel.Mark) (appendedRead, error) {
	traces, tracesAt, err := readOn(m, storeTraces, func(t record.Trace) time.Time { return t.Time },
		func(from string) ([]record.Trace, string, error) {
			return s.traces.After(ctx, record.TraceFilter{}, from)
		})
	if err != nil {
		return appendedRead{}, err
	}
	verdicts, verdictsAt, err := readOn(m, storeVerdicts, func(f record.Feedback) time.Time { return f.Time },
		func(from string) ([]record.Feedback, string, error) { return s.verdicts.After(ctx, from, 0) })
	if err != nil {
		return appendedRead{}, err
	}
	outcomes, outcomesAt, err := readOn(m, storeOutcomes, func(o record.Outcome) time.Time { return o.Time },
		func(from string) ([]record.Outcome, string, error) { return s.outcomes.After(ctx, from, 0) })
	if err != nil {
		return appendedRead{}, err
	}
	items, itemsAt, err := readOn(m, storeLedger, func(k record.Knowledge) time.Time { return k.Time },
		func(from string) ([]record.Knowledge, string, error) { return s.items.After(ctx, from, 0) })
	if err != nil {
		return appendedRead{}, err
	}
	recs := exportRecords{
		traces: trace.RestoreAll(traces), verdicts: feedback.RestoreAll(verdicts), outcomes: feedback.RestoreOutcomes(outcomes), items: knowledge.RestoreAll(items),
	}
	slices.Reverse(recs.traces)
	slices.Reverse(recs.verdicts)
	slices.Reverse(recs.outcomes)
	slices.Reverse(recs.items)
	return appendedRead{records: recs, positions: map[string]string{
		storeTraces: tracesAt, storeVerdicts: verdictsAt, storeOutcomes: outcomesAt, storeLedger: itemsAt,
	}}, nil
}

// The records one store appended after the position the mark keeps for it and the position after them
// 1. a store the mark names no position for reads every record stamped after the time of the mark
// A first export and a mark of the time alone name none
// 2. so does a store whose record of the position a prune or a repair removed
// A record the mark passed is never sent again there but one appended late with a time before the mark is not sent either
func readOn[T any](m otel.Mark, store string, stamp func(T) time.Time, read func(from string) ([]T, string, error)) ([]T, string, error) {
	from, known := m.After[store]
	recs, next, err := read(from)
	if errors.Is(err, record.ErrPositionGone) {
		known = false
		recs, next, err = read("")
	}
	if !known {
		recs = slices.DeleteFunc(recs, func(r T) bool { return !stamp(r).After(m.At) })
	}
	return recs, next, err
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

// The records stamped at the time or later of the kinds an export sends
func (r exportRecords) stamped(since time.Time) exportRecords {
	before := func(at time.Time) bool { return at.Before(since) }
	r.traces = slices.DeleteFunc(r.traces, func(t trace.Trace) bool { return before(t.Time) })
	r.verdicts = slices.DeleteFunc(r.verdicts, func(v feedback.Feedback) bool { return before(v.Time) })
	r.outcomes = slices.DeleteFunc(r.outcomes, func(o feedback.Outcome) bool { return before(o.Time) })
	r.items = slices.DeleteFunc(r.items, func(k knowledge.Knowledge) bool { return before(k.Time) })
	return r.sent()
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

// What the records of an export name from earlier records read from the stores only as far as each name reaches
// 1. each trace is read once by its id and one the stores lack reads as the zero trace
// A trace the retention removed still names its span through the derived ids
// 2. the first read that fails ends the export since a span would lose what it names
type lookup struct {
	ctx    context.Context
	stores recordStores
	// Every knowledge record newest first since the ledger stays small
	ledger knowledge.Set
	// The newest trace of each id read so far with the traces the export sends
	byID map[string]trace.Trace
	// The verdicts of each run newest first and nil until an extraction needs them
	judged map[string]feedback.Records
	// The newest extract trace whose candidate is each version among the extract traces read so far
	drafts map[knowledge.Ref]trace.Trace
	// How many extract traces the reads took newest first
	extracts int
	// Whether those reach the oldest extract trace
	allExtracts bool
	err         error
}

func newLookup(ctx context.Context, s recordStores, recs exportRecords) *lookup {
	ledger, err := s.items.List(ctx)
	l := &lookup{
		ctx: ctx, stores: s, ledger: knowledge.RestoreAll(ledger),
		byID: map[string]trace.Trace{}, drafts: map[knowledge.Ref]trace.Trace{}, err: err,
	}
	for _, t := range recs.traces {
		if _, ok := l.byID[t.ID]; !ok {
			l.byID[t.ID] = t
		}
	}
	return l
}

// The newest trace of the id and the zero trace for an empty id or one the store lacks
func (l *lookup) trace(id string) trace.Trace {
	if id == "" {
		return trace.Trace{}
	}
	if t, ok := l.byID[id]; ok {
		return t
	}
	found, err := l.stores.traces.Get(l.ctx, id)
	var t trace.Trace
	switch {
	case errors.Is(err, record.ErrTraceNotFound):
	case err != nil:
		l.err = cmp.Or(l.err, err)
	default:
		t = trace.Restore(found)
	}
	l.byID[id] = t
	return t
}

// The verdicts of the run newest first
// Every verdict is read once at the first call since a read of the verdicts of one run reads the whole store too
func (l *lookup) verdictsOf(run string) feedback.Records {
	if l.judged == nil {
		found, err := l.stores.verdicts.List(l.ctx, record.FeedbackFilter{})
		l.err = cmp.Or(l.err, err)
		l.judged = map[string]feedback.Records{}
		for _, v := range feedback.RestoreAll(found) {
			l.judged[v.TraceID] = append(l.judged[v.TraceID], v)
		}
	}
	return l.judged[run]
}

func (l *lookup) batch(recs exportRecords) (otel.Batch, error) {
	var b otel.Batch
	for _, v := range recs.verdicts {
		b.Evaluations = append(b.Evaluations, verdictEvaluation(v, l.trace(v.TraceID).SessionID))
	}
	for _, o := range recs.outcomes {
		b.Evaluations = append(b.Evaluations, outcomeEvaluation(o, l.trace(o.TraceID).SessionID))
	}
	for _, k := range recs.items {
		b.Evaluations = append(b.Evaluations, l.decision(k))
	}
	for _, t := range recs.traces {
		l.add(&b, t)
	}
	return b, l.err
}

// The span or the evaluation of one trace in the batch
func (l *lookup) add(b *otel.Batch, t trace.Trace) {
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
// 1. the extract traces are read newest first in reads that double so a recent draft leaves the old traces unread
// 2. each read keeps every draft it holds so a version no extraction drafted reads the store once per export
func (l *lookup) drafted(id string, version int) trace.Trace {
	ref := knowledge.Ref{ID: id, Version: version}
	for limit := max(8, 2*l.extracts); ; limit *= 2 {
		if t, ok := l.drafts[ref]; ok || l.allExtracts {
			return t
		}
		found, err := l.stores.traces.List(l.ctx, record.TraceFilter{Name: string(trace.NameExtract), Limit: limit})
		l.err = cmp.Or(l.err, err)
		l.extracts, l.allExtracts = len(found), err != nil || len(found) < limit
		for _, t := range slices.Backward(found) {
			if out := extractOutput(t.Output); out.Candidate != nil {
				l.drafts[*out.Candidate] = trace.Restore(t)
			}
		}
	}
}

// The model step of a trace with the run it judged and the correction that caused it
func (l *lookup) call(t trace.Trace) otel.Call {
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
	c.SessionID, c.Links = l.trace(t.Ref).SessionID, []otel.SpanRef{otel.RunSpan(t.Ref)}
	if t.Name != trace.NameExtract {
		return c
	}
	start := t.Time.Add(-duration)
	for _, v := range l.verdictsOf(t.Ref) {
		if !v.Time.After(start) {
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
	annotator := map[feedback.Actor]otel.Annotator{
		feedback.ActorPerson: otel.AnnotatorHuman, feedback.ActorCode: otel.AnnotatorCode,
		feedback.ActorModel: otel.AnnotatorLLM,
	}[o.Evaluator()]
	return otel.Evaluation{
		Name: otel.EvaluationOutcome, Time: o.Time, SessionID: sessionID, Label: string(o.Result),
		Explanation: cmp.Or(o.ConfirmedCause, o.Note), Reviewer: o.Reviewer, Annotator: annotator,
		Actor: string(o.Evaluator()), Proof: string(o.Proof),
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
