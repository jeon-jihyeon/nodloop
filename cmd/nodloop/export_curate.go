package main

import (
	"cmp"
	"encoding/json"

	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/otel"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// A decide as the export reads it
type exportDecide struct {
	trace.Trace
	held curate.Decided
	// False for a trace that is no decide or does not decode
	ok bool
}

func readDecide(t trace.Trace) exportDecide {
	d, ok := curate.RestoreDecide(t)
	return exportDecide{Trace: t, held: d, ok: ok}
}

// The enums and numbers of the decide with its draw and none for a trace that is no decide
func (d exportDecide) enums() otel.Decision {
	if !d.ok {
		return otel.Decision{}
	}
	decision := d.held.Decision
	out := otel.Decision{
		Source: string(d.Source), Tier: &decision.Tier, ChangeKind: string(d.held.Change.Kind), RuleCode: string(decision.RuleCode),
		Bin: string(decision.Bin), RetireReason: string(decision.Reason), Vote: decision.Vote,
	}
	if d.Draw != nil {
		out.Cycle, out.P = d.Draw.CycleID, d.Draw.P
	}
	return out
}

// Whether a judge ran on the change so a model and not a rule of nodloop decided it
func (d exportDecide) judged() bool {
	return d.held.Decision.Judge != ""
}

// The decide as an evaluation in the session of the run it cites or of the proposal it asks about
// 1. it links the judge span when a judge ran, the proposal of an agent it asks about and else the run the change cites
// 2. an offer of the knobs cites no run so it links nothing
// 3. its label is the outcome of the judge and else its action such as the drop of a tier 0 rule
func (d exportDecide) evaluation(sessionID string) otel.Evaluation {
	enums := d.enums()
	ev := otel.Evaluation{
		Name: otel.EvaluationCurate, Time: d.Time, SessionID: sessionID,
		Label:     cmp.Or(string(d.held.Decision.Outcome), string(d.held.Decision.Action)),
		Annotator: otel.AnnotatorCode, Span: otel.TraceSpan(d.ID), Decision: &enums,
	}
	switch {
	case d.judged():
		ev.Annotator, ev.Target = otel.AnnotatorLLM, otel.TraceSpan(d.held.Decision.Judge)
	case d.held.Change.Proposal != "":
		ev.Target = otel.TraceSpan(d.held.Change.Proposal)
	case d.Ref != "":
		ev.Target = otel.RunSpan(d.Ref)
	}
	if d.held.Change.ID != "" {
		ev.Candidate = &otel.Ref{ID: d.held.Change.ID, Version: d.held.Change.Version}
	}
	return ev
}

// The decide of a trace in the session of the run it cites or of the proposal it asks about
func (l *lookup) curated(t trace.Trace) otel.Evaluation {
	d := readDecide(t)
	return d.evaluation(l.trace(cmp.Or(t.Ref, d.held.Change.Proposal)).SessionID)
}

// The question an expose trace records with the text of the version it asks about
func (l *lookup) question(t trace.Trace) otel.Question {
	var x curate.Exposure
	_ = json.Unmarshal(t.Input, &x)
	q := otel.Question{
		ID: t.ID, Time: t.Time, SessionID: t.SessionID, Run: x.Run, Kind: t.Subject, Source: string(t.Source), Round: x.Round,
	}
	if t.Draw != nil {
		q.Cycle, q.P = t.Draw.CycleID, t.Draw.P
	}
	if x.Change != nil {
		k, _ := l.ledger.Version(x.Change.ID, x.Change.Version)
		q.Candidate, q.Content = &otel.Ref{ID: x.Change.ID, Version: x.Change.Version}, k.Content
	}
	return q
}

// The evaluation of an answer, an intervention or a decide
func (l *lookup) evaluation(t trace.Trace) otel.Evaluation {
	switch t.Name {
	case trace.NameAnswer:
		return l.judgment(t)
	case trace.NameIntervention:
		return l.intervention(t)
	}
	return l.curated(t)
}

// The answer to a question as a judgment linked to the question span of its expose
// 1. the tier, the change kind, the rule and the bin of the decide the question asked about with the draw of the answer
// 2. a question nobody answered after its second round was recorded by code
// 3. an answer of the write side to a question no expose showed links the decide it names
func (l *lookup) judgment(t trace.Trace) otel.Evaluation {
	var a curate.Answer
	_ = json.Unmarshal(t.Input, &a)
	expose := l.trace(t.Ref)
	var x curate.Exposure
	_ = json.Unmarshal(expose.Input, &x)
	enums := readDecide(l.trace(a.Decide)).enums()
	enums.Source, enums.QuestionKind, enums.Choice, enums.Authority = string(t.Source), expose.Subject, string(a.Choice), string(a.Authority)
	enums.Overturn, enums.Cycle, enums.P = &a.Overturn, "", nil
	if t.Draw != nil {
		enums.Cycle, enums.P = t.Draw.CycleID, t.Draw.P
	}
	ev := otel.Evaluation{
		Name: otel.EvaluationJudgment, Time: t.Time, SessionID: cmp.Or(t.SessionID, expose.SessionID), Label: string(a.Choice),
		Reviewer: a.Person, Annotator: otel.AnnotatorHuman, Proof: string(a.Proof), Span: otel.TraceSpan(t.ID),
		Target: otel.QuestionSpan(x.Run, t.Ref), Decision: &enums,
	}
	if t.Ref == "" {
		ev.Target = otel.TraceSpan(a.Decide)
	}
	if a.Choice == curate.ChoiceNoAnswer {
		ev.Annotator = otel.AnnotatorCode
	}
	if a.Change != nil {
		ev.Candidate = &otel.Ref{ID: a.Change.ID, Version: a.Change.Version}
	}
	return ev
}

// The intervention linked to what it acts on
// A person's is the judgment of a person and one without a proof the proposal of an agent
func (l *lookup) intervention(t trace.Trace) otel.Evaluation {
	var in trace.Intervention
	_ = json.Unmarshal(t.Input, &in)
	ev := otel.Evaluation{
		Name: otel.EvaluationIntervention, Time: t.Time, SessionID: t.SessionID, Label: cmp.Or(string(in.Choice), string(in.Action)),
		Reviewer: in.Person, Annotator: otel.AnnotatorLLM, Proof: string(in.Proof), Span: otel.TraceSpan(t.ID), Target: l.acted(in),
		Decision: &otel.Decision{
			Source: string(t.Source), Choice: string(in.Choice), Action: string(in.Action), Authority: string(in.Authority),
			Knob: in.Knob, Value: in.Value, Before: in.Before,
		},
	}
	if in.Authority == trace.AuthorityPerson {
		ev.Annotator = otel.AnnotatorHuman
	}
	if in.ID != "" {
		ev.Candidate = &otel.Ref{ID: in.ID, Version: in.Version}
	}
	return ev
}

// The span of what an intervention acts on and the zero span when it names nothing the records hold
// 1. the question span of the expose it named or else the carrier of the decide of the question it closes or judges
// 2. the carrier of the intervention a reverse or a resume takes back
// 3. the extraction that drafted the item version it judges
func (l *lookup) acted(in trace.Intervention) otel.SpanRef {
	switch {
	case in.Decide != "":
		x := l.trace(in.Target)
		if x.Name != trace.NameExpose {
			return otel.TraceSpan(in.Decide)
		}
		var shown curate.Exposure
		_ = json.Unmarshal(x.Input, &shown)
		return otel.QuestionSpan(shown.Run, x.ID)
	case in.Reverses != "":
		return otel.TraceSpan(in.Reverses)
	case in.ID == "":
		return otel.SpanRef{}
	}
	if extract := l.drafted(in.ID, in.Version); extract.ID != "" {
		return otel.TraceSpan(extract.ID)
	}
	return otel.SpanRef{}
}

// The decision on a version with the enums of what decided it
// 1. a decision taken from an answer links the question span and a decision of nodloop after a judge the judge span
// 2. any other links the extraction that drafted the version and else what its decide or intervention links
// 3. nodloop decided by a model when a judge ran and by a rule or a signal otherwise
// 4. every other approver is a person
func (l *lookup) decision(k knowledge.Knowledge) otel.Evaluation {
	ref := otel.Ref{ID: k.ID, Version: k.Version}
	ev := otel.Evaluation{
		Name: otel.EvaluationDecision, Time: k.Time, Label: string(k.Status), Reviewer: k.Approver, Annotator: otel.AnnotatorHuman,
		Span: otel.DecisionSpan(ref, string(k.Status), k.Time), Candidate: &ref, Content: k.Content,
	}
	cause, by := l.cause(k.Decision)
	enums := *cmp.Or(by.Decision, &otel.Decision{})
	enums.RetireReason = string(k.Reason)
	ev.SessionID, ev.Target, ev.Decision = by.SessionID, by.Target, &enums
	if k.Approver == knowledge.ApproverNodloop {
		ev.Annotator = cmp.Or(by.Annotator, otel.AnnotatorCode)
	}
	if cause.Name == trace.NameAnswer || readDecide(cause).judged() {
		return ev
	}
	if extract := l.drafted(k.ID, k.Version); extract.ID != "" {
		ev.SessionID, ev.Target = l.trace(extract.Ref).SessionID, otel.TraceSpan(extract.ID)
	}
	return ev
}

// The answer, the intervention or the decide a knowledge record names in decision with its evaluation
// The zero evaluation when it names none or the records lack it
func (l *lookup) cause(id string) (trace.Trace, otel.Evaluation) {
	cause := l.trace(id)
	switch cause.Name {
	case trace.NameAnswer, trace.NameIntervention, trace.NameDecide:
		return cause, l.evaluation(cause)
	}
	return cause, otel.Evaluation{}
}
