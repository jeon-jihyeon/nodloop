package otel_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"maps"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/otel"
)

var at = time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)

func nanos(t time.Time) string {
	return strconv.FormatInt(t.UnixNano(), 10)
}

// The requests a collector received
type collector struct {
	mu     sync.Mutex
	status map[string]int
	paths  []string
	types  []string
	raw    [][]byte
	bodies []map[string]any
}

func (c *collector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var decoded map[string]any
	_ = json.Unmarshal(body, &decoded)
	c.mu.Lock()
	c.paths = append(c.paths, r.URL.Path)
	c.types = append(c.types, r.Header.Get("Content-Type"))
	c.raw = append(c.raw, body)
	c.bodies = append(c.bodies, decoded)
	status := c.status[r.URL.Path]
	c.mu.Unlock()
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
}

// One span with its plain attributes
type gotSpan struct {
	traceID, spanID, parent, name string
	kind                          float64
	start, end                    string
	attrs                         map[string]any
	links                         []string
	status                        float64
	// The status message of a failed span
	message string
}

// The spans of every request by name
func (c *collector) spans() map[string]gotSpan {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string]gotSpan{}
	for _, b := range c.bodies {
		rss, _ := b["resourceSpans"].([]any)
		for _, rs := range rss {
			for _, ss := range rs.(map[string]any)["scopeSpans"].([]any) {
				for _, s := range ss.(map[string]any)["spans"].([]any) {
					m := s.(map[string]any)
					g := gotSpan{
						traceID: m["traceId"].(string), spanID: m["spanId"].(string), name: m["name"].(string), kind: m["kind"].(float64),
						start: m["startTimeUnixNano"].(string), end: m["endTimeUnixNano"].(string), attrs: attrs(m),
					}
					g.parent, _ = m["parentSpanId"].(string)
					links, _ := m["links"].([]any)
					for _, l := range links {
						g.links = append(g.links, l.(map[string]any)["spanId"].(string))
					}
					if st, ok := m["status"].(map[string]any); ok {
						g.status = st["code"].(float64)
						g.message, _ = st["message"].(string)
					}
					out[g.name] = g
				}
			}
		}
	}
	return out
}

// The log records of every request in order
func (c *collector) logs() []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []map[string]any
	for _, b := range c.bodies {
		rls, _ := b["resourceLogs"].([]any)
		for _, rl := range rls {
			for _, sl := range rl.(map[string]any)["scopeLogs"].([]any) {
				for _, l := range sl.(map[string]any)["logRecords"].([]any) {
					out = append(out, l.(map[string]any))
				}
			}
		}
	}
	return out
}

// The attributes of a span or a log record by key with their plain values
func attrs(m map[string]any) map[string]any {
	out := map[string]any{}
	list, _ := m["attributes"].([]any)
	for _, a := range list {
		kv := a.(map[string]any)
		for kind, v := range kv["value"].(map[string]any) {
			if kind == "arrayValue" {
				var values []any
				for _, item := range v.(map[string]any)["values"].([]any) {
					values = append(values, item.(map[string]any)["stringValue"])
				}
				v = values
			}
			out[kv["key"].(string)] = v
		}
	}
	return out
}

var (
	lesson   = otel.Ref{ID: "lesson", Version: 1}
	cycle    = "2026-10-05"
	pushed   = 1.0
	audited  = 0.05
	judgedP  = 0.15
	kept     = false
	reversed = true
	tier0    = 0
	tier1    = 1
	tier2    = 2
	everyone = 1.0
	twoThird = 2.0 / 3
)

// The question the stop pushed after run r1 about the lesson
var asked = otel.Question{
	ID: "x1", Time: at.Add(2 * time.Minute), SessionID: "s1", Run: "r1", Kind: "direction", Source: "push", Round: 1, Candidate: &lesson,
	Cycle: cycle, P: &pushed, Content: "Use git -C",
}

// The enums of the answer of keep to the question about a tier 2 add the judge found in b2
func answered() *otel.Decision {
	return &otel.Decision{
		Source: "push", QuestionKind: "direction", Tier: &tier2, ChangeKind: "add", RuleCode: "kind_closed", Bin: "b2", Choice: "keep",
		Overturn: &kept, Cycle: cycle, P: &pushed, Authority: "person",
	}
}

// One record of every kind but a judge
func batch() otel.Batch {
	question := otel.QuestionSpan("r1", "x1")
	return otel.Batch{
		Runs: []otel.Run{{
			ID: "r1", Time: at.Add(90 * time.Second), Duration: 90 * time.Second, Producer: "session", SessionID: "s1",
			Labels: map[string][]string{"repo": {"nodloop"}}, Withheld: []otel.Ref{{ID: "git-c", Version: 2}}, Drawn: true, Unit: "u1", Share: 0.1,
			Plugin: "0.9.0", Output: json.RawMessage(`"run git -C repo status"`),
		}},
		Questions: []otel.Question{asked},
		Evaluations: []otel.Evaluation{
			{
				Name: otel.EvaluationVerdict, Time: at.Add(time.Minute), SessionID: "s1", Label: "reject", Explanation: "wrong flag",
				ReasonCode: "fact", Reviewer: "session", Annotator: otel.AnnotatorLLM, Span: otel.VerdictSpan("r1", at.Add(time.Minute)),
				Target: otel.RunSpan("r1"),
			},
			{
				Name: otel.EvaluationOutcome, Time: at.Add(time.Hour), SessionID: "s1", Label: "inconclusive", Reviewer: "ann",
				Annotator: otel.AnnotatorHuman, Span: otel.OutcomeSpan("r1", at.Add(time.Hour)), Target: otel.RunSpan("r1"),
			},
			{
				Name: otel.EvaluationDecision, Time: at.Add(3 * time.Minute), SessionID: "s1", Label: "approved", Reviewer: "ann",
				Annotator: otel.AnnotatorHuman, Span: otel.DecisionSpan(lesson, "approved", at.Add(3*time.Minute)), Target: question,
				Candidate: &lesson, Content: "Use git -C", Decision: answered(),
			},
			{
				Name: otel.EvaluationJudgment, Time: at.Add(3 * time.Minute), SessionID: "s1", Label: "keep", Reviewer: "ann",
				Annotator: otel.AnnotatorHuman, Proof: "reply", Span: otel.TraceSpan("a1"), Target: question, Candidate: &lesson, Decision: answered(),
			},
		},
		Calls: []otel.Call{
			{
				ID: "e1", Name: "extract", Time: at.Add(5 * time.Minute), Duration: 30 * time.Second, SessionID: "s1", Model: "claude-haiku-4-5",
				Usage:      otel.Usage{Input: 10, Output: 4, Thinking: 1, CacheRead: 100, CacheWrite: 50, CostUSD: 0.25},
				Conclusion: "proposed", Candidate: &lesson, Links: []otel.SpanRef{otel.RunSpan("r1"), otel.VerdictSpan("r1", at.Add(time.Minute))},
			},
			{ID: "c1", Name: "classify", Time: at.Add(6 * time.Minute), Duration: time.Second, Point: "critic", Error: "endpoint down"},
		},
	}
}

func exporter(srv *httptest.Server, logs bool, content bool) *otel.Exporter {
	cfg := otel.Config{
		Traces: srv.URL + "/v1/traces", Protocol: otel.ProtocolJSON, Version: "0.9.0", Client: srv.Client(), Content: content,
		Headers: map[string]string{"Authorization": "Basic k"},
	}
	if logs {
		cfg.Logs = srv.URL + "/v1/logs"
	}
	return otel.New(cfg)
}

// Every record becomes its spans with the timing, the nesting and the links of what it judged
func TestExporterExportSpans(t *testing.T) {
	c := &collector{}
	srv := httptest.NewServer(c)
	t.Cleanup(srv.Close)

	sent, err := exporter(srv, false, false).Export(context.Background(), batch())

	require.NoError(t, err)
	assert.Equal(t, otel.Sent{Runs: 1, Questions: 1, Verdicts: 1, Outcomes: 1, Decisions: 1, Judgments: 1, Calls: 2}, sent)
	spans := c.spans()
	run, question := otel.RunSpan("r1"), otel.QuestionSpan("r1", "x1")
	turn := spans["invoke_agent session"]
	assert.Equal(t, gotSpan{
		traceID: run.TraceID, spanID: run.SpanID, name: "invoke_agent session", kind: 1, start: nanos(at), end: nanos(at.Add(90 * time.Second)),
		attrs: map[string]any{
			"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": "session", "openinference.span.kind": "AGENT", "nodloop.run.id": "r1",
			"session.id": "s1", "gen_ai.conversation.id": "s1", "nodloop.plugin.version": "0.9.0", "nodloop.label.repo": []any{"nodloop"},
			"nodloop.knowledge.withheld": []any{"git-c:2"}, "nodloop.holdout.unit": "u1", "nodloop.holdout.share": 0.1, "nodloop.holdout.arm": "withheld",
		},
	}, turn, "the output stays home without content")
	assert.Equal(t, gotSpan{
		traceID: run.TraceID, spanID: question.SpanID, parent: run.SpanID, name: "nodloop.question direction", kind: 1,
		start: nanos(at.Add(2 * time.Minute)), end: nanos(at.Add(2 * time.Minute)),
		attrs: map[string]any{
			"openinference.span.kind": "CHAIN", "nodloop.question.kind": "direction", "session.id": "s1", "gen_ai.conversation.id": "s1",
			"nodloop.question.source": "push", "nodloop.question.round": "1", "nodloop.knowledge.candidate": "lesson:1",
			"nodloop.cycle": cycle, "nodloop.draw.p": 1.0,
		},
	}, spans["nodloop.question direction"], "the candidate text stays home without content")
	verdict := spans["evaluate nodloop.verdict"]
	assert.Equal(t, []string{run.SpanID}, verdict.links)
	assert.Equal(t, evaluated("nodloop.verdict", "reject", "LLM", 0.0, map[string]any{"nodloop.reason_code": "fact", "nodloop.reviewer": "session"}),
		verdict.attrs, "the reason stays home without content")
	assert.NotContains(t, spans["evaluate nodloop.outcome"].attrs, "gen_ai.evaluation.score.value", "an inconclusive check has no score")
	assert.Equal(t, "HUMAN", spans["evaluate nodloop.outcome"].attrs["annotation.annotator_kind"])
	assert.Equal(t, []string{question.SpanID}, spans["evaluate nodloop.decision"].links)
	assert.Equal(t, []string{question.SpanID}, spans["evaluate nodloop.judgment"].links)
	extract, chat := spans["invoke_agent nodloop.extract"], spans["chat claude-haiku-4-5"]
	assert.Equal(t, []string{nanos(at.Add(4*time.Minute + 30*time.Second)), nanos(at.Add(5 * time.Minute))}, []string{extract.start, extract.end})
	assert.Equal(t, []string{run.SpanID, otel.VerdictSpan("r1", at.Add(time.Minute)).SpanID}, extract.links)
	assert.Equal(t, "proposed", extract.attrs["nodloop.extract.conclusion"])
	assert.Equal(t, []any{extract.traceID, extract.spanID, 3.0, extract.start, extract.end}, []any{chat.traceID, chat.parent, chat.kind, chat.start, chat.end})
	assert.Equal(t, map[string]any{
		"gen_ai.operation.name": "chat", "openinference.span.kind": "LLM", "session.id": "s1", "gen_ai.conversation.id": "s1",
		"gen_ai.provider.name": "anthropic", "gen_ai.request.model": "claude-haiku-4-5", "gen_ai.response.model": "claude-haiku-4-5",
		"gen_ai.usage.input_tokens": "160", "gen_ai.usage.output_tokens": "4", "gen_ai.usage.cache_read.input_tokens": "100",
		"gen_ai.usage.cache_write.input_tokens": "50", "gen_ai.usage.reasoning.output_tokens": "1", "llm.cost.total": 0.25,
	}, chat.attrs, "input tokens count the cache read and write")
	classify := spans["invoke_agent nodloop.classify"]
	assert.Equal(t, "critic", classify.attrs["nodloop.classify.point"])
	assert.Equal(t, "nodloop.classify_failed", classify.attrs["error.type"])
	assert.Equal(t, 2.0, classify.status)
	assert.Empty(t, classify.message, "the error stays home without content since it may quote a model")
	assert.NotContains(t, classify.attrs, "nodloop.judge.outcome", "a classify of another point carries nothing of the judge")
	assert.NotContains(t, spans, "chat", "a step that called no model has no inference span")
	assert.Len(t, spans, 9)
	assert.Equal(t, []string{"/v1/traces"}, c.paths)
}

// The attributes of an evaluation span in session s1 with the keys of its kind
// A nil score leaves the score keys out
func evaluated(name, label, annotator string, score any, keys map[string]any) map[string]any {
	out := map[string]any{
		"openinference.span.kind": "EVALUATOR", "session.id": "s1", "gen_ai.conversation.id": "s1",
		"gen_ai.evaluation.name": name, "gen_ai.evaluation.score.label": label,
		"annotation.name": name, "annotation.label": label, "annotation.annotator_kind": annotator,
	}
	if score != nil {
		out["gen_ai.evaluation.score.value"], out["annotation.score"] = score, score
	}
	maps.Copy(out, keys)
	return out
}

// Each decision, judgment, intervention and decide is an evaluation span with one link and a log record of who judged
// 1. a person is HUMAN, a rule of nodloop CODE and a model LLM
// 2. the enums go along and no text without content
// 3. the log record names the span the evaluation links and the uid of its key
func TestExporterExportEvaluations(t *testing.T) {
	question, judge, run, extract := otel.QuestionSpan("r1", "x1"), otel.TraceSpan("j1"), otel.RunSpan("r1"), otel.TraceSpan("e1")
	keep := map[string]any{
		"nodloop.source": "push", "nodloop.question.kind": "direction", "nodloop.tier": "2", "nodloop.change.kind": "add",
		"nodloop.rule.code": "kind_closed", "nodloop.judge.bin": "b2", "nodloop.choice": "keep", "nodloop.overturn": false,
		"nodloop.cycle": cycle, "nodloop.draw.p": 1.0, "nodloop.authority": "person", "nodloop.knowledge.candidate": "lesson:1",
	}
	with := func(base map[string]any, keys map[string]any) map[string]any {
		out := maps.Clone(base)
		maps.Copy(out, keys)
		return out
	}
	dropped := &otel.Decision{
		Source: "push", Tier: &tier0, ChangeKind: "drop", RuleCode: "temp_scope", RetireReason: "temp_scope", Cycle: cycle, P: &audited,
	}
	drop := map[string]any{
		"nodloop.source": "push", "nodloop.tier": "0", "nodloop.change.kind": "drop", "nodloop.rule.code": "temp_scope",
		"nodloop.retire.reason": "temp_scope", "nodloop.cycle": cycle, "nodloop.draw.p": 0.05, "nodloop.knowledge.candidate": "lesson:1",
	}
	overturned, unanswered := answered(), answered()
	overturned.QuestionKind, overturned.Choice, overturned.Overturn = "audit", "undo", &reversed
	unanswered.Choice = "no_answer"
	type want struct {
		attrs map[string]any
		links []string
		// The trace and span id the log record names and its uid
		log []any
	}
	tcs := []struct {
		name string
		args otel.Evaluation
		want want
	}{
		{"a person's decision taken from an answer is HUMAN and links the question", otel.Evaluation{
			Name: otel.EvaluationDecision, Label: "approved", Reviewer: "ann", Annotator: otel.AnnotatorHuman, Target: question,
			Span: otel.DecisionSpan(lesson, "approved", at), Candidate: &lesson, Content: "Use git -C", Decision: answered(),
		}, want{evaluated("nodloop.decision", "approved", "HUMAN", 1.0, with(keep, map[string]any{"nodloop.approver": "ann"})),
			[]string{question.SpanID}, []any{question.TraceID, question.SpanID, otel.DecisionSpan(lesson, "approved", at).TraceID}}},
		{"a decision of a tier 0 rule is CODE with its retire reason and links its run", otel.Evaluation{
			Name: otel.EvaluationDecision, Label: "retired", Reviewer: "nodloop", Annotator: otel.AnnotatorCode, Target: run,
			Span: otel.DecisionSpan(lesson, "retired", at), Candidate: &lesson, Decision: dropped,
		}, want{evaluated("nodloop.decision", "retired", "CODE", 0.0, with(drop, map[string]any{"nodloop.approver": "nodloop"})),
			[]string{run.SpanID}, []any{run.TraceID, run.SpanID, otel.DecisionSpan(lesson, "retired", at).TraceID}}},
		{"a decision of nodloop after a judge is LLM and links the judge", otel.Evaluation{
			Name: otel.EvaluationDecision, Label: "approved", Reviewer: "nodloop", Annotator: otel.AnnotatorLLM, Target: judge,
			Span: otel.DecisionSpan(lesson, "approved", at), Candidate: &lesson,
			Decision: &otel.Decision{Source: "push", Tier: &tier1, ChangeKind: "add", Bin: "b1", Cycle: cycle, P: &judgedP, Vote: &everyone},
		}, want{evaluated("nodloop.decision", "approved", "LLM", 1.0, map[string]any{
			"nodloop.approver": "nodloop", "nodloop.source": "push", "nodloop.tier": "1", "nodloop.change.kind": "add", "nodloop.judge.bin": "b1",
			"nodloop.cycle": cycle, "nodloop.draw.p": 0.15, "nodloop.knowledge.candidate": "lesson:1",
		}), []string{judge.SpanID}, []any{judge.TraceID, judge.SpanID, otel.DecisionSpan(lesson, "approved", at).TraceID}}},
		{"an answer that kept what was prepared scores 1 with its proof", otel.Evaluation{
			Name: otel.EvaluationJudgment, Label: "keep", Reviewer: "ann", Annotator: otel.AnnotatorHuman, Proof: "reply", Target: question,
			Span: otel.TraceSpan("a1"), Candidate: &lesson, Decision: answered(),
		}, want{evaluated("nodloop.judgment", "keep", "HUMAN", 1.0, with(keep, map[string]any{"nodloop.reviewer": "ann", "nodloop.proof": "reply"})),
			[]string{question.SpanID}, []any{question.TraceID, question.SpanID, otel.TraceSpan("a1").TraceID}}},
		{"an answer that overturned an audit scores 0", otel.Evaluation{
			Name: otel.EvaluationJudgment, Label: "undo", Reviewer: "ann", Annotator: otel.AnnotatorHuman, Proof: "reply", Target: question,
			Span: otel.TraceSpan("a1"), Candidate: &lesson, Decision: overturned,
		}, want{evaluated("nodloop.judgment", "undo", "HUMAN", 0.0, with(keep, map[string]any{
			"nodloop.reviewer": "ann", "nodloop.proof": "reply", "nodloop.question.kind": "audit", "nodloop.choice": "undo", "nodloop.overturn": true,
		})), []string{question.SpanID}, []any{question.TraceID, question.SpanID, otel.TraceSpan("a1").TraceID}}},
		{"no answer is CODE with no score", otel.Evaluation{
			Name: otel.EvaluationJudgment, Label: "no_answer", Annotator: otel.AnnotatorCode, Proof: "none", Target: question,
			Span: otel.TraceSpan("a1"), Candidate: &lesson, Decision: unanswered,
		}, want{evaluated("nodloop.judgment", "no_answer", "CODE", nil, with(keep, map[string]any{"nodloop.proof": "none", "nodloop.choice": "no_answer"})),
			[]string{question.SpanID}, []any{question.TraceID, question.SpanID, otel.TraceSpan("a1").TraceID}}},
		{"a manual intervention of a person names its action, authority, proof and source with no score", otel.Evaluation{
			Name: otel.EvaluationIntervention, Label: "keep", Reviewer: "ann", Annotator: otel.AnnotatorHuman, Proof: "elicitation",
			Target: extract, Span: otel.TraceSpan("i1"), Candidate: &lesson,
			Decision: &otel.Decision{Source: "manual", Choice: "keep", Action: "judge", Authority: "person"},
		}, want{evaluated("nodloop.intervention", "keep", "HUMAN", nil, map[string]any{
			"nodloop.reviewer": "ann", "nodloop.proof": "elicitation", "nodloop.knowledge.candidate": "lesson:1", "nodloop.source": "manual",
			"nodloop.choice": "keep", "nodloop.action": "judge", "nodloop.authority": "person",
		}), []string{extract.SpanID}, []any{extract.TraceID, extract.SpanID, otel.TraceSpan("i1").TraceID}}},
		{"a knob intervention names the knob with the value it takes and the one it had", otel.Evaluation{
			Name: otel.EvaluationIntervention, Label: "knob", Reviewer: "ann", Annotator: otel.AnnotatorHuman, Proof: "terminal",
			Span: otel.TraceSpan("i2"), Decision: &otel.Decision{Source: "command", Action: "knob", Authority: "person", Knob: "audit_tier1", Value: &twoThird, Before: &judgedP},
		}, want{evaluated("nodloop.intervention", "knob", "HUMAN", nil, map[string]any{
			"nodloop.reviewer": "ann", "nodloop.proof": "terminal", "nodloop.source": "command", "nodloop.action": "knob", "nodloop.authority": "person",
			"nodloop.knob": "audit_tier1", "nodloop.knob.value": twoThird, "nodloop.knob.before": 0.15,
		}), nil, []any{nil, nil, otel.TraceSpan("i2").TraceID}}},
		{"a decide of a tier 0 rule is CODE with no score and links its run", otel.Evaluation{
			Name: otel.EvaluationCurate, Label: "drop", Annotator: otel.AnnotatorCode, Target: run, Span: otel.TraceSpan("d1"),
			Candidate: &lesson, Decision: dropped,
		}, want{evaluated("nodloop.curate", "drop", "CODE", nil, drop), []string{run.SpanID}, []any{run.TraceID, run.SpanID, otel.TraceSpan("d1").TraceID}}},
		{"a judged decide scores the share of the conforming members and links the judge", otel.Evaluation{
			Name: otel.EvaluationCurate, Label: "split", Annotator: otel.AnnotatorLLM, Target: judge, Span: otel.TraceSpan("d1"), Candidate: &lesson,
			Decision: &otel.Decision{Source: "push", Tier: &tier2, ChangeKind: "add", RuleCode: "judge_split", Bin: "b2", Cycle: cycle, P: &pushed, Vote: &twoThird},
		}, want{evaluated("nodloop.curate", "split", "LLM", twoThird, map[string]any{
			"nodloop.source": "push", "nodloop.tier": "2", "nodloop.change.kind": "add", "nodloop.rule.code": "judge_split", "nodloop.judge.bin": "b2",
			"nodloop.cycle": cycle, "nodloop.draw.p": 1.0, "nodloop.knowledge.candidate": "lesson:1",
		}), []string{judge.SpanID}, []any{judge.TraceID, judge.SpanID, otel.TraceSpan("d1").TraceID}}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := &collector{}
			srv := httptest.NewServer(c)
			t.Cleanup(srv.Close)
			ev := tc.args
			ev.Time, ev.SessionID = at, "s1"

			_, err := exporter(srv, true, false).Export(context.Background(), otel.Batch{Evaluations: []otel.Evaluation{ev}})

			require.NoError(t, err)
			spans, logs := c.spans(), c.logs()
			require.Len(t, spans, 1)
			require.Len(t, logs, 1)
			got := spans["evaluate "+string(ev.Name)]
			assert.Equal(t, []string{ev.Span.TraceID, ev.Span.SpanID, ""}, []string{got.traceID, got.spanID, got.parent})
			assert.Equal(t, tc.want.attrs, got.attrs)
			assert.Equal(t, tc.want.links, got.links)
			logged := attrs(logs[0])
			assert.Equal(t, tc.want.log, []any{logs[0]["traceId"], logs[0]["spanId"], logged["log.record.uid"]})
			assert.Equal(t, tc.want.attrs["annotation.annotator_kind"], logged["annotations.0.annotation.annotator_kind"])
			assert.Equal(t, tc.want.attrs["gen_ai.evaluation.score.value"], logged["gen_ai.evaluation.score.value"])
		})
	}
}

// A judge classify carries the model and effort of every member and what they found
// Its log record carries what the judge predicted
func TestExporterExportJudge(t *testing.T) {
	type want struct {
		attrs map[string]any
		// The trace and span id, the label, the score and the uid of the log record
		log []any
	}
	tcs := []struct {
		name string
		args otel.Judge
		want want
	}{
		{"a split of two conforming members of three", otel.Judge{
			Models: []string{"claude-sonnet-5-5", "claude-sonnet-5-5", "acme"}, Efforts: []string{"high", "high", ""}, Outcome: "split",
			Votes: twoThird, Bin: "b2",
		}, want{map[string]any{
			"gen_ai.request.model": []any{"claude-sonnet-5-5", "claude-sonnet-5-5", "acme"}, "nodloop.judge.effort": []any{"high", "high", ""},
			"nodloop.judge.outcome": "split", "nodloop.judge.votes": twoThird, "nodloop.judge.bin": "b2",
		}, []any{"split", twoThird}}},
		{"members that all failed have no bin", otel.Judge{
			Models: []string{"claude-sonnet-5-5"}, Efforts: []string{"high"}, Outcome: "split",
		}, want{map[string]any{
			"gen_ai.request.model": []any{"claude-sonnet-5-5"}, "nodloop.judge.effort": []any{"high"}, "nodloop.judge.outcome": "split",
			"nodloop.judge.votes": 0.0,
		}, []any{"split", 0.0}}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := &collector{}
			srv := httptest.NewServer(c)
			t.Cleanup(srv.Close)
			judge := tc.args
			call := otel.Call{ID: "j1", Name: "classify", Time: at, Duration: 20 * time.Second, SessionID: "s1", Point: "judge", Judge: &judge}

			sent, err := exporter(srv, true, false).Export(context.Background(), otel.Batch{Calls: []otel.Call{call}})

			require.NoError(t, err)
			assert.Equal(t, otel.Sent{Calls: 1, LogRecords: 1}, sent)
			spans, logs := c.spans(), c.logs()
			require.Len(t, spans, 1, "a panel names no model of its own so it has no inference span")
			require.Len(t, logs, 1)
			span := otel.TraceSpan("j1")
			got := spans["invoke_agent nodloop.classify"]
			assert.Equal(t, []string{span.TraceID, span.SpanID}, []string{got.traceID, got.spanID})
			want := map[string]any{
				"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": "nodloop.classify", "openinference.span.kind": "AGENT",
				"nodloop.trace.id": "j1", "session.id": "s1", "gen_ai.conversation.id": "s1", "nodloop.classify.point": "judge",
			}
			maps.Copy(want, tc.want.attrs)
			assert.Equal(t, want, got.attrs)
			logged := attrs(logs[0])
			assert.Equal(t, []any{span.TraceID, span.SpanID, "gen_ai.evaluation.result", nanos(at)},
				[]any{logs[0]["traceId"], logs[0]["spanId"], logs[0]["eventName"], logs[0]["timeUnixNano"]})
			assert.Equal(t, []any{"nodloop.judge", "LLM", span.TraceID, "s1"}, []any{
				logged["gen_ai.evaluation.name"], logged["annotations.0.annotation.annotator_kind"], logged["log.record.uid"], logged["session.id"],
			})
			assert.Equal(t, tc.want.log, []any{logged["gen_ai.evaluation.score.label"], logged["gen_ai.evaluation.score.value"]})
		})
	}
}

// Text goes along only with content
func TestExporterExportContent(t *testing.T) {
	c := &collector{}
	srv := httptest.NewServer(c)
	t.Cleanup(srv.Close)

	_, err := exporter(srv, false, true).Export(context.Background(), batch())

	require.NoError(t, err)
	spans := c.spans()
	assert.Equal(t, "run git -C repo status", spans["invoke_agent session"].attrs["output.value"])
	assert.Equal(t, "text/plain", spans["invoke_agent session"].attrs["output.mime_type"])
	assert.Equal(t, "wrong flag", spans["evaluate nodloop.verdict"].attrs["gen_ai.evaluation.explanation"])
	assert.Equal(t, "wrong flag", spans["evaluate nodloop.verdict"].attrs["annotation.explanation"])
	assert.Equal(t, "Use git -C", spans["nodloop.question direction"].attrs["nodloop.knowledge.content"])
	assert.Equal(t, "Use git -C", spans["evaluate nodloop.decision"].attrs["nodloop.knowledge.content"])
	assert.Equal(t, "endpoint down", spans["invoke_agent nodloop.classify"].message)
}

// A failed run is an ERROR span with its error type and its error as the message only with content
func TestExporterExportFailedRun(t *testing.T) {
	type want struct {
		status    float64
		errorType any
		message   string
	}
	tcs := []struct {
		name string
		// Whether text goes along
		args bool
		want want
	}{
		{"without content the error stays home", false, want{2, "nodloop.run_failed", ""}},
		{"with content the error is the status message", true, want{2, "nodloop.run_failed", "claude: exit status 1"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := &collector{}
			srv := httptest.NewServer(c)
			t.Cleanup(srv.Close)
			b := otel.Batch{Runs: []otel.Run{{ID: "r1", Time: at, Producer: "session", Error: "claude: exit status 1"}}}

			_, err := exporter(srv, false, tc.args).Export(context.Background(), b)

			require.NoError(t, err)
			run := c.spans()["invoke_agent session"]
			assert.Equal(t, tc.want, want{run.status, run.attrs["error.type"], run.message})
		})
	}
}

// A question lies in the trace of the run it followed and else is the root of a trace of its own
// A notice asks about no version and a question the person asked for carries no probability
func TestExporterExportQuestion(t *testing.T) {
	type want struct {
		// The trace id, the span id and the parent
		ids   []string
		name  string
		attrs map[string]any
	}
	run := otel.RunSpan("r1")
	tcs := []struct {
		name string
		args otel.Question
		want want
	}{
		{"a pushed question under the turn of its run", asked, want{
			[]string{run.TraceID, otel.QuestionSpan("r1", "x1").SpanID, run.SpanID}, "nodloop.question direction", map[string]any{
				"openinference.span.kind": "CHAIN", "nodloop.question.kind": "direction", "session.id": "s1", "gen_ai.conversation.id": "s1",
				"nodloop.question.source": "push", "nodloop.question.round": "1", "nodloop.knowledge.candidate": "lesson:1",
				"nodloop.cycle": cycle, "nodloop.draw.p": 1.0,
			},
		}},
		{"a manual question without a run is a trace of its own with no probability", otel.Question{
			ID: "x2", Time: at, SessionID: "s1", Kind: "audit", Source: "manual", Round: 1, Candidate: &lesson, Cycle: cycle,
		}, want{
			[]string{otel.QuestionSpan("", "x2").TraceID, otel.QuestionSpan("", "x2").SpanID, ""}, "nodloop.question audit", map[string]any{
				"openinference.span.kind": "CHAIN", "nodloop.question.kind": "audit", "session.id": "s1", "gen_ai.conversation.id": "s1",
				"nodloop.question.source": "manual", "nodloop.question.round": "1", "nodloop.knowledge.candidate": "lesson:1", "nodloop.cycle": cycle,
			},
		}},
		{"a notice names no version", otel.Question{
			ID: "x3", Time: at, SessionID: "s1", Run: "r1", Kind: "notice", Source: "push", Round: 1, Cycle: cycle, P: &pushed,
		}, want{
			[]string{run.TraceID, otel.QuestionSpan("r1", "x3").SpanID, run.SpanID}, "nodloop.question notice", map[string]any{
				"openinference.span.kind": "CHAIN", "nodloop.question.kind": "notice", "session.id": "s1", "gen_ai.conversation.id": "s1",
				"nodloop.question.source": "push", "nodloop.question.round": "1", "nodloop.cycle": cycle, "nodloop.draw.p": 1.0,
			},
		}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := &collector{}
			srv := httptest.NewServer(c)
			t.Cleanup(srv.Close)

			sent, err := exporter(srv, false, false).Export(context.Background(), otel.Batch{Questions: []otel.Question{tc.args}})

			require.NoError(t, err)
			assert.Equal(t, otel.Sent{Questions: 1}, sent)
			got := c.spans()[tc.want.name]
			assert.Equal(t, tc.want.ids, []string{got.traceID, got.spanID, got.parent})
			assert.Equal(t, tc.want.attrs, got.attrs)
		})
	}
}

// A run is the span of its turn with the holdout arm of report effect
// 1. a run without a duration has a span of no length
// 2. the model goes only when the run recorded one and the output only with content
func TestExporterExportRun(t *testing.T) {
	gitC := []otel.Ref{{ID: "git-c", Version: 1}}
	turn := func(id string, keys map[string]any) map[string]any {
		out := map[string]any{
			"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": "bot", "openinference.span.kind": "AGENT", "nodloop.run.id": id,
		}
		maps.Copy(out, keys)
		return out
	}
	type args struct {
		run     otel.Run
		content bool
	}
	type want struct {
		start, end string
		attrs      map[string]any
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"a drawn run that applied items names the applied arm and the model it ran",
			args{otel.Run{
				ID: "r3", Time: at, Duration: 10 * time.Second, Producer: "bot", Model: "claude-haiku-4-5", Applied: gitC, Drawn: true, Unit: "u2", Share: 0.2,
			}, false},
			want{nanos(at.Add(-10 * time.Second)), nanos(at), turn("r3", map[string]any{
				"gen_ai.request.model": "claude-haiku-4-5", "nodloop.knowledge.applied": []any{"git-c:1"}, "nodloop.holdout.unit": "u2",
				"nodloop.holdout.share": 0.2, "nodloop.holdout.arm": "applied",
			})},
		},
		{
			"a run with items and no draw is undrawn",
			args{otel.Run{ID: "r4", Time: at, Producer: "bot", Applied: gitC}, false},
			want{nanos(at), nanos(at), turn("r4", map[string]any{"nodloop.knowledge.applied": []any{"git-c:1"}, "nodloop.holdout.arm": "undrawn"})},
		},
		{
			"a run with no item to receive has no arm and its JSON output goes as written with content",
			args{otel.Run{ID: "r5", Time: at, Producer: "bot", Output: json.RawMessage(`{"files":2}`)}, true},
			want{nanos(at), nanos(at), turn("r5", map[string]any{"output.value": `{"files":2}`, "output.mime_type": "text/plain"})},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := &collector{}
			srv := httptest.NewServer(c)
			t.Cleanup(srv.Close)

			sent, err := exporter(srv, false, tc.args.content).Export(context.Background(), otel.Batch{Runs: []otel.Run{tc.args.run}})

			require.NoError(t, err)
			assert.Equal(t, otel.Sent{Runs: 1}, sent)
			got := c.spans()["invoke_agent bot"]
			assert.Equal(t, tc.want, want{got.start, got.end, got.attrs})
		})
	}
}

// A value JSON cannot hold fails the export before any request goes out
func TestExporterExportFailsToEncode(t *testing.T) {
	c := &collector{}
	srv := httptest.NewServer(c)
	t.Cleanup(srv.Close)

	sent, err := exporter(srv, true, false).Export(context.Background(), otel.Batch{Runs: []otel.Run{{
		ID: "r1", Time: at, Producer: "bot", Withheld: []otel.Ref{{ID: "git-c", Version: 1}}, Drawn: true, Share: math.NaN(),
	}}})

	var unsupported *json.UnsupportedValueError
	assert.ErrorAs(t, err, &unsupported)
	assert.Equal(t, otel.Sent{}, sent)
	assert.Empty(t, c.paths)
}

// Every evaluation goes as a log record of the GenAI conventions parented to what it evaluates after every span
func TestExporterExportLogs(t *testing.T) {
	c := &collector{}
	srv := httptest.NewServer(c)
	t.Cleanup(srv.Close)

	sent, err := exporter(srv, true, false).Export(context.Background(), batch())

	require.NoError(t, err)
	assert.Equal(t, 4, sent.LogRecords)
	assert.Equal(t, []string{"/v1/traces", "/v1/logs"}, c.paths)
	logs := c.logs()
	require.Len(t, logs, 4)
	run := otel.RunSpan("r1")
	verdict := logs[0]
	assert.Equal(t, []any{"gen_ai.evaluation.result", 9.0, run.TraceID, run.SpanID, nanos(at.Add(time.Minute))},
		[]any{verdict["eventName"], verdict["severityNumber"], verdict["traceId"], verdict["spanId"], verdict["timeUnixNano"]})
	a := attrs(verdict)
	assert.Equal(t, otel.VerdictSpan("r1", at.Add(time.Minute)).TraceID, a["log.record.uid"])
	assert.Equal(t, "LLM", a["annotations.0.annotation.annotator_kind"])
	assert.Equal(t, "reject", a["annotations.0.annotation.label"])
	assert.Equal(t, "s1", a["session.id"])
	assert.Equal(t, "HUMAN", attrs(logs[1])["annotations.0.annotation.annotator_kind"])
	question := otel.QuestionSpan("r1", "x1")
	assert.Equal(t, []any{question.TraceID, question.SpanID}, []any{logs[2]["traceId"], logs[2]["spanId"]})
	assert.Equal(t, []any{question.TraceID, question.SpanID}, []any{logs[3]["traceId"], logs[3]["spanId"]})
	assert.Equal(t, false, attrs(logs[3])["nodloop.overturn"])
}

// Two exports of one batch send the same bytes so a collector keyed on ids never counts a record twice
func TestExporterExportIsStable(t *testing.T) {
	c := &collector{}
	srv := httptest.NewServer(c)
	t.Cleanup(srv.Close)
	e := exporter(srv, true, true)

	_, first := e.Export(context.Background(), batch())
	_, second := e.Export(context.Background(), batch())

	require.NoError(t, first)
	require.NoError(t, second)
	require.Len(t, c.raw, 4)
	assert.Equal(t, c.raw[0], c.raw[2])
	assert.Equal(t, c.raw[1], c.raw[3])
}

// A field of a protobuf message as the test reads it back
type field struct {
	num   int
	value []byte
	n     uint64
}

// The fields of one protobuf message in order
func fields(t *testing.T, b []byte) []field {
	t.Helper()
	var out []field
	for len(b) > 0 {
		key, n := uvarint(b)
		require.Positive(t, n)
		b = b[n:]
		f := field{num: int(key >> 3)}
		switch key & 7 {
		case 0:
			v, n := uvarint(b)
			f.n, b = v, b[n:]
		case 1:
			f.n, b = leUint64(b[:8]), b[8:]
		case 2:
			l, n := uvarint(b)
			f.value, b = b[n:n+int(l)], b[n+int(l):]
		default:
			t.Fatalf("wire type %d", key&7)
		}
		out = append(out, f)
	}
	return out
}

func uvarint(b []byte) (uint64, int) {
	var v uint64
	for i, c := range b {
		v |= uint64(c&0x7f) << (7 * i)
		if c < 0x80 {
			return v, i + 1
		}
	}
	return 0, 0
}

func leUint64(b []byte) uint64 {
	var v uint64
	for i := 7; i >= 0; i-- {
		v = v<<8 | uint64(b[i])
	}
	return v
}

// The first field of the number
func first(t *testing.T, fs []field, num int) field {
	t.Helper()
	for _, f := range fs {
		if f.num == num {
			return f
		}
	}
	t.Fatalf("no field %d", num)
	return field{}
}

// The AnyValue fields of the attribute of the key among the fields of a span or a log record
func valueOf(t *testing.T, fs []field, num int, key string) []field {
	t.Helper()
	for _, f := range fs {
		if f.num != num {
			continue
		}
		if kv := fields(t, f.value); string(first(t, kv, 1).value) == key {
			return fields(t, first(t, kv, 2).value)
		}
	}
	t.Fatalf("no attribute %s", key)
	return nil
}

// The protobuf body holds the ids as bytes, the times as fixed64 and the attributes of the OTLP proto files
// A false is written as its bool field so the oneof never reads as no value
func TestExporterExportProtobuf(t *testing.T) {
	c := &collector{}
	srv := httptest.NewServer(c)
	t.Cleanup(srv.Close)
	cfg := otel.Config{Traces: srv.URL + "/v1/traces", Logs: srv.URL + "/v1/logs", Protocol: otel.ProtocolProtobuf, Version: "0.9.0", Client: srv.Client()}
	b := batch()
	b.Questions, b.Calls, b.Evaluations = nil, nil, []otel.Evaluation{b.Evaluations[0], b.Evaluations[3]}

	_, err := otel.New(cfg).Export(context.Background(), b)

	require.NoError(t, err)
	assert.Equal(t, []string{"application/x-protobuf", "application/x-protobuf"}, c.types)
	resourceSpans := fields(t, first(t, fields(t, c.raw[0]), 1).value)
	resource := fields(t, first(t, resourceSpans, 1).value)
	service := fields(t, first(t, resource, 1).value)
	assert.Equal(t, "service.name", string(first(t, service, 1).value))
	assert.Equal(t, "nodloop", string(first(t, fields(t, first(t, service, 2).value), 1).value))
	scopeSpans := fields(t, first(t, resourceSpans, 2).value)
	assert.Equal(t, "nodloop", string(first(t, fields(t, first(t, scopeSpans, 1).value), 1).value))
	byName := map[string][]field{}
	var names []string
	for _, f := range scopeSpans {
		if f.num == 2 {
			s := fields(t, f.value)
			names = append(names, string(first(t, s, 5).value))
			byName[string(first(t, s, 5).value)] = s
		}
	}
	assert.Equal(t, []string{"invoke_agent session", "evaluate nodloop.verdict", "evaluate nodloop.judgment"}, names)
	turn := byName["invoke_agent session"]
	run := otel.RunSpan("r1")
	assert.Equal(t, run.TraceID, hexOf(first(t, turn, 1).value))
	assert.Equal(t, run.SpanID, hexOf(first(t, turn, 2).value))
	assert.Equal(t, uint64(1), first(t, turn, 6).n)
	assert.Equal(t, uint64(at.UnixNano()), first(t, turn, 7).n)
	assert.Equal(t, uint64(at.Add(90*time.Second).UnixNano()), first(t, turn, 8).n)
	overturn := valueOf(t, byName["evaluate nodloop.judgment"], 9, "nodloop.overturn")
	assert.Equal(t, []field{{num: 2, n: 0}}, overturn)
	assert.Equal(t, uint64(2), first(t, valueOf(t, byName["evaluate nodloop.judgment"], 9, "nodloop.tier"), 3).n)
	resourceLogs := fields(t, first(t, fields(t, c.raw[1]), 1).value)
	record := fields(t, first(t, fields(t, first(t, resourceLogs, 2).value), 2).value)
	assert.Equal(t, uint64(9), first(t, record, 2).n)
	assert.Equal(t, run.SpanID, hexOf(first(t, record, 10).value))
	assert.Equal(t, "gen_ai.evaluation.result", string(first(t, record, 12).value))
}

func hexOf(b []byte) string {
	const digits = "0123456789abcdef"
	var out bytes.Buffer
	for _, c := range b {
		out.WriteByte(digits[c>>4])
		out.WriteByte(digits[c&15])
	}
	return out.String()
}

// A refused request stops the export with the counts of what went out before it
func TestExporterExportRefused(t *testing.T) {
	type want struct {
		sent  otel.Sent
		paths []string
	}
	tcs := []struct {
		name string
		// The path the collector refuses
		args string
		want want
	}{
		{"a refused logs request fails after the spans went out", "/v1/logs",
			want{otel.Sent{Runs: 1, Questions: 1, Verdicts: 1, Outcomes: 1, Decisions: 1, Judgments: 1, Calls: 2}, []string{"/v1/traces", "/v1/logs"}}},
		{"refused spans fail before any log record", "/v1/traces", want{otel.Sent{}, []string{"/v1/traces"}}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := &collector{status: map[string]int{tc.args: http.StatusServiceUnavailable}}
			srv := httptest.NewServer(c)
			t.Cleanup(srv.Close)

			sent, err := exporter(srv, true, false).Export(context.Background(), batch())

			require.ErrorIs(t, err, otel.ErrStatus)
			assert.Equal(t, tc.want.sent, sent)
			assert.Equal(t, tc.want.paths, c.paths)
		})
	}
}

// The error of a failed send names the endpoint without the token a collector may take in its password or query
func TestExporterExportFailsToSend(t *testing.T) {
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	refusing := httptest.NewServer(&collector{status: map[string]int{"/v1/traces": http.StatusUnauthorized}})
	t.Cleanup(refusing.Close)
	type want struct {
		contains string
		// A secret of the endpoint the error must not repeat
		hidden string
	}
	tcs := []struct {
		name string
		// The endpoint of the exporter
		args string
		want want
	}{
		{"a URL no request can be built from fails without the password or the query", "http://ann:secret@bad host/v1/traces?token=secret", want{"invalid character", "secret"}},
		{"a collector that is gone fails without the password or the query", "http://ann:secret@" + closed.Listener.Addr().String() + "/v1/traces?token=secret",
			want{"connection refused", "secret"}},
		{"a refusal names the status without the password or the query", "http://ann:secret@" + refusing.Listener.Addr().String() + "/v1/traces?token=secret",
			want{"answered 401", "secret"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := otel.New(otel.Config{Traces: tc.args, Protocol: otel.ProtocolJSON, Client: http.DefaultClient})

			_, err := e.Export(context.Background(), otel.Batch{Runs: []otel.Run{{ID: "r1"}}})

			require.Error(t, err)
			assert.ErrorContains(t, err, tc.want.contains)
			assert.NotContains(t, err.Error(), tc.want.hidden)
		})
	}
}
