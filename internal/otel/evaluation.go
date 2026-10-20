package otel

// The event name of the GenAI conventions for an evaluation
const evaluationEvent = "gen_ai.evaluation.result"

// Severity INFO of the OTLP log data model
const severityInfo = 9

// The OpenInference annotator kinds
const (
	annotatorHuman = "HUMAN" // a person judged
	annotatorLLM   = "LLM"   // the conversation inferred the verdict from the person's words
)

// The score of each label per evaluation
// A label left out has no score such as a withdraw or an inconclusive check
var scores = map[EvaluationName]map[string]float64{
	EvaluationVerdict: {"approve": 1, "edit": 0, "reject": 0},
	EvaluationOutcome: {"confirmed": 1, "refuted": 0},
	EvaluationReview:  {"approved": 1, "retired": 0},
}

// The keys of the GenAI evaluation and the OpenInference annotation under prefix
// 1. the GenAI evaluation has no key for who judged so the annotation carries the annotator kind
// 2. the explanation and the edited output go only with content since a person wrote them
func (e Evaluation) attributes(content bool, prefix string) []attribute {
	attrs := []attribute{
		str("gen_ai.evaluation.name", string(e.Name)), str("gen_ai.evaluation.score.label", e.Label),
		str(prefix+"name", string(e.Name)), str(prefix+"label", e.Label), str(prefix+"annotator_kind", e.annotator()),
	}
	if score, ok := scores[e.Name][e.Label]; ok {
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
	if e.Name == EvaluationReview {
		reviewer = "nodloop.approver"
	}
	attrs = appendStr(attrs, reviewer, e.Reviewer)
	if e.Candidate != nil {
		attrs = append(attrs, str("nodloop.knowledge.candidate", e.Candidate.ref()))
	}
	return attrs
}

func (e Evaluation) annotator() string {
	if e.Human {
		return annotatorHuman
	}
	return annotatorLLM
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
