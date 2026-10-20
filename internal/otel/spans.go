package otel

import (
	"encoding/json"
	"maps"
	"slices"
	"strconv"
)

// Span kinds of the OTLP data model
const kindInternal = 1

// The status code of a span that failed
const statusError = 2

// The keys a span gets in every tool
const (
	keyOperation  = "gen_ai.operation.name"
	keyAgent      = "gen_ai.agent.name"
	keyKind       = "openinference.span.kind"
	keySession    = "session.id"
	keyCoversa    = "gen_ai.conversation.id"
	operationTurn = "invoke_agent"
)

// The session under the key of each convention since every receiver reads only its own
// No session leaves both out since the conventions forbid a made up one
func session(id string) []attribute {
	if id == "" {
		return nil
	}
	return []attribute{str(keySession, id), str(keyCoversa, id)}
}

// The run as the invoke_agent span of its turn
// 1. it starts its duration before the record and a run of unknown duration has a span of no length
// 2. the model goes along only when the run recorded one since a session may switch models
// 3. the output goes along only with content since it may hold what a person wrote
func (r Run) span(content bool) span {
	ids := RunSpan(r.ID)
	s := span{
		TraceID: ids.TraceID, SpanID: ids.SpanID, Name: operationTurn + " " + r.Producer, Kind: kindInternal,
		Start: nanos(r.Time.Add(-r.Duration)), End: nanos(r.Time),
		Attributes: []attribute{
			str(keyOperation, operationTurn), str(keyAgent, r.Producer), str(keyKind, "AGENT"), str("nodloop.run.id", r.ID),
		},
	}
	s.Attributes = append(s.Attributes, session(r.SessionID)...)
	s.Attributes = appendStr(s.Attributes, "gen_ai.request.model", r.Model)
	s.Attributes = appendStr(s.Attributes, "nodloop.subject", r.Subject)
	s.Attributes = appendStr(s.Attributes, "nodloop.plugin.version", r.Plugin)
	for _, k := range slices.Sorted(maps.Keys(r.Labels)) {
		s.Attributes = append(s.Attributes, strs("nodloop.label."+k, r.Labels[k]))
	}
	s.Attributes = append(s.Attributes, r.holdout()...)
	if content && len(r.Output) > 0 {
		s.Attributes = append(s.Attributes, str("output.value", text(r.Output)), str("output.mime_type", "text/plain"))
	}
	if r.Error != "" {
		s.failed("nodloop.run_failed", r.Error)
	}
	return s
}

// The holdout arm and the items of a run as span attributes
// 1. the GenAI conventions name no experiment arm so the keys stay under nodloop
// 2. a run with no item to receive has no arm and a run with items that went through no draw is undrawn as report effect counts it
func (r Run) holdout() []attribute {
	var out []attribute
	if len(r.Applied) > 0 {
		out = append(out, strs("nodloop.knowledge.applied", refs(r.Applied)))
	}
	if len(r.Withheld) > 0 {
		out = append(out, strs("nodloop.knowledge.withheld", refs(r.Withheld)))
	}
	if r.Drawn {
		out = append(out, str("nodloop.holdout.unit", r.Unit), double("nodloop.holdout.share", r.Share))
	}
	switch {
	case len(r.Applied) == 0 && len(r.Withheld) == 0:
		return out
	case !r.Drawn:
		return append(out, str("nodloop.holdout.arm", "undrawn"))
	case len(r.Applied) > 0:
		return append(out, str("nodloop.holdout.arm", "applied"))
	}
	return append(out, str("nodloop.holdout.arm", "withheld"))
}

// The evaluation as a carrier span of no length with one link to what it evaluates
// The OpenInference annotation spec takes feedback given after the fact on such a span
func (e Evaluation) span(content bool) span {
	s := span{
		TraceID: e.Span.TraceID, SpanID: e.Span.SpanID, Name: "evaluate " + string(e.Name), Kind: kindInternal,
		Start: nanos(e.Time), End: nanos(e.Time),
		Attributes: append(append([]attribute{str(keyKind, "EVALUATOR")}, session(e.SessionID)...), e.attributes(content, "annotation.")...),
	}
	if !e.Target.IsZero() {
		s.Links = []link{{TraceID: e.Target.TraceID, SpanID: e.Target.SpanID}}
	}
	return s
}

// Marks the span failed with the error type and the error as its status message
func (s *span) failed(errorType, message string) {
	s.Attributes = append(s.Attributes, str("error.type", errorType))
	s.Status = &status{Code: statusError, Message: message}
}

func (r Ref) ref() string {
	return r.ID + ":" + strconv.Itoa(r.Version)
}

// Each ref as id:version
func refs(rs []Ref) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.ref())
	}
	return out
}

// A JSON string output as its text and any other JSON as written
func text(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

func appendStr(attrs []attribute, key, s string) []attribute {
	if s == "" {
		return attrs
	}
	return append(attrs, str(key, s))
}
