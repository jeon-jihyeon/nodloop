package otel

import (
	"cmp"
	"time"
)

// The event name of the GenAI conventions for an evaluation
const evaluationEvent = "gen_ai.evaluation.result"

// Severity INFO of the OTLP log data model
const severityInfo = 9

// What an evaluation judged
type EvaluationName string

const (
	EvaluationVerdict      EvaluationName = "nodloop.verdict"      // a verdict on a run
	EvaluationOutcome      EvaluationName = "nodloop.outcome"      // what a real check found for a run
	EvaluationDecision     EvaluationName = "nodloop.decision"     // a person or nodloop approved or retired a version
	EvaluationJudgment     EvaluationName = "nodloop.judgment"     // the answer to a question or no answer after its second round
	EvaluationIntervention EvaluationName = "nodloop.intervention" // an intervention of the person or the proposal of an agent
	EvaluationCurate       EvaluationName = "nodloop.curate"       // a decide of curate on one change
	EvaluationJudge        EvaluationName = "nodloop.judge"        // what the judge predicted as the log record of its classify trace
)

// Who judged as the OpenInference annotation spec names it
// The spec allows these three kinds alone
type Annotator string

const (
	AnnotatorHuman Annotator = "HUMAN" // a person
	AnnotatorCode  Annotator = "CODE"  // a rule of nodloop
	AnnotatorLLM   Annotator = "LLM"   // a model
)

// The score of each label per evaluation
// A label left out has no score such as a withdraw or an inconclusive check
var scores = map[EvaluationName]map[string]float64{
	EvaluationVerdict:  {"approve": 1, "edit": 0, "reject": 0},
	EvaluationOutcome:  {"confirmed": 1, "refuted": 0},
	EvaluationDecision: {"approved": 1, "retired": 0},
}

// The label of a question nobody answered after its second round
const labelNoAnswer = "no_answer"

// One verdict, outcome, decision, judgment, intervention or decide as an evaluation carrier span and a log record
type Evaluation struct {
	Name      EvaluationName
	Time      time.Time
	SessionID string
	// The verdict, the result, the decision status, the choice, or the outcome of the judge and else the action of a decide
	Label string
	// A reason, a note or a confirmed cause sent only with content
	Explanation string
	// The edited output of a verdict sent only with content
	Edited     string
	ReasonCode string
	// Who judged: the reviewer of a verdict or an outcome, the person of an answer or an intervention, the approver of a decision
	Reviewer  string
	Annotator Annotator
	// What proved the caller a person on a verdict, an answer or an intervention
	Proof string
	// The span of the evaluation itself from its key
	Span SpanRef
	// The span it evaluates
	// The carrier links it and the log record names it as its parent
	// Zero when the records name none
	Target SpanRef
	// The version a decision, a judgment, an intervention or a decide is about
	Candidate *Ref
	// The candidate text sent only with content
	Content string
	// Nil on a verdict and an outcome
	Decision *Decision
}

// The enums and numbers a tool filters and charts a decision, a judgment, an intervention or a decide by
// None of them holds text and an empty one is left out
type Decision struct {
	// push, manual or command
	Source string
	// The kind of the question an answer answered
	QuestionKind string
	// The tier of the decide behind it and nil where none stands behind it
	Tier       *int
	ChangeKind string
	RuleCode   string
	// The judge bin of the decide behind it
	Bin    string
	Choice string
	// Whether an answer reverses what curate carried out or differs from what the judge predicted
	Overturn     *bool
	RetireReason string
	// The cycle of the draw and the probability it was drawn with
	// P is nil for a record the person asked for
	Cycle string
	P     *float64
	// The action and the authority of an intervention or an answer
	Action    string
	Authority string
	// The knob a knob intervention or its reverse sets with the value it takes and the one it had
	Knob   string
	Value  *float64
	Before *float64
	// The share of the judge members that conform
	// The score of a decide and of a judge
	Vote *float64
}

// The keys of the GenAI evaluation and the OpenInference annotation under prefix
// 1. the GenAI evaluation has no key for who judged so the annotation carries the annotator kind
// 2. the explanation and the edited output go only with content since a person wrote them
func (e Evaluation) attributes(content bool, prefix string) []attribute {
	attrs := []attribute{
		str("gen_ai.evaluation.name", string(e.Name)), str("gen_ai.evaluation.score.label", e.Label),
		str(prefix+"name", string(e.Name)), str(prefix+"label", e.Label), str(prefix+"annotator_kind", string(e.Annotator)),
	}
	if score, ok := e.score(); ok {
		attrs = append(attrs, double("gen_ai.evaluation.score.value", score), double(prefix+"score", score))
	}
	if content && e.Explanation != "" {
		attrs = append(attrs, str("gen_ai.evaluation.explanation", e.Explanation), str(prefix+"explanation", e.Explanation))
	}
	if content {
		attrs = appendStr(attrs, "nodloop.edited", e.Edited)
		attrs = appendStr(attrs, "nodloop.knowledge.content", e.Content)
	}
	attrs = appendStr(attrs, "nodloop.reason_code", e.ReasonCode)
	reviewer := "nodloop.reviewer"
	if e.Name == EvaluationDecision {
		reviewer = "nodloop.approver"
	}
	attrs = appendStr(attrs, reviewer, e.Reviewer)
	attrs = appendStr(attrs, "nodloop.proof", e.Proof)
	if e.Candidate != nil {
		attrs = append(attrs, str("nodloop.knowledge.candidate", e.Candidate.ref()))
	}
	if e.Decision != nil {
		attrs = append(attrs, e.Decision.attributes()...)
	}
	return attrs
}

// The score of the evaluation and false where it has none
// 1. a judgment scores 1 when the person kept what was prepared or carried out and 0 when the answer overturned it
// 2. a decide and a judge score the share of the judge members that conform so a tier 0 rule has none
func (e Evaluation) score() (float64, bool) {
	d := cmp.Or(e.Decision, &Decision{})
	switch e.Name {
	case EvaluationJudgment:
		if e.Label == labelNoAnswer || d.Overturn == nil {
			return 0, false
		}
		return map[bool]float64{false: 1, true: 0}[*d.Overturn], true
	case EvaluationCurate, EvaluationJudge:
		if d.Vote == nil {
			return 0, false
		}
		return *d.Vote, true
	}
	score, ok := scores[e.Name][e.Label]
	return score, ok
}

func (d Decision) attributes() []attribute {
	attrs := appendStr(nil, "nodloop.source", d.Source)
	attrs = appendStr(attrs, "nodloop.question.kind", d.QuestionKind)
	if d.Tier != nil {
		attrs = append(attrs, integer("nodloop.tier", int64(*d.Tier)))
	}
	attrs = appendStr(attrs, "nodloop.change.kind", d.ChangeKind)
	attrs = appendStr(attrs, "nodloop.rule.code", d.RuleCode)
	attrs = appendStr(attrs, "nodloop.judge.bin", d.Bin)
	attrs = appendStr(attrs, "nodloop.choice", d.Choice)
	if d.Overturn != nil {
		attrs = append(attrs, boolean("nodloop.overturn", *d.Overturn))
	}
	attrs = appendStr(attrs, "nodloop.retire.reason", d.RetireReason)
	attrs = appendStr(attrs, "nodloop.cycle", d.Cycle)
	if d.P != nil {
		attrs = append(attrs, double("nodloop.draw.p", *d.P))
	}
	attrs = appendStr(attrs, "nodloop.action", d.Action)
	attrs = appendStr(attrs, "nodloop.authority", d.Authority)
	attrs = appendStr(attrs, "nodloop.knob", d.Knob)
	if d.Value != nil {
		attrs = append(attrs, double("nodloop.knob.value", *d.Value))
	}
	if d.Before != nil {
		attrs = append(attrs, double("nodloop.knob.before", *d.Before))
	}
	return attrs
}

// The evaluation as a log based event of the GenAI conventions parented to the span it evaluates
// 1. each log record carries one annotation so its keys sit under annotations.0
// 2. log.record.uid derives from the evaluation so a log backend can tell a resend
func (e Evaluation) record(content bool) logRecord {
	attrs := append(e.attributes(content, "annotations.0.annotation."), str("log.record.uid", e.Span.uid()))
	attrs = append(attrs, session(e.SessionID)...)
	return logRecord{
		Time: nanos(e.Time), Severity: severityInfo, EventName: evaluationEvent,
		TraceID: e.Target.TraceID, SpanID: e.Target.SpanID, Attributes: attrs,
	}
}

// What the judge predicted as an evaluation whose log record the span of its classify trace parents
// The GenAI event has no key for who judged so a tool that reads only the event still sees the prediction beside the answer
func (c Call) judged() Evaluation {
	span, votes := TraceSpan(c.ID), c.Judge.Votes
	return Evaluation{
		Name: EvaluationJudge, Time: c.Time, SessionID: c.SessionID, Label: c.Judge.Outcome, Annotator: AnnotatorLLM,
		Span: span, Target: span, Decision: &Decision{Bin: c.Judge.Bin, Vote: &votes},
	}
}
