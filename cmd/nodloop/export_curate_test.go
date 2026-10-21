package main

import (
	"bytes"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/classify"
	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	knowledgefile "github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	"github.com/jeon-jihyeon/nodloop/internal/otel"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// The spans and log records of a collector that reads OTLP JSON with their attributes
type curateCollector struct {
	mu    sync.Mutex
	spans []map[string]any
	logs  []map[string]any
}

func (c *curateCollector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body struct {
		ResourceSpans []struct {
			ScopeSpans []struct {
				Spans []map[string]any `json:"spans"`
			} `json:"scopeSpans"`
		} `json:"resourceSpans"`
		ResourceLogs []struct {
			ScopeLogs []struct {
				LogRecords []map[string]any `json:"logRecords"`
			} `json:"scopeLogs"`
		} `json:"resourceLogs"`
	}
	_ = json.Unmarshal(raw, &body)
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, rs := range body.ResourceSpans {
		for _, ss := range rs.ScopeSpans {
			c.spans = append(c.spans, ss.Spans...)
		}
	}
	for _, rl := range body.ResourceLogs {
		for _, sl := range rl.ScopeLogs {
			c.logs = append(c.logs, sl.LogRecords...)
		}
	}
	w.WriteHeader(http.StatusOK)
}

// The attributes of a span or a log record by key with the one value each holds
func otlpAttributes(m map[string]any) map[string]any {
	out := map[string]any{}
	list, _ := m["attributes"].([]any)
	for _, a := range list {
		kv := a.(map[string]any)
		for _, v := range kv["value"].(map[string]any) {
			out[kv["key"].(string)] = v
		}
	}
	return out
}

// What a collector received of one span and of the log record of its uid
type curateSpan struct {
	// The name and the trace id
	ids   []any
	attrs map[string]any
	// The span ids it links
	links []string
	// The trace and span id of the log record and whether one arrived
	log []any
}

// The span of the id and the log record of the uid
func (c *curateCollector) find(spanID, uid string) curateSpan {
	c.mu.Lock()
	defer c.mu.Unlock()
	var span, log map[string]any
	for _, s := range c.spans {
		if s["spanId"] == spanID {
			span = s
		}
	}
	for _, l := range c.logs {
		if otlpAttributes(l)["log.record.uid"] == uid {
			log = l
		}
	}
	got := curateSpan{ids: []any{span["name"], span["traceId"]}, attrs: otlpAttributes(span), log: []any{log["traceId"], log["spanId"], log != nil}}
	listed, _ := span["links"].([]any)
	for _, l := range listed {
		got.links = append(got.links, l.(map[string]any)["spanId"].(string))
	}
	return got
}

// The keys of the list the span carries
func (s curateSpan) held(keys []string) []string {
	var out []string
	for _, k := range keys {
		if _, ok := s.attrs[k]; ok {
			out = append(out, k)
		}
	}
	return out
}

// How often each span id arrived
func (c *curateCollector) counts() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string]int{}
	for _, s := range c.spans {
		out[s["spanId"].(string)]++
	}
	return out
}

// Ids of the records of the curate fixture
type curateIDs struct {
	run, extract, judge                string
	ask, drop, apply                   string
	asked, audited, answer, unanswered string
	manual, proposal, compaction       string
	manualAsk, manualAnswer, critic    string
	asks, picked, answering, reverse   string
	knob, dismiss                      string
	keptAt, droppedAt, appliedAt       time.Time
}

const (
	curateCycle  = "2026-10-05"
	curatePolicy = "0.10.0+1a2b3c4d"
)

// One corrected run of session s1 with what curate and the write side recorded on its three lessons
// 1. kept: a tier 2 decide asked as a direction question after the run, answered keep and approved under ann through the answer
// 2. dropped: retired as nodloop by a tier 0 decide of temp_scope whose audit went unanswered after its second round
// 3. judged: approved as nodloop by a tier 1 decide after a judge of three conforming members
// 4. a manual intervention of ann keeping kept, the proposal of an agent retiring judged and a command keeping compaction c1
// 5. a question about that proposal the person asked for through the manual path, answered keep with no draw
// 6. a critic classify of the run beside the judge
// 7. the write side of writeSide
// The decides, exposes, answers and interventions come from their constructors as curate, the question path and the write side write them
func exportCurate(t *testing.T) (string, curateIDs) {
	t.Helper()
	ctx := t.Context()
	records := t.TempDir()
	at := exportAt
	a := app{cfg: config{recordDir: records}, now: func() time.Time { return at }}
	traces, err := a.traces()
	require.NoError(t, err)
	appendAll := func(trs ...trace.Trace) {
		for _, tr := range trs {
			require.NoError(t, traces.Append(ctx, tr.Record()))
		}
	}
	run, err := trace.NewRun(sessionProducer, "", trace.Labels{"repo": {"nodloop"}}, nil, []byte("done"), at.Add(time.Minute))
	require.NoError(t, err)
	run.SessionID = "s1"
	appendAll(run)
	ids := curateIDs{run: run.ID}
	verdicts, err := a.feedback()
	require.NoError(t, err)
	fb, err := feedback.New(run.ID, feedback.VerdictReject, feedback.ReasonFact, "wrong flag", nil, feedback.ReviewerSession, at.Add(90*time.Second))
	require.NoError(t, err)
	require.NoError(t, verdicts.Append(ctx, fb.Record()))
	at = at.Add(2 * time.Minute)
	extract := trace.Trace{
		ID: trace.NewID(at), Name: trace.NameExtract, Ref: run.ID, Time: at, Input: json.RawMessage("{}"),
		Output: json.RawMessage(`{"conclusion":"proposed","candidate":{"id":"kept","version":1}}`),
	}
	appendAll(extract)
	ids.extract = extract.ID
	ledger, err := a.ledger()
	require.NoError(t, err)
	for _, id := range []string{"kept", "dropped", "judged"} {
		_, _, err := ledger.Propose(ctx, knowledge.Knowledge{
			ID: id, Kind: knowledge.KindJudgment, Content: "Lesson " + id, Author: "claude",
			Run: &knowledge.RunScope{Producer: sessionProducer, Labels: trace.Labels{"repo": {"nodloop"}}}, Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{run.ID}},
		})
		require.NoError(t, err)
	}
	draw := func(unit, purpose string, p float64) trace.Draw {
		return trace.NewDraw(curateCycle, curatePolicy, "seed", unit, purpose, p)
	}
	decide := func(c curate.Change, d curate.Decision, p float64) trace.Trace {
		id := trace.NewID(at)
		tr, err := curate.Event{ID: id, Ref: run.ID, Source: trace.SourcePush, Draw: draw(id, "audit", p)}.Decide(c, d, nil, at)
		require.NoError(t, err)
		appendAll(tr)
		return tr
	}
	expose := func(ref string, x curate.Exposure) trace.Trace {
		tr, err := curate.Event{Session: "s1", Ref: ref, Source: trace.SourcePush, Draw: draw("s1", string(x.Kind), 1)}.Expose(x, at)
		require.NoError(t, err)
		appendAll(tr)
		return tr
	}
	answer := func(ref string, ans curate.Answer, wrote []trace.Written) trace.Trace {
		tr, err := curate.Event{Session: "s1", Ref: ref, Source: trace.SourcePush, Draw: draw("s1", "answer", 1)}.Answer(ans, curate.AnswerOutput{Records: wrote}, at)
		require.NoError(t, err)
		appendAll(tr)
		return tr
	}
	kept, dropped, judged := &knowledge.Ref{ID: "kept", Version: 1}, &knowledge.Ref{ID: "dropped", Version: 1}, &knowledge.Ref{ID: "judged", Version: 1}
	text := strings.Repeat("0", 64)

	at = at.Add(time.Minute)
	ids.ask = decide(curate.Change{Kind: curate.ChangeAdd, ID: "kept", Version: 1, Extract: extract.ID, Anchor: 1},
		curate.Decision{Tier: 2, RuleCode: curate.RuleScopeWider, Action: curate.ActionAsk}, 1).ID
	at = at.Add(time.Minute)
	ids.asked = expose(ids.ask, curate.Exposure{
		Kind: curate.QuestionDirection, Key: "ab12", Change: kept, Surface: curate.SurfaceContext, Rules: curate.QuestionRules,
		Choices: []curate.Choice{curate.ChoiceKeep, curate.ChoiceDrop}, Text: text, Lines: []string{text}, Round: 1, Run: run.ID, Runes: 120,
	}).ID
	at = at.Add(time.Minute)
	ids.answer = answer(ids.asked, curate.Answer{Choice: curate.ChoiceKeep, Decide: ids.ask, Change: kept, Proof: trace.ProofReply, Person: "ann"},
		[]trace.Written{{ID: "kept", Version: 1, Status: string(knowledge.StatusApproved)}}).ID
	all, err := ledger.All(ctx)
	require.NoError(t, err)
	approved, err := all.Version("kept", 1)
	require.NoError(t, err)
	approved.Status, approved.Approver, approved.ApprovedAt, approved.Time, approved.Decision = knowledge.StatusApproved, "ann", at, at, ids.answer
	items, err := knowledgefile.New(records)
	require.NoError(t, err)
	require.NoError(t, items.Append(ctx, approved.Record()))
	ids.keptAt = at

	at = at.Add(time.Minute)
	ids.drop = decide(curate.Change{Kind: curate.ChangeDrop, ID: "dropped", Version: 1, Anchor: 1}, curate.Decision{
		Tier: 0, RuleCode: curate.RuleTempScope, Action: curate.ActionDrop, Reason: knowledge.ReasonTempScope, Failure: curate.FailureHard,
		Reopen: curate.ReopenPermanentPlace,
	}, 0.05).ID
	require.NoError(t, testkit.Err(ledger.Drop(ctx, "dropped", 1, knowledge.ReasonTempScope, ids.drop)))
	ids.droppedAt = at
	at = at.Add(time.Minute)
	ids.audited = expose(ids.drop, curate.Exposure{
		Kind: curate.QuestionAudit, Key: "cd34", Change: dropped, Surface: curate.SurfaceContext, Rules: curate.QuestionRules,
		Choices: []curate.Choice{curate.ChoiceSame, curate.ChoiceUndo}, Text: text, Lines: []string{text}, Round: 2, Run: run.ID, Runes: 90,
	}).ID
	at = at.Add(time.Minute)
	ids.unanswered = answer(ids.audited, curate.Answer{Choice: curate.ChoiceNoAnswer, Decide: ids.drop, Change: dropped, Proof: trace.ProofNone}, nil).ID

	at = at.Add(time.Minute)
	member := classify.Asked{
		Name: "claude", Model: "claude-sonnet-5-5", Effort: "high",
		Answers: classify.Answers{"decided": {Yes: 0.9}, "against": {Yes: 0.03}},
	}
	out, err := json.Marshal(classify.TraceOutput{Members: []classify.Asked{member, member, member}})
	require.NoError(t, err)
	judge := trace.Trace{
		ID: trace.NewID(at), Name: trace.NameClassify, Subject: string(classify.PointJudge), Ref: run.ID, Time: at, DurationMS: 20_000,
		Input: json.RawMessage(`{"state":"` + text + `","questions":["against","decided"]}`), Output: out,
	}
	appendAll(judge)
	ids.judge = judge.ID
	at = at.Add(time.Minute)
	vote, confidence := 1.0, 0.97
	ids.apply = decide(curate.Change{Kind: curate.ChangeAdd, ID: "judged", Version: 1, Anchor: 1}, curate.Decision{
		Tier: 1, Judge: judge.ID, Outcome: curate.OutcomeConform, Vote: &vote, Bin: curate.Bin1, Confidence: &confidence, Action: curate.ActionApply,
	}, 0.15).ID
	require.NoError(t, testkit.Err(ledger.Apply(ctx, "judged", 1, ids.apply)))
	ids.appliedAt = at

	at = at.Add(time.Minute)
	manual, err := trace.Intervention{
		Action: trace.ActionJudge, ID: "kept", Version: 1, Choice: trace.ChoiceKeep, Proof: trace.ProofElicitation, Person: "ann",
	}.Trace("", "s1", trace.SourceManual, trace.InterventionOutput{}, at)
	require.NoError(t, err)
	at = at.Add(time.Minute)
	proposal, err := trace.Intervention{Action: trace.ActionJudge, ID: judged.ID, Version: 1, Choice: trace.ChoiceUndo, Proof: trace.ProofNone}.
		Trace("", "s1", trace.SourceManual, trace.InterventionOutput{}, at)
	require.NoError(t, err)
	appendAll(manual, proposal)
	ids.manual, ids.proposal = manual.ID, proposal.ID

	chosen := trace.Draw{CycleID: curateCycle, PolicyVersion: curatePolicy}
	at = at.Add(time.Minute)
	manualAsk, err := curate.Event{Session: "s1", Ref: proposal.ID, Source: trace.SourceManual, Draw: chosen}.Expose(curate.Exposure{
		Kind: curate.QuestionDirection, Key: "ef56", Change: judged, Surface: curate.SurfaceContext, Rules: curate.QuestionRules,
		Choices: []curate.Choice{curate.ChoiceKeep, curate.ChoiceDrop}, Text: text, Lines: []string{text}, Round: 1, Run: run.ID, Runes: 80,
	}, at)
	require.NoError(t, err)
	at = at.Add(time.Minute)
	manualAnswer, err := curate.Event{Session: "s1", Ref: manualAsk.ID, Source: trace.SourceManual, Draw: chosen}.
		Answer(curate.Answer{Choice: curate.ChoiceKeep, Change: judged, Proof: trace.ProofReply, Person: "ann"}, curate.AnswerOutput{}, at)
	require.NoError(t, err)
	at = at.Add(time.Minute)
	compaction, err := trace.Intervention{Action: trace.ActionJudge, Compaction: "c1", Choice: trace.ChoiceKeep, Proof: trace.ProofTerminal, Person: "ann"}.
		Trace("", "", trace.SourceCommand, trace.InterventionOutput{}, at)
	require.NoError(t, err)
	critic := trace.Trace{
		ID: trace.NewID(at), Name: trace.NameClassify, Subject: string(classify.PointCritic), Ref: run.ID, Time: at,
		Input:  json.RawMessage(`{"state":"draft","questions":{"holds":"It holds"}}`),
		Output: json.RawMessage(`{"members":[{"name":"claude","answers":{"holds":{"yes":0.9}}}],"answers":{"holds":{"yes":0.9}}}`),
	}
	appendAll(manualAsk, manualAnswer, compaction, critic)
	ids.manualAsk, ids.manualAnswer, ids.compaction, ids.critic = manualAsk.ID, manualAnswer.ID, compaction.ID, critic.ID
	appendAll(writeSide(t, &ids, at)...)
	return records, ids
}

// The records of the write side over the fixture
// 1. the decide that asks about the proposal and an answer of ann to it that no expose showed
// 2. an answer intervention through the pushed question, a dismiss of the question about the proposal by its decide,
// a reverse of the manual intervention and a knob a command set
func writeSide(t *testing.T, ids *curateIDs, at time.Time) []trace.Trace {
	t.Helper()
	at = at.Add(time.Minute)
	id := trace.NewID(at)
	asks, err := curate.Event{ID: id, Source: trace.SourcePush, Draw: trace.NewDraw(curateCycle, curatePolicy, "seed", id, "audit", 0)}.Decide(
		curate.Change{Kind: curate.ChangeIntervention, ID: "judged", Version: 1, Anchor: 1, Proposal: ids.proposal},
		curate.Decision{Tier: 2, RuleCode: curate.RuleProposal, Action: curate.ActionAsk}, nil, at)
	require.NoError(t, err)
	at = at.Add(time.Minute)
	picked, err := curate.Event{Session: "s1", Source: trace.SourceManual, Draw: trace.Draw{CycleID: curateCycle, PolicyVersion: curatePolicy}}.
		Answer(curate.Answer{Choice: curate.ChoiceNotYet, Decide: asks.ID, Proof: trace.ProofElicitation, Person: "ann"}, curate.AnswerOutput{}, at)
	require.NoError(t, err)
	person := func(in trace.Intervention, session string, source trace.Source) trace.Trace {
		at = at.Add(time.Minute)
		in.Person = "ann"
		tr, err := in.Trace("", session, source, trace.InterventionOutput{}, at)
		require.NoError(t, err)
		return tr
	}
	answering := person(trace.Intervention{
		Action: trace.ActionAnswer, Target: ids.asked, Decide: ids.ask, ID: "kept", Version: 1, Choice: trace.ChoiceKeep, Proof: trace.ProofElicitation,
	}, "s1", trace.SourceManual)
	dismiss := person(trace.Intervention{Action: trace.ActionDismiss, Target: asks.ID, Decide: asks.ID, Proof: trace.ProofElicitation}, "s1", trace.SourceManual)
	reverse := person(trace.Intervention{Action: trace.ActionReverse, Target: ids.manual, Reverses: ids.manual, Proof: trace.ProofTerminal}, "", trace.SourceCommand)
	value, before := 0.3, 0.15
	knob := person(trace.Intervention{
		Action: trace.ActionKnob, Target: "audit_tier1", Knob: "audit_tier1", Value: &value, Before: &before, Proof: trace.ProofTerminal,
	}, "", trace.SourceCommand)
	ids.asks, ids.picked, ids.answering, ids.dismiss, ids.reverse, ids.knob = asks.ID, picked.ID, answering.ID, dismiss.ID, reverse.ID, knob.ID
	return []trace.Trace{asks, picked, answering, dismiss, reverse, knob}
}

// Every question, judgment, intervention, decide and decision of the records goes as its span with its enums and one link
// and as a log record of who judged parented to that link
func TestRunExportOtelCurate(t *testing.T) {
	records, ids := exportCurate(t)
	c := &curateCollector{}
	srv := httptest.NewServer(c)
	t.Cleanup(srv.Close)
	getenv := func(k string) string {
		return map[string]string{envRecordDir: records, envOTLPEndpoint: srv.URL, envOTLPProtocol: "http/json"}[k]
	}
	var stdout, stderr bytes.Buffer

	code := runExport([]string{"otel"}, getenv, time.Now, &stdout, &stderr)

	require.Equal(t, 0, code, stderr.String())
	assert.Equal(t, "exported 1 runs, 1 verdicts, 0 outcomes, 3 model calls, 3 questions, 3 decisions and 15 judgments\n", stdout.String())
	run, judge, extract := otel.RunSpan(ids.run), otel.TraceSpan(ids.judge), otel.TraceSpan(ids.extract)
	asked, audited := otel.QuestionSpan(ids.run, ids.asked), otel.QuestionSpan(ids.run, ids.audited)
	manualAsk := otel.QuestionSpan(ids.run, ids.manualAsk)
	proposal, asks, manual := otel.TraceSpan(ids.proposal), otel.TraceSpan(ids.asks), otel.TraceSpan(ids.manual)
	kept, dropped, judged := otel.Ref{ID: "kept", Version: 1}, otel.Ref{ID: "dropped", Version: 1}, otel.Ref{ID: "judged", Version: 1}
	type want struct {
		name  string
		attrs map[string]any
		// The span ids it links
		links []string
		// The trace and span id of its log record and whether it has one
		log []any
		// Keys it never carries
		absent []string
	}
	tcs := []struct {
		name string
		// The span and the uid of its log record
		args otel.SpanRef
		want want
	}{
		{"the pushed question lies under the turn of its run", asked, want{"nodloop.question direction", map[string]any{
			"nodloop.question.kind": "direction", "nodloop.question.source": "push", "nodloop.question.round": "1",
			"nodloop.knowledge.candidate": "kept:1", "nodloop.cycle": curateCycle, "nodloop.draw.p": 1.0, "session.id": "s1",
		}, nil, []any{nil, nil, false}, nil}},
		{"the audit asked again lies under the turn of its run", audited, want{"nodloop.question audit", map[string]any{
			"nodloop.question.kind": "audit", "nodloop.question.round": "2", "nodloop.knowledge.candidate": "dropped:1",
		}, nil, []any{nil, nil, false}, nil}},
		{"the answer of keep is a person's judgment of the question with the enums of its decide", otel.TraceSpan(ids.answer),
			want{"evaluate nodloop.judgment", map[string]any{
				"gen_ai.evaluation.score.label": "keep", "gen_ai.evaluation.score.value": 1.0, "annotation.annotator_kind": "HUMAN",
				"nodloop.reviewer": "ann", "nodloop.proof": "reply", "nodloop.source": "push", "nodloop.question.kind": "direction",
				"nodloop.tier": "2", "nodloop.change.kind": "add", "nodloop.rule.code": "scope_wider", "nodloop.choice": "keep",
				"nodloop.overturn": false, "nodloop.authority": "person", "nodloop.cycle": curateCycle, "nodloop.draw.p": 1.0,
				"nodloop.knowledge.candidate": "kept:1", "session.id": "s1",
			}, []string{asked.SpanID}, []any{asked.TraceID, asked.SpanID, true}, nil}},
		{"no answer to the audit is code with no score", otel.TraceSpan(ids.unanswered), want{"evaluate nodloop.judgment", map[string]any{
			"gen_ai.evaluation.score.label": "no_answer", "annotation.annotator_kind": "CODE", "nodloop.proof": "none",
			"nodloop.question.kind": "audit", "nodloop.tier": "0", "nodloop.change.kind": "drop", "nodloop.retire.reason": "temp_scope",
		}, []string{audited.SpanID}, []any{audited.TraceID, audited.SpanID, true}, nil}},
		{"the manual intervention of a person names its source and links the extraction of its version", otel.TraceSpan(ids.manual),
			want{"evaluate nodloop.intervention", map[string]any{
				"gen_ai.evaluation.score.label": "keep", "annotation.annotator_kind": "HUMAN", "nodloop.reviewer": "ann",
				"nodloop.proof": "elicitation", "nodloop.source": "manual", "nodloop.action": "judge", "nodloop.authority": "person",
				"nodloop.choice": "keep", "nodloop.knowledge.candidate": "kept:1", "session.id": "s1",
			}, []string{extract.SpanID}, []any{extract.TraceID, extract.SpanID, true}, nil}},
		{"the proposal of an agent is a model's with nothing to link", otel.TraceSpan(ids.proposal), want{"evaluate nodloop.intervention", map[string]any{
			"gen_ai.evaluation.score.label": "undo", "annotation.annotator_kind": "LLM", "nodloop.proof": "none", "nodloop.authority": "agent",
			"nodloop.knowledge.candidate": "judged:1",
		}, nil, []any{nil, nil, true}, nil}},
		{"the decide that asked is code and links its run", otel.TraceSpan(ids.ask), want{"evaluate nodloop.curate", map[string]any{
			"gen_ai.evaluation.score.label": "ask", "annotation.annotator_kind": "CODE", "nodloop.source": "push", "nodloop.tier": "2",
			"nodloop.rule.code": "scope_wider", "nodloop.change.kind": "add", "nodloop.draw.p": 1.0, "session.id": "s1",
		}, []string{run.SpanID}, []any{run.TraceID, run.SpanID, true}, nil}},
		{"the tier 0 drop is code with its source and links its run", otel.TraceSpan(ids.drop), want{"evaluate nodloop.curate", map[string]any{
			"gen_ai.evaluation.score.label": "drop", "annotation.annotator_kind": "CODE", "nodloop.source": "push", "nodloop.tier": "0",
			"nodloop.rule.code": "temp_scope", "nodloop.change.kind": "drop", "nodloop.retire.reason": "temp_scope",
			"nodloop.knowledge.candidate": "dropped:1", "nodloop.draw.p": 0.05, "nodloop.cycle": curateCycle,
		}, []string{run.SpanID}, []any{run.TraceID, run.SpanID, true}, nil}},
		{"the judged apply scores the share of the conforming members and links the judge", otel.TraceSpan(ids.apply),
			want{"evaluate nodloop.curate", map[string]any{
				"gen_ai.evaluation.score.label": "conform", "gen_ai.evaluation.score.value": 1.0, "annotation.annotator_kind": "LLM",
				"nodloop.tier": "1", "nodloop.judge.bin": "b1", "nodloop.change.kind": "add", "nodloop.draw.p": 0.15,
			}, []string{judge.SpanID}, []any{judge.TraceID, judge.SpanID, true}, nil}},
		{"the approval through the answer is a person's linked to the question", otel.DecisionSpan(kept, "approved", ids.keptAt),
			want{"evaluate nodloop.decision", map[string]any{
				"gen_ai.evaluation.score.value": 1.0, "annotation.annotator_kind": "HUMAN", "nodloop.approver": "ann", "nodloop.source": "push",
				"nodloop.question.kind": "direction", "nodloop.choice": "keep", "nodloop.tier": "2", "session.id": "s1",
			}, []string{asked.SpanID}, []any{asked.TraceID, asked.SpanID, true}, nil}},
		{"the drop of a tier 0 rule is code linked to the run of its decide", otel.DecisionSpan(dropped, "retired", ids.droppedAt),
			want{"evaluate nodloop.decision", map[string]any{
				"gen_ai.evaluation.score.value": 0.0, "annotation.annotator_kind": "CODE", "nodloop.approver": "nodloop", "nodloop.source": "push",
				"nodloop.tier": "0", "nodloop.rule.code": "temp_scope", "nodloop.retire.reason": "temp_scope", "session.id": "s1",
			}, []string{run.SpanID}, []any{run.TraceID, run.SpanID, true}, nil}},
		{"the apply after a judge is a model's linked to the judge", otel.DecisionSpan(judged, "approved", ids.appliedAt),
			want{"evaluate nodloop.decision", map[string]any{
				"gen_ai.evaluation.score.value": 1.0, "annotation.annotator_kind": "LLM", "nodloop.approver": "nodloop", "nodloop.tier": "1",
				"nodloop.judge.bin": "b1",
			}, []string{judge.SpanID}, []any{judge.TraceID, judge.SpanID, true}, nil}},
		{"the judge carries every member with what they found and a log record of its prediction", judge, want{"invoke_agent nodloop.classify",
			map[string]any{
				"nodloop.classify.point": "judge", "nodloop.judge.outcome": "conform", "nodloop.judge.votes": 1.0, "nodloop.judge.bin": "b1",
				"session.id": "s1",
			}, []string{run.SpanID}, []any{judge.TraceID, judge.SpanID, true}, nil}},
		{"a critic classify carries nothing of the judge and has no log record", otel.TraceSpan(ids.critic), want{"invoke_agent nodloop.classify",
			map[string]any{"nodloop.classify.point": "critic"}, []string{run.SpanID}, []any{nil, nil, false},
			[]string{"nodloop.judge.outcome", "gen_ai.request.model"}}},
		{"a question the person asked for carries its source and no probability", manualAsk, want{"nodloop.question direction",
			map[string]any{"nodloop.question.source": "manual", "nodloop.cycle": curateCycle, "nodloop.knowledge.candidate": "judged:1"}, nil,
			[]any{nil, nil, false}, []string{"nodloop.draw.p"}}},
		{"a manual answer about a proposal is a person's judgment with no draw and no decide behind it", otel.TraceSpan(ids.manualAnswer),
			want{"evaluate nodloop.judgment", map[string]any{
				"gen_ai.evaluation.score.label": "keep", "annotation.annotator_kind": "HUMAN", "nodloop.source": "manual", "nodloop.choice": "keep",
				"nodloop.cycle": curateCycle, "nodloop.knowledge.candidate": "judged:1",
			}, []string{manualAsk.SpanID}, []any{manualAsk.TraceID, manualAsk.SpanID, true}, []string{"nodloop.draw.p", "nodloop.tier"}}},
		{"a command on a compaction names its source and no version", otel.TraceSpan(ids.compaction), want{"evaluate nodloop.intervention",
			map[string]any{"nodloop.source": "command", "nodloop.proof": "terminal", "annotation.annotator_kind": "HUMAN"}, nil,
			[]any{nil, nil, true}, []string{"nodloop.knowledge.candidate", "session.id"}}},
		{"the decide of a proposal links the proposal in the session that made it", asks, want{"evaluate nodloop.curate", map[string]any{
			"gen_ai.evaluation.score.label": "ask", "nodloop.rule.code": "proposal", "nodloop.change.kind": "intervention", "session.id": "s1",
		}, []string{proposal.SpanID}, []any{proposal.TraceID, proposal.SpanID, true}, nil}},
		{"an answer no expose showed links the decide it names", otel.TraceSpan(ids.picked), want{"evaluate nodloop.judgment", map[string]any{
			"gen_ai.evaluation.score.label": "notyet", "nodloop.source": "manual", "nodloop.rule.code": "proposal", "session.id": "s1",
		}, []string{asks.SpanID}, []any{asks.TraceID, asks.SpanID, true}, nil}},
		{"an answer intervention through an expose links the question span", otel.TraceSpan(ids.answering), want{"evaluate nodloop.intervention",
			map[string]any{"nodloop.action": "answer", "nodloop.choice": "keep", "nodloop.knowledge.candidate": "kept:1"},
			[]string{asked.SpanID}, []any{asked.TraceID, asked.SpanID, true}, nil}},
		{"a dismiss of a question by its decide links the decide", otel.TraceSpan(ids.dismiss), want{"evaluate nodloop.intervention",
			map[string]any{"gen_ai.evaluation.score.label": "dismiss", "nodloop.source": "manual"},
			[]string{asks.SpanID}, []any{asks.TraceID, asks.SpanID, true}, nil}},
		{"a reverse links the intervention it takes back", otel.TraceSpan(ids.reverse), want{"evaluate nodloop.intervention",
			map[string]any{"gen_ai.evaluation.score.label": "reverse", "nodloop.source": "command"},
			[]string{manual.SpanID}, []any{manual.TraceID, manual.SpanID, true}, nil}},
		{"a knob a command set names the knob and both values and links nothing", otel.TraceSpan(ids.knob), want{"evaluate nodloop.intervention",
			map[string]any{"nodloop.action": "knob", "nodloop.knob": "audit_tier1", "nodloop.knob.value": 0.3, "nodloop.knob.before": 0.15},
			nil, []any{nil, nil, true}, []string{"nodloop.choice"}}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := c.find(tc.args.SpanID, tc.args.TraceID)

			assert.Equal(t, []any{tc.want.name, tc.args.TraceID}, got.ids)
			assert.Subset(t, got.attrs, tc.want.attrs)
			assert.Empty(t, got.held(tc.want.absent))
			assert.Equal(t, tc.want.links, got.links)
			assert.Equal(t, tc.want.log, got.log)
		})
	}
}

// --pending sends each curate record once across two exports
func TestRunExportOtelCuratePending(t *testing.T) {
	records, _ := exportCurate(t)
	c := &curateCollector{}
	srv := httptest.NewServer(c)
	t.Cleanup(srv.Close)
	getenv := func(k string) string {
		return map[string]string{envRecordDir: records, envOTLPEndpoint: srv.URL, envOTLPProtocol: "http/json"}[k]
	}
	var first, second, stderr bytes.Buffer

	codes := []int{
		runExport([]string{"otel", "--pending"}, getenv, time.Now, &first, &stderr),
		runExport([]string{"otel", "--pending"}, getenv, time.Now, &second, &stderr),
	}

	assert.Equal(t, []int{0, 0}, codes, stderr.String())
	assert.Equal(t, []string{
		"exported 1 runs, 1 verdicts, 0 outcomes, 3 model calls, 3 questions, 3 decisions and 15 judgments\n",
		"exported 0 runs, 0 verdicts, 0 outcomes, 0 model calls, 0 questions, 0 decisions and 0 judgments\n",
	}, []string{first.String(), second.String()})
	counts := c.counts()
	assert.Len(t, counts, 26, "the run, three questions, a verdict, three decisions, fifteen judgments and three model calls")
	assert.Equal(t, []int{1}, slices.Compact(slices.Sorted(maps.Values(counts))), "no span arrived twice")
	assert.Len(t, c.logs, 20, "a verdict, three decisions, fifteen judgments and the judge once each")
}

// A corrupt line of any store before the mark fails the export and leaves the mark since an export reads every store whole
// A read that stopped at the mark would have skipped a record appended late with an older time
func TestRunExportOtelCorruptBeforeMark(t *testing.T) {
	const mark = `{"at":"2026-10-07T09:01:00Z"}`
	tcs := []struct {
		name string
		// The record file whose first line is corrupt
		args string
	}{
		{"a run line", "traces.jsonl"},
		{"a verdict line", "feedback.jsonl"},
		{"an outcome line", "outcomes.jsonl"},
		{"a knowledge line", "knowledge.jsonl"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			records := t.TempDir()
			exportRecordFile(t, records, tc.args, "not json\n")
			a := app{cfg: config{recordDir: records}}
			traces, err := a.traces()
			require.NoError(t, err)
			run, err := trace.NewRun("bot", "", nil, nil, []byte("steps"), exportAt)
			require.NoError(t, err)
			require.NoError(t, traces.Append(t.Context(), run.Record()))
			verdicts, err := a.feedback()
			require.NoError(t, err)
			fb, err := feedback.New(run.ID, feedback.VerdictApprove, "", "", nil, "ann", exportAt.Add(2*time.Minute))
			require.NoError(t, err)
			require.NoError(t, verdicts.Append(t.Context(), fb.Record()))
			exportRecordFile(t, records, otel.MarkFile, mark)
			c := &curateCollector{}
			srv := httptest.NewServer(c)
			t.Cleanup(srv.Close)
			getenv := func(k string) string {
				return map[string]string{envRecordDir: records, envOTLPEndpoint: srv.URL, envOTLPProtocol: "http/json"}[k]
			}
			var stderr bytes.Buffer

			code := runExport([]string{"otel", "--pending"}, getenv, time.Now, &bytes.Buffer{}, &stderr)

			assert.Equal(t, 1, code)
			assert.Contains(t, stderr.String(), tc.args+" has corrupt lines")
			b, err := os.ReadFile(filepath.Join(records, otel.MarkFile))
			require.NoError(t, err)
			assert.Equal(t, mark, string(b))
			assert.Empty(t, c.counts())
		})
	}
}
