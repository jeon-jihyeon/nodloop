package mcp_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/analysis"
	"github.com/jeon-jihyeon/nodloop/internal/diagnose"
	"github.com/jeon-jihyeon/nodloop/internal/evidence"
	evidencefile "github.com/jeon-jihyeon/nodloop/internal/evidence/file"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/mcp"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

const (
	spikeEvent   = "tq-005"
	plannedEvent = "tq-009"
	// Shares the planned change context with plannedEvent
	relatedEvent   = "tq-011"
	unrelatedEvent = "tq-003"
	knownSegment   = "metric-anomaly-investigation#Metric anomaly investigation/Check the segment#1"
	// A review that cites the runbook paragraphs of every context and passes the gate
	reviewFile = "testdata/review.json"
)

func TestServerTools(t *testing.T) {
	t.Parallel()
	st := testkit.Open(t)
	policy := analysis.DefaultPolicy()
	diagnoser := diagnose.New(st.Source, policy, diagnose.Limits{}, nil, st.Traces, st.Feedback, st.Ledger, st.Clock.Now)
	srv := mcp.New(st.Source, policy, diagnoser, st.Traces, st.Feedback, st.Outcomes, st.Ledger, st.Clock.Now, "test")
	c := testkit.Connect(t, srv.ServeTransport)

	assert.ElementsMatch(t, mcp.Tools(), c.Tools(t))
}

func TestServerEvents(t *testing.T) {
	t.Parallel()
	st := testkit.Open(t)
	policy := analysis.DefaultPolicy()
	diagnoser := diagnose.New(st.Source, policy, diagnose.Limits{}, nil, st.Traces, st.Feedback, st.Ledger, st.Clock.Now)
	srv := mcp.New(st.Source, policy, diagnoser, st.Traces, st.Feedback, st.Outcomes, st.Ledger, st.Clock.Now, "test")
	c := testkit.Connect(t, srv.ServeTransport)
	want, err := os.ReadFile("testdata/events.json")
	require.NoError(t, err)

	var got json.RawMessage
	require.NoError(t, c.Call(t, "events", map[string]any{}, &got))
	assert.JSONEq(t, string(want), string(got))
}

// The answers are captured from the default policy over the demo data
func TestServerObserve(t *testing.T) {
	t.Parallel()
	st := testkit.Open(t)
	policy := analysis.DefaultPolicy()
	diagnoser := diagnose.New(st.Source, policy, diagnose.Limits{}, nil, st.Traces, st.Feedback, st.Ledger, st.Clock.Now)
	srv := mcp.New(st.Source, policy, diagnoser, st.Traces, st.Feedback, st.Outcomes, st.Ledger, st.Clock.Now, "test")
	c := testkit.Connect(t, srv.ServeTransport)
	tcs := []struct {
		name string
		args string
		want string
	}{
		{
			name: "a spike event returns its observations under the policy version",
			args: spikeEvent,
			want: "testdata/observe-tq-005.json",
		},
		{
			name: "a planned change event returns its change context",
			args: plannedEvent,
			want: "testdata/observe-tq-009.json",
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			want, err := os.ReadFile(tc.want)
			require.NoError(t, err)

			var got json.RawMessage
			require.NoError(t, c.Call(t, "observe", map[string]any{"event_id": tc.args}, &got))
			assert.JSONEq(t, string(want), string(got))
		})
	}
}

func TestServerContext(t *testing.T) {
	t.Parallel()
	st := testkit.Open(t)
	policy := analysis.DefaultPolicy()
	diagnoser := diagnose.New(st.Source, policy, diagnose.Limits{}, nil, st.Traces, st.Feedback, st.Ledger, st.Clock.Now)
	srv := mcp.New(st.Source, policy, diagnoser, st.Traces, st.Feedback, st.Outcomes, st.Ledger, st.Clock.Now, "test")
	c := testkit.Connect(t, srv.ServeTransport)

	var got struct {
		PendingID           string                        `json:"pending_id"`
		Rules               string                        `json:"rules"`
		Schema              json.RawMessage               `json:"schema"`
		Context             string                        `json:"context"`
		KnowledgeCandidates []diagnose.KnowledgeCandidate `json:"knowledge_candidates"`
		ExampleCandidates   []diagnose.ExampleCandidate   `json:"example_candidates"`
		// Nil when the answer leaves the flag out
		CandidatesOmitted *bool `json:"candidates_omitted"`
	}
	require.NoError(t, c.Call(t, "context", map[string]any{"event_id": spikeEvent}, &got))
	assert.NotEmpty(t, got.PendingID)
	assert.Equal(t, diagnose.Rules, got.Rules)
	assert.JSONEq(t, diagnose.Schema, string(got.Schema))
	assert.Contains(t, got.Context, "["+knownSegment+"]")
	assert.Empty(t, got.KnowledgeCandidates)
	assert.Empty(t, got.ExampleCandidates)
	assert.Equal(t, new(bool), got.CandidatesOmitted)
}

// Approved knowledge and the correction it came from reach events of the same change context only
// A candidate nobody approved is never offered
// record refuses an offered context until select and the recorded trace then names the chosen knowledge
func TestServerOffers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := testkit.Open(t)
	policy := analysis.DefaultPolicy()
	diagnoser := diagnose.New(st.Source, policy, diagnose.Limits{}, nil, st.Traces, st.Feedback, st.Ledger, st.Clock.Now)
	srv := mcp.New(st.Source, policy, diagnoser, st.Traces, st.Feedback, st.Outcomes, st.Ledger, st.Clock.Now, "test")
	c := testkit.Connect(t, srv.ServeTransport)
	b, err := os.ReadFile(reviewFile)
	require.NoError(t, err)
	review := json.RawMessage(b)
	var opened struct {
		PendingID string `json:"pending_id"`
	}
	require.NoError(t, c.Call(t, "context", map[string]any{"event_id": plannedEvent}, &opened))
	var reviewed struct {
		TraceID string `json:"trace_id"`
	}
	recordInput := map[string]any{"pending_id": opened.PendingID, "diagnosis": review}
	require.NoError(t, c.Call(t, "record", recordInput, &reviewed))
	rejection := map[string]any{
		"trace_id": reviewed.TraceID, "verdict": feedback.VerdictReject, "reason": "measurement changed first",
	}
	require.NoError(t, c.Run(t, "feedback", rejection))
	scope := knowledge.Scope{ChangeContexts: []evidence.Context{evidence.ContextPlannedChange}}
	require.NoError(t, c.Run(t, "propose", map[string]any{
		"id": "k-tracking", "kind": knowledge.KindJudgment, "content": "after a planned change check tracking first",
		"change_contexts": scope.ChangeContexts, "trace_ids": []string{reviewed.TraceID},
	}))
	require.NoError(t, c.Run(t, "approve", map[string]any{"id": "k-tracking", "version": 1, "approver": "reviewer"}))
	require.NoError(t, c.Run(t, "propose", map[string]any{
		"id": "k-draft", "kind": knowledge.KindJudgment, "content": "after a planned change wait a day",
		"change_contexts": scope.ChangeContexts, "trace_ids": []string{reviewed.TraceID},
	}))
	type offers struct {
		PendingID string                        `json:"pending_id"`
		Knowledge []diagnose.KnowledgeCandidate `json:"knowledge_candidates"`
		Examples  []diagnose.ExampleCandidate   `json:"example_candidates"`
	}

	var unrelated offers
	require.NoError(t, c.Call(t, "context", map[string]any{"event_id": unrelatedEvent}, &unrelated))
	var related offers
	require.NoError(t, c.Call(t, "context", map[string]any{"event_id": relatedEvent}, &related))
	recordRelated := map[string]any{"pending_id": related.PendingID, "diagnosis": review}
	notSelected := c.Run(t, "record", recordRelated)
	var selected struct {
		Text    string `json:"text"`
		Omitted bool   `json:"omitted"`
	}
	choices := map[string]any{
		"pending_id": related.PendingID,
		"knowledge":  []diagnose.Choice{{ID: "k-tracking", Reason: "same context"}},
		"examples":   []diagnose.Choice{{ID: reviewed.TraceID, Reason: "same shape"}},
	}
	require.NoError(t, c.Call(t, "select", choices, &selected))
	var recorded struct {
		TraceID string `json:"trace_id"`
	}
	require.NoError(t, c.Call(t, "record", recordRelated, &recorded))
	tr, err := st.Traces.Get(ctx, recorded.TraceID)
	require.NoError(t, err)
	assert.Equal(t, offers{PendingID: unrelated.PendingID}, unrelated)
	assert.Equal(t, offers{
		PendingID: related.PendingID,
		Knowledge: []diagnose.KnowledgeCandidate{{
			ID: "k-tracking", Version: 1, Kind: knowledge.KindJudgment,
			Head: "after a planned change check tracking first", Scope: scope,
		}},
		Examples: []diagnose.ExampleCandidate{{
			TraceID: reviewed.TraceID, Verdict: feedback.VerdictReject, Head: "measurement changed first",
		}},
	}, related)
	assert.ErrorIs(t, notSelected, testkit.ErrTool)
	assert.ErrorContains(t, notSelected, diagnose.ErrNotSelected.Error())
	assert.Contains(t, selected.Text, "check tracking first")
	assert.Contains(t, selected.Text, "measurement changed first")
	assert.False(t, selected.Omitted)
	assert.Contains(t, string(tr.Input), `"k-tracking"`)
}

// record answers with the reasons once and records the next review of the same context
func TestServerRecordSendsBack(t *testing.T) {
	t.Parallel()
	st := testkit.Open(t)
	policy := analysis.DefaultPolicy()
	diagnoser := diagnose.New(st.Source, policy, diagnose.Limits{}, nil, st.Traces, st.Feedback, st.Ledger, st.Clock.Now)
	srv := mcp.New(st.Source, policy, diagnoser, st.Traces, st.Feedback, st.Outcomes, st.Ledger, st.Clock.Now, "test")
	c := testkit.Connect(t, srv.ServeTransport)
	var opened struct {
		PendingID string `json:"pending_id"`
	}
	require.NoError(t, c.Call(t, "context", map[string]any{"event_id": spikeEvent}, &opened))
	incomplete := diagnose.Diagnosis{
		Status:        diagnose.StatusReadyForReview,
		Observations:  []string{"clicks up"},
		Causes:        []diagnose.Cause{{Summary: "low quality traffic", ParagraphIDs: []string{knownSegment}}},
		Checks:        []diagnose.Check{{Step: "s", Purpose: "p", ParagraphIDs: []string{knownSegment}}},
		OpenQuestions: []string{},
	}
	var back struct {
		Recorded  bool     `json:"recorded"`
		PendingID string   `json:"pending_id"`
		Revise    []string `json:"revise"`
	}
	in := map[string]any{"pending_id": opened.PendingID, "diagnosis": incomplete}
	require.NoError(t, c.Call(t, "record", in, &back))
	assert.False(t, back.Recorded)
	assert.Equal(t, opened.PendingID, back.PendingID)
	require.Len(t, back.Revise, 1)
	assert.Contains(t, back.Revise[0], "Confirm the signal")
	var recorded struct {
		Recorded bool   `json:"recorded"`
		TraceID  string `json:"trace_id"`
	}
	require.NoError(t, c.Call(t, "record", in, &recorded))
	assert.True(t, recorded.Recorded)
	assert.NotEmpty(t, recorded.TraceID)
}

func TestServerRecord(t *testing.T) {
	t.Parallel()
	st := testkit.Open(t)
	policy := analysis.DefaultPolicy()
	diagnoser := diagnose.New(st.Source, policy, diagnose.Limits{}, nil, st.Traces, st.Feedback, st.Ledger, st.Clock.Now)
	srv := mcp.New(st.Source, policy, diagnoser, st.Traces, st.Feedback, st.Outcomes, st.Ledger, st.Clock.Now, "test")
	c := testkit.Connect(t, srv.ServeTransport)
	b, err := os.ReadFile(reviewFile)
	require.NoError(t, err)
	var review diagnose.Diagnosis
	require.NoError(t, json.Unmarshal(b, &review))
	type want struct {
		ForcedHold bool               `json:"forced_hold"`
		Diagnosis  diagnose.Diagnosis `json:"diagnosis"`
	}
	tcs := []struct {
		name string
		args []string
		want want
	}{
		{
			name: "a citation outside the context is dropped from the recorded review",
			args: []string{knownSegment, "made-up"},
			want: want{Diagnosis: review},
		},
		{
			name: "a review citing the context is recorded as written",
			args: []string{knownSegment},
			want: want{Diagnosis: review},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var opened struct {
				PendingID string `json:"pending_id"`
			}
			require.NoError(t, c.Call(t, "context", map[string]any{"event_id": spikeEvent}, &opened))
			written := review
			written.Causes = []diagnose.Cause{{Summary: "low quality traffic", ParagraphIDs: tc.args}}

			var got want
			require.NoError(t, c.Call(t, "record", map[string]any{"pending_id": opened.PendingID, "diagnosis": written}, &got))
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestServerFeedback(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := testkit.Open(t)
	policy := analysis.DefaultPolicy()
	diagnoser := diagnose.New(st.Source, policy, diagnose.Limits{}, nil, st.Traces, st.Feedback, st.Ledger, st.Clock.Now)
	srv := mcp.New(st.Source, policy, diagnoser, st.Traces, st.Feedback, st.Outcomes, st.Ledger, st.Clock.Now, "test")
	c := testkit.Connect(t, srv.ServeTransport)
	b, err := os.ReadFile(reviewFile)
	require.NoError(t, err)
	review := json.RawMessage(b)
	// Keys in sorted order and no spaces so the bytes survive any decode and encode on the way
	edited := json.RawMessage(`{"causes":[{"paragraph_ids":["` + knownSegment + `"],"summary":"campaign launch"}],` +
		`"checks":[],"observations":["clicks up"],"open_questions":[],"status":"ready_for_review"}`)
	type args struct {
		verdict  feedback.Verdict
		reason   string
		edited   json.RawMessage
		reviewer string
	}
	type answer struct {
		TraceID  string           `json:"trace_id"`
		Verdict  feedback.Verdict `json:"verdict"`
		Reviewer string           `json:"reviewer"`
	}
	tcs := []struct {
		name string
		args args
		want feedback.Feedback
	}{
		{
			name: "an edit stores the corrected review in full",
			args: args{verdict: feedback.VerdictEdit, reason: "it was a launch", edited: edited},
			want: feedback.Feedback{
				Verdict: feedback.VerdictEdit, Reason: "it was a launch", Edited: edited, Reviewer: feedback.DefaultReviewer,
			},
		},
		{
			name: "a rejection with a null edited review keeps the named reviewer and stores no edit",
			args: args{verdict: feedback.VerdictReject, reason: "wrong segment", reviewer: "user"},
			want: feedback.Feedback{Verdict: feedback.VerdictReject, Reason: "wrong segment", Reviewer: "user"},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var opened struct {
				PendingID string `json:"pending_id"`
			}
			require.NoError(t, c.Call(t, "context", map[string]any{"event_id": spikeEvent}, &opened))
			var reviewed struct {
				TraceID string `json:"trace_id"`
			}
			recordInput := map[string]any{"pending_id": opened.PendingID, "diagnosis": review}
			require.NoError(t, c.Call(t, "record", recordInput, &reviewed))

			var got answer
			in := map[string]any{
				"trace_id": reviewed.TraceID, "verdict": tc.args.verdict, "reason": tc.args.reason,
				"edited": tc.args.edited, "reviewer": tc.args.reviewer,
			}
			require.NoError(t, c.Call(t, "feedback", in, &got))
			stored, err := st.Feedback.List(ctx, feedback.Filter{TraceID: reviewed.TraceID})
			require.NoError(t, err)
			require.Len(t, stored, 1)
			want := tc.want
			want.TraceID = reviewed.TraceID
			want.Time = stored[0].Time
			assert.Equal(t, answer{TraceID: want.TraceID, Verdict: want.Verdict, Reviewer: want.Reviewer}, got)
			assert.Equal(t, []feedback.Feedback{want}, stored)
		})
	}
}

func TestServerOutcome(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := testkit.Open(t)
	policy := analysis.DefaultPolicy()
	diagnoser := diagnose.New(st.Source, policy, diagnose.Limits{}, nil, st.Traces, st.Feedback, st.Ledger, st.Clock.Now)
	srv := mcp.New(st.Source, policy, diagnoser, st.Traces, st.Feedback, st.Outcomes, st.Ledger, st.Clock.Now, "test")
	c := testkit.Connect(t, srv.ServeTransport)
	b, err := os.ReadFile(reviewFile)
	require.NoError(t, err)
	review := json.RawMessage(b)
	type args struct {
		result         feedback.Result
		confirmedCause string
		note           string
		reviewer       string
	}
	type answer struct {
		TraceID  string          `json:"trace_id"`
		Result   feedback.Result `json:"result"`
		Reviewer string          `json:"reviewer"`
	}
	tcs := []struct {
		name string
		args args
		want feedback.Outcome
	}{
		{
			name: "a confirmed check stores the confirmed cause",
			args: args{result: feedback.ResultConfirmed, confirmedCause: "campaign launch"},
			want: feedback.Outcome{
				Result: feedback.ResultConfirmed, ConfirmedCause: "campaign launch", Reviewer: feedback.DefaultReviewer,
			},
		},
		{
			name: "an inconclusive check stores its note",
			args: args{result: feedback.ResultInconclusive, note: "logs expired"},
			want: feedback.Outcome{
				Result: feedback.ResultInconclusive, Note: "logs expired", Reviewer: feedback.DefaultReviewer,
			},
		},
		{
			name: "a refuted check keeps the named reviewer",
			args: args{result: feedback.ResultRefuted, reviewer: "user"},
			want: feedback.Outcome{Result: feedback.ResultRefuted, Reviewer: "user"},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var opened struct {
				PendingID string `json:"pending_id"`
			}
			require.NoError(t, c.Call(t, "context", map[string]any{"event_id": spikeEvent}, &opened))
			var reviewed struct {
				TraceID string `json:"trace_id"`
			}
			recordInput := map[string]any{"pending_id": opened.PendingID, "diagnosis": review}
			require.NoError(t, c.Call(t, "record", recordInput, &reviewed))

			var got answer
			in := map[string]any{
				"trace_id": reviewed.TraceID, "result": tc.args.result, "confirmed_cause": tc.args.confirmedCause,
				"note": tc.args.note, "reviewer": tc.args.reviewer,
			}
			require.NoError(t, c.Call(t, "outcome", in, &got))
			stored, err := st.Outcomes.List(ctx, reviewed.TraceID)
			require.NoError(t, err)
			require.Len(t, stored, 1)
			want := tc.want
			want.TraceID = reviewed.TraceID
			want.Time = stored[0].Time
			assert.Equal(t, answer{TraceID: want.TraceID, Result: want.Result, Reviewer: want.Reviewer}, got)
			assert.Equal(t, []feedback.Outcome{want}, stored)
		})
	}
}

func TestServerProposeAndApprove(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := testkit.Open(t)
	policy := analysis.DefaultPolicy()
	diagnoser := diagnose.New(st.Source, policy, diagnose.Limits{}, nil, st.Traces, st.Feedback, st.Ledger, st.Clock.Now)
	srv := mcp.New(st.Source, policy, diagnoser, st.Traces, st.Feedback, st.Outcomes, st.Ledger, st.Clock.Now, "test")
	c := testkit.Connect(t, srv.ServeTransport)
	b, err := os.ReadFile(reviewFile)
	require.NoError(t, err)
	review := json.RawMessage(b)
	var opened struct {
		PendingID string `json:"pending_id"`
	}
	require.NoError(t, c.Call(t, "context", map[string]any{"event_id": plannedEvent}, &opened))
	var reviewed struct {
		TraceID string `json:"trace_id"`
	}
	require.NoError(t, c.Call(t, "record", map[string]any{"pending_id": opened.PendingID, "diagnosis": review}, &reviewed))
	type args struct {
		id     string
		kind   knowledge.Kind
		author string
	}
	type answer struct {
		ID       string                `json:"id"`
		Version  int                   `json:"version"`
		Status   knowledge.Status      `json:"status"`
		Approver string                `json:"approver"`
		Overlaps []knowledge.Knowledge `json:"overlaps"`
	}
	type want struct {
		proposed answer
		approved answer
		author   string
	}
	// The cases differ in kind so neither overlaps the other
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			name: "a candidate without an author is proposed by claude and approved by the named person",
			args: args{id: "k-tracking", kind: knowledge.KindJudgment},
			want: want{
				proposed: answer{
					ID: "k-tracking", Version: 1, Status: knowledge.StatusCandidate, Overlaps: []knowledge.Knowledge{},
				},
				approved: answer{ID: "k-tracking", Version: 1, Status: knowledge.StatusApproved, Approver: "reviewer"},
				author:   "claude",
			},
		},
		{
			name: "a candidate keeps the author the caller names",
			args: args{id: "k-meaning", kind: knowledge.KindMeaning, author: "user"},
			want: want{
				proposed: answer{ID: "k-meaning", Version: 1, Status: knowledge.StatusCandidate, Overlaps: []knowledge.Knowledge{}},
				approved: answer{ID: "k-meaning", Version: 1, Status: knowledge.StatusApproved, Approver: "reviewer"},
				author:   "user",
			},
		},
	}
	scope := knowledge.Scope{Metrics: []string{"click_count"}, Dims: map[string]string{"platform": "ios"}}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var proposed answer
			in := map[string]any{
				"id": tc.args.id, "kind": tc.args.kind, "content": "after a planned change check tracking first",
				"metrics": scope.Metrics, "dims": scope.Dims, "trace_ids": []string{reviewed.TraceID}, "author": tc.args.author,
			}
			require.NoError(t, c.Call(t, "propose", in, &proposed))
			var approved answer
			approval := map[string]any{"id": tc.args.id, "version": 1, "approver": "reviewer"}
			require.NoError(t, c.Call(t, "approve", approval, &approved))
			stored, err := st.Ledger.Approved(ctx, tc.args.id, 1)
			require.NoError(t, err)
			assert.Equal(t, tc.want.proposed, proposed)
			assert.Equal(t, tc.want.approved, approved)
			assert.Equal(t, knowledge.Knowledge{
				ID: tc.args.id, Version: 1, Kind: tc.args.kind, Content: "after a planned change check tracking first",
				Scope: scope, Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{reviewed.TraceID}}, Basis: stored.Basis,
				Status: knowledge.StatusApproved, Approver: "reviewer", ApprovedAt: stored.ApprovedAt, Time: stored.Time,
				Author: tc.want.author,
			}, stored)
		})
	}
}

// The answers are captured from the demo data and from a fixture event with more rows than the limit
func TestServerDetail(t *testing.T) {
	t.Parallel()
	type args struct {
		dir   string
		input map[string]any
	}
	demo := testkit.DemoDir(t)
	tcs := []struct {
		name string
		args args
		want string
	}{
		{
			name: "one metric from a start time returns its rows to the event end",
			args: args{dir: demo, input: map[string]any{
				"event_id": spikeEvent, "metric": "click_count", "start": "2026-09-21T12:00:00Z",
			}},
			want: "testdata/detail-from-start.json",
		},
		{
			name: "an end time bounds the range inclusively",
			args: args{dir: demo, input: map[string]any{
				"event_id": spikeEvent, "metric": "click_count", "start": "2026-09-21T12:00:00Z", "end": "2026-09-21T13:00:00Z",
			}},
			want: "testdata/detail-bounded.json",
		},
		{
			name: "rows past the limit are left out and counted",
			args: args{dir: "testdata/large", input: map[string]any{"event_id": "tq-large"}},
			want: "testdata/detail-limited.json",
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := testkit.Open(t)
			src, err := evidencefile.New(tc.args.dir)
			require.NoError(t, err)
			policy := analysis.DefaultPolicy()
			diagnoser := diagnose.New(src, policy, diagnose.Limits{}, nil, st.Traces, st.Feedback, st.Ledger, st.Clock.Now)
			srv := mcp.New(src, policy, diagnoser, st.Traces, st.Feedback, st.Outcomes, st.Ledger, st.Clock.Now, "test")
			c := testkit.Connect(t, srv.ServeTransport)
			want, err := os.ReadFile(tc.want)
			require.NoError(t, err)

			var got json.RawMessage
			require.NoError(t, c.Call(t, "detail", tc.args.input, &got))
			assert.JSONEq(t, string(want), string(got))
		})
	}
}

// Recording a context closes it
func TestServerPending(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := testkit.Open(t)
	policy := analysis.DefaultPolicy()
	diagnoser := diagnose.New(st.Source, policy, diagnose.Limits{}, nil, st.Traces, st.Feedback, st.Ledger, st.Clock.Now)
	srv := mcp.New(st.Source, policy, diagnoser, st.Traces, st.Feedback, st.Outcomes, st.Ledger, st.Clock.Now, "test")
	c := testkit.Connect(t, srv.ServeTransport)
	b, err := os.ReadFile(reviewFile)
	require.NoError(t, err)
	review := json.RawMessage(b)
	type row struct {
		PendingID string `json:"pending_id"`
		EventID   string `json:"event_id"`
		Time      string `json:"time"`
	}
	type answer struct {
		Pending []row `json:"pending"`
	}
	var opened struct {
		PendingID string `json:"pending_id"`
	}
	require.NoError(t, c.Call(t, "context", map[string]any{"event_id": spikeEvent}, &opened))
	tr, err := st.Traces.Get(ctx, opened.PendingID)
	require.NoError(t, err)

	var open answer
	require.NoError(t, c.Call(t, "pending", map[string]any{}, &open))
	require.NoError(t, c.Run(t, "record", map[string]any{"pending_id": opened.PendingID, "diagnosis": review}))
	var closed answer
	require.NoError(t, c.Call(t, "pending", map[string]any{}, &closed))
	want := row{PendingID: opened.PendingID, EventID: spikeEvent, Time: tr.Time.Format(time.RFC3339)}
	assert.Equal(t, answer{Pending: []row{want}}, open)
	assert.Equal(t, answer{Pending: []row{}}, closed)
}

// Tool errors cross the transport as text so each case names the sentinel whose message the text carries
func TestServerRefusals(t *testing.T) {
	t.Parallel()
	st := testkit.Open(t)
	policy := analysis.DefaultPolicy()
	diagnoser := diagnose.New(st.Source, policy, diagnose.Limits{}, nil, st.Traces, st.Feedback, st.Ledger, st.Clock.Now)
	srv := mcp.New(st.Source, policy, diagnoser, st.Traces, st.Feedback, st.Outcomes, st.Ledger, st.Clock.Now, "test")
	c := testkit.Connect(t, srv.ServeTransport)
	b, err := os.ReadFile(reviewFile)
	require.NoError(t, err)
	review := json.RawMessage(b)
	var opened struct {
		PendingID string `json:"pending_id"`
	}
	require.NoError(t, c.Call(t, "context", map[string]any{"event_id": plannedEvent}, &opened))
	var reviewed struct {
		TraceID string `json:"trace_id"`
	}
	require.NoError(t, c.Call(t, "record", map[string]any{"pending_id": opened.PendingID, "diagnosis": review}, &reviewed))
	proposal := map[string]any{
		"id": "k-tracking", "kind": knowledge.KindJudgment, "content": "after a planned change check tracking first",
		"trace_ids": []string{reviewed.TraceID},
	}
	require.NoError(t, c.Run(t, "propose", proposal))
	var fresh struct {
		PendingID string `json:"pending_id"`
	}
	require.NoError(t, c.Call(t, "context", map[string]any{"event_id": relatedEvent}, &fresh))
	type args struct {
		tool  string
		input map[string]any
	}
	tcs := []struct {
		name string
		args args
		want string
	}{
		{
			name: "observe refuses an unknown event",
			args: args{tool: "observe", input: map[string]any{"event_id": "nope"}},
			want: evidence.ErrNotFound.Error(),
		},
		{
			name: "context refuses an unknown event",
			args: args{tool: "context", input: map[string]any{"event_id": "nope"}},
			want: evidence.ErrNotFound.Error(),
		},
		{
			name: "record refuses an unknown pending id",
			args: args{tool: "record", input: map[string]any{"pending_id": "nope", "diagnosis": review}},
			want: trace.ErrNotFound.Error(),
		},
		{
			name: "record refuses a context recorded before",
			args: args{tool: "record", input: map[string]any{"pending_id": opened.PendingID, "diagnosis": review}},
			want: diagnose.ErrRecorded.Error(),
		},
		{
			name: "select refuses knowledge outside the offer",
			args: args{tool: "select", input: map[string]any{
				"pending_id": fresh.PendingID, "knowledge": []diagnose.Choice{{ID: "k-other", Reason: "x"}},
				"examples": []diagnose.Choice{},
			}},
			want: diagnose.ErrNotOffered.Error(),
		},
		{
			name: "feedback refuses an unknown trace",
			args: args{tool: "feedback", input: map[string]any{"trace_id": "nope", "verdict": feedback.VerdictApprove}},
			want: trace.ErrNotFound.Error(),
		},
		{
			name: "feedback refuses an unknown verdict",
			args: args{tool: "feedback", input: map[string]any{"trace_id": reviewed.TraceID, "verdict": "maybe"}},
			want: feedback.ErrVerdictUnknown.Error(),
		},
		{
			name: "feedback refuses a context trace that is not a review",
			args: args{tool: "feedback", input: map[string]any{"trace_id": fresh.PendingID, "verdict": feedback.VerdictApprove}},
			want: trace.ErrNotReview.Error(),
		},
		{
			name: "outcome refuses a context trace that is not a review",
			args: args{tool: "outcome", input: map[string]any{"trace_id": fresh.PendingID, "result": feedback.ResultConfirmed}},
			want: trace.ErrNotReview.Error(),
		},
		{
			name: "outcome refuses an unknown trace",
			args: args{tool: "outcome", input: map[string]any{"trace_id": "nope", "result": feedback.ResultConfirmed}},
			want: trace.ErrNotFound.Error(),
		},
		{
			name: "outcome refuses an unknown result",
			args: args{tool: "outcome", input: map[string]any{"trace_id": reviewed.TraceID, "result": "maybe"}},
			want: feedback.ErrResultUnknown.Error(),
		},
		{
			name: "propose refuses a context trace as evidence",
			args: args{tool: "propose", input: map[string]any{
				"kind": knowledge.KindJudgment, "content": "check tracking first", "trace_ids": []string{fresh.PendingID},
			}},
			want: trace.ErrNotReview.Error(),
		},
		{
			name: "propose refuses an unknown trace as evidence",
			args: args{tool: "propose", input: map[string]any{
				"kind": knowledge.KindJudgment, "content": "check tracking first", "trace_ids": []string{"nope"},
			}},
			want: trace.ErrNotFound.Error(),
		},
		{
			name: "propose refuses a candidate without content",
			args: args{tool: "propose", input: map[string]any{
				"kind": knowledge.KindJudgment, "content": "", "trace_ids": []string{reviewed.TraceID},
			}},
			want: knowledge.ErrContentRequired.Error(),
		},
		{
			name: "approve refuses a missing approver name",
			args: args{tool: "approve", input: map[string]any{"id": "k-tracking", "version": 1, "approver": ""}},
			want: knowledge.ErrApproverRequired.Error(),
		},
		{
			name: "detail refuses a start that is not RFC3339",
			args: args{tool: "detail", input: map[string]any{"event_id": spikeEvent, "start": "yesterday"}},
			want: mcp.ErrTimeInvalid.Error() + `: start "yesterday"`,
		},
		{
			name: "detail refuses an end that is not RFC3339",
			args: args{tool: "detail", input: map[string]any{"event_id": spikeEvent, "end": "tomorrow"}},
			want: mcp.ErrTimeInvalid.Error() + `: end "tomorrow"`,
		},
		{
			name: "detail refuses an unknown event",
			args: args{tool: "detail", input: map[string]any{"event_id": "nope"}},
			want: evidence.ErrNotFound.Error(),
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := c.Run(t, tc.args.tool, tc.args.input)
			assert.ErrorIs(t, err, testkit.ErrTool)
			assert.ErrorContains(t, err, tc.want)
		})
	}
}
