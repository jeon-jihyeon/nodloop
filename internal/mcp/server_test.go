package mcp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/compact"
	"github.com/jeon-jihyeon/nodloop/internal/diagnose"
	"github.com/jeon-jihyeon/nodloop/internal/evidence"
	evidencefile "github.com/jeon-jihyeon/nodloop/internal/evidence/file"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	knowledgefile "github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	"github.com/jeon-jihyeon/nodloop/internal/loop"
	"github.com/jeon-jihyeon/nodloop/internal/mcp"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	vetofile "github.com/jeon-jihyeon/nodloop/internal/veto/file"
)

const (
	spikeEvent   = "tq-005"
	plannedEvent = "tq-009"
	// Shares the planned change context with plannedEvent
	relatedEvent   = "tq-011"
	unrelatedEvent = "tq-003"
	knownSegment   = "metric-anomaly-investigation#Metric anomaly investigation/Check the segment#1"
	// A review that cites the procedure paragraphs of every context and passes the gate
	reviewFile = "testdata/review.json"
)

// The server over the stores as the composition root builds it
// A test that needs another source or ledger replaces it in its copy of the stores
func connect(t *testing.T, st testkit.Stores, exe, dataArgs string) testkit.Client {
	t.Helper()
	policy := testkit.Policy(t)
	diagnoser := diagnose.New(st.Source, policy, nil, st.Traces, st.Feedback, st.Ledger, st.Clock.Now)
	compactor := compact.New(st.Source, st.Ledger, st.Traces, st.Feedback, st.Outcomes, st.Replays)
	srv := mcp.New(
		st.Source, policy, diagnoser, st.Traces, st.Feedback, st.Outcomes, st.Ledger, compactor, st.Clock.Now, "test", exe, dataArgs,
	)
	return testkit.Connect(t, srv.ServeTransport)
}

func TestServerTools(t *testing.T) {
	t.Parallel()
	st := testkit.Open(t)
	c := connect(t, st, "nodloop", "")

	assert.ElementsMatch(t, mcp.Tools(), c.Tools(t))
}

func TestServerEvents(t *testing.T) {
	type args struct {
		dataDir string
		input   map[string]any
	}
	dims := func(pairs ...string) map[string]any {
		named := map[string]string{}
		for i := 0; i < len(pairs); i += 2 {
			named[pairs[i]] = pairs[i+1]
		}
		return map[string]any{"dims": named}
	}
	type want struct {
		// Golden answer
		golden string
		// The refusal names the scope error and ends with the dimension names
		refused string
	}
	unobserved := "events dims filter: " + knowledge.ErrScopeUnobserved.Error() + ": "
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"demo events list their range and the dimension names once",
			args{testkit.DemoDir(t), map[string]any{}}, want{golden: "testdata/events.json"},
		},
		{"a data set without dimensions answers an empty name list", args{"testdata/nodims", map[string]any{}}, want{golden: "testdata/events-nodims.json"}},
		{
			"a filter on a data set without dimensions is refused naming none",
			args{"testdata/nodims", dims("source", "source-a")}, want{refused: unobserved + "dim source=source-a. Dimensions: none"},
		},
		{"rows carry no dimension values however many an event has", args{"testdata/dims", map[string]any{}}, want{golden: "testdata/events-dims.json"}},
		{"a value past the tenth still finds its event", args{"testdata/dims", dims("source", "source-11")}, want{golden: "testdata/events-dims-wide.json"}},
		{"the eleventh value finds its event", args{"testdata/dims", dims("source", "source-10")}, want{golden: "testdata/events-dims-wide.json"}},
		{"an early value finds its event", args{"testdata/dims", dims("source", "source-03")}, want{golden: "testdata/events-dims-wide.json"}},
		{
			"two values that each exist but never on one event find nothing",
			args{"testdata/dims", dims("source", "source-11", "topic", "finance")}, want{golden: "testdata/events-dims-none.json"},
		},
		{
			"a misspelled dimension name is refused naming the dimensions",
			args{"testdata/dims", dims("sources", "source-a")}, want{refused: unobserved + "dim sources=source-a. Dimensions: source, topic"},
		},
		{
			"a dimension name in the wrong case is refused naming the dimensions",
			args{"testdata/dims", dims("Source", "source-03")}, want{refused: unobserved + "dim Source=source-03. Dimensions: source, topic"},
		},
		{
			"a dimension no event carries is refused",
			args{"testdata/dims", dims("region", "eu")}, want{refused: unobserved + "dim region=eu. Dimensions: source, topic"},
		},
		{
			"a value no event carries is refused",
			args{"testdata/dims", dims("source", "source-99")}, want{refused: unobserved + "dim source=source-99. Dimensions: source, topic"},
		},
		{
			"two events that share their first ten values both list",
			args{"testdata/campaigns", map[string]any{}}, want{golden: "testdata/events-campaigns.json"},
		},
		{"a value past the tenth tells two events apart", args{"testdata/campaigns", dims("campaign", "c-12")}, want{golden: "testdata/events-campaigns-c12.json"}},
		{
			"the value only the other event carries finds that event",
			args{"testdata/campaigns", dims("campaign", "c-13")}, want{golden: "testdata/events-campaigns-c13.json"},
		},
		{
			"a dimension the events lack is refused naming the one they have",
			args{"testdata/campaigns", dims("source", "c-12")}, want{refused: unobserved + "dim source=c-12. Dimensions: campaign"},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := testkit.Open(t)
			src, err := evidencefile.New(tc.args.dataDir)
			require.NoError(t, err)
			st.Source = src
			c := connect(t, st, "nodloop", "")

			var got json.RawMessage
			err = c.Call(t, "events", tc.args.input, &got)

			if tc.want.refused != "" {
				assert.ErrorIs(t, err, testkit.ErrTool)
				assert.True(t, strings.HasSuffix(fmt.Sprint(err), tc.want.refused), fmt.Sprint(err))
				return
			}
			require.NoError(t, err)
			want, err := os.ReadFile(tc.want.golden)
			require.NoError(t, err)
			assert.JSONEq(t, string(want), string(got))
		})
	}
}

// The answers are captured from the default policy over the demo data
func TestServerObserve(t *testing.T) {
	t.Parallel()
	st := testkit.Open(t)
	c := connect(t, st, "nodloop", "")
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
	c := connect(t, st, "nodloop", "")

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
	c := connect(t, st, "nodloop", "")
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
	scope := knowledge.Scope{Scope: evidence.Scope{ChangeContexts: []evidence.Context{evidence.ContextPlannedChange}}}
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
	c := connect(t, st, "nodloop", "")
	var opened struct {
		PendingID string `json:"pending_id"`
	}
	require.NoError(t, c.Call(t, "context", map[string]any{"event_id": spikeEvent}, &opened))
	incomplete := diagnose.Diagnosis{
		Status:        evidence.StatusReadyForReview,
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

// The input schema lists the valid statuses so a typo is refused before anything is recorded
// The corrected review of the same context is then recorded
func TestServerRecordRefusesUnknownStatus(t *testing.T) {
	t.Parallel()
	st := testkit.Open(t)
	c := connect(t, st, "nodloop", "")
	b, err := os.ReadFile(reviewFile)
	require.NoError(t, err)
	var review diagnose.Diagnosis
	require.NoError(t, json.Unmarshal(b, &review))
	tcs := []struct {
		name string
		args evidence.Status
	}{
		{name: "a dashed status", args: "ready-for-review"},
		{name: "a capitalized status", args: "Hold"},
		{name: "an empty status", args: ""},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var opened struct {
				PendingID string `json:"pending_id"`
			}
			require.NoError(t, c.Call(t, "context", map[string]any{"event_id": spikeEvent}, &opened))
			typo := review
			typo.Status = tc.args

			refused := c.Run(t, "record", map[string]any{"pending_id": opened.PendingID, "diagnosis": typo})
			var recorded struct {
				Recorded   bool `json:"recorded"`
				ForcedHold bool `json:"forced_hold"`
			}
			require.NoError(t, c.Call(t, "record", map[string]any{"pending_id": opened.PendingID, "diagnosis": review}, &recorded))
			assert.ErrorIs(t, refused, testkit.ErrTool)
			assert.ErrorContains(t, refused, "status")
			assert.True(t, recorded.Recorded)
			assert.False(t, recorded.ForcedHold)
		})
	}
}

func TestServerRecord(t *testing.T) {
	t.Parallel()
	st := testkit.Open(t)
	c := connect(t, st, "nodloop", "")
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
	c := connect(t, st, "nodloop", "")
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
				Verdict: feedback.VerdictEdit, Reason: "it was a launch", Edited: edited, Reviewer: feedback.ReviewerAuthor,
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
			// A correction of the other case may already be offered as an example of the same event
			selectInput := map[string]any{"pending_id": opened.PendingID, "knowledge": []any{}, "examples": []any{}}
			require.NoError(t, c.Run(t, "select", selectInput))
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
	c := connect(t, st, "nodloop", "")
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
				Result: feedback.ResultConfirmed, ConfirmedCause: "campaign launch", Reviewer: feedback.ReviewerAuthor,
			},
		},
		{
			name: "an inconclusive check stores its note",
			args: args{result: feedback.ResultInconclusive, note: "logs expired"},
			want: feedback.Outcome{
				Result: feedback.ResultInconclusive, Note: "logs expired", Reviewer: feedback.ReviewerAuthor,
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
	c := connect(t, st, "nodloop", "")
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
		// A change context of its own per case so no two cases share a folder or overlap
		context evidence.Context
		// The veto input as a client sends it
		veto map[string]any
	}
	type folder struct {
		Chars  int      `json:"chars"`
		Budget int      `json:"budget"`
		Full   bool     `json:"full"`
		Items  []string `json:"items"`
	}
	type proposal struct {
		ID       string                `json:"id"`
		Version  int                   `json:"version"`
		Status   knowledge.Status      `json:"status"`
		Overlaps []knowledge.Knowledge `json:"overlaps"`
		Folder   folder                `json:"folder"`
		Veto     *knowledge.Veto       `json:"veto"`
		Drafted  bool                  `json:"drafted"`
	}
	type approval struct {
		ID       string           `json:"id"`
		Version  int              `json:"version"`
		Status   knowledge.Status `json:"status"`
		Approver string           `json:"approver"`
		Veto     bool             `json:"veto"`
	}
	type want struct {
		proposed proposal
		approved approval
		author   string
	}
	sed := &knowledge.Veto{
		Tool:    "Bash",
		When:    []knowledge.VetoCondition{{Field: "command", Match: `sed\s+-i`}},
		Example: map[string]any{"command": "sed -i s/a/b/ f"},
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			name: "a candidate without an author is proposed by claude and approved by the named person",
			args: args{id: "k-tracking", kind: knowledge.KindJudgment, context: evidence.ContextNoKnownChange},
			want: want{
				proposed: proposal{
					ID: "k-tracking", Version: 1, Status: knowledge.StatusCandidate, Overlaps: []knowledge.Knowledge{},
					Folder: folder{Chars: 144, Budget: knowledge.ReviewChars, Items: []string{}}, Drafted: true,
				},
				approved: approval{ID: "k-tracking", Version: 1, Status: knowledge.StatusApproved, Approver: "reviewer"},
				author:   "claude",
			},
		},
		{
			name: "a candidate keeps the author the caller names",
			args: args{id: "k-meaning", kind: knowledge.KindMeaning, author: "user", context: evidence.ContextMeasurementChanged},
			want: want{
				proposed: proposal{
					ID: "k-meaning", Version: 1, Status: knowledge.StatusCandidate, Overlaps: []knowledge.Knowledge{},
					Folder: folder{Chars: 154, Budget: knowledge.ReviewChars, Items: []string{}}, Drafted: true,
				},
				approved: approval{ID: "k-meaning", Version: 1, Status: knowledge.StatusApproved, Approver: "reviewer"},
				author:   "user",
			},
		},
		{
			name: "a judgment with a veto shows the veto before approval and says so after",
			args: args{id: "k-no-sed", kind: knowledge.KindJudgment, context: evidence.ContextDataAvailability, veto: map[string]any{
				"tool":    "Bash",
				"when":    []map[string]any{{"field": "command", "match": `sed\s+-i`}},
				"example": map[string]any{"command": "sed -i s/a/b/ f"},
			}},
			want: want{
				proposed: proposal{
					ID: "k-no-sed", Version: 1, Status: knowledge.StatusCandidate, Overlaps: []knowledge.Knowledge{},
					Folder: folder{Chars: 150, Budget: knowledge.ReviewChars, Items: []string{}}, Veto: sed, Drafted: true,
				},
				approved: approval{
					ID: "k-no-sed", Version: 1, Status: knowledge.StatusApproved, Approver: "reviewer", Veto: true,
				},
				author: "claude",
			},
		},
	}
	// One ledger serves every case and each answer counts the items earlier cases approved so the cases run in turn
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			scope := knowledge.Scope{
				Scope: evidence.Scope{ChangeContexts: []evidence.Context{tc.args.context}, Metrics: []string{"click_count"}},
				Dims:  map[string]string{"platform": "ios"},
			}
			in := map[string]any{
				"id": tc.args.id, "kind": tc.args.kind, "content": "after a planned change check tracking first",
				"change_contexts": scope.ChangeContexts, "metrics": scope.Metrics, "dims": scope.Dims, "trace_ids": []string{reviewed.TraceID},
				"author": tc.args.author, "veto": tc.args.veto,
			}
			var proposed proposal
			require.NoError(t, c.Call(t, "propose", in, &proposed))
			var approved approval
			approve := map[string]any{"id": tc.args.id, "version": 1, "approver": "reviewer"}
			require.NoError(t, c.Call(t, "approve", approve, &approved))
			stored, err := st.Ledger.Approved(ctx, tc.args.id, 1)
			require.NoError(t, err)

			assert.Equal(t, tc.want.proposed, proposed)
			assert.Equal(t, tc.want.approved, approved)
			assert.Equal(t, knowledge.Knowledge{
				ID: tc.args.id, Version: 1, Kind: tc.args.kind, Content: "after a planned change check tracking first",
				Scope: scope, Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{reviewed.TraceID}},
				Basis: knowledge.BasisStated, Status: knowledge.StatusApproved, Approver: "reviewer",
				ApprovedAt: stored.ApprovedAt, Time: stored.Time, Author: tc.want.author, Veto: tc.want.proposed.Veto, Drafted: true,
			}, stored)
		})
	}
}

// A proposal from a corrected review takes its scope and evidence from code and its content from the conversation
// The planned event moved conversion_count under a planned change
func TestServerProposeFrom(t *testing.T) {
	type args struct {
		verdict feedback.Verdict
		edited  json.RawMessage
		// Scope fields sent beside from
		metrics []string
	}
	cleared := json.RawMessage(`{"status":"no_action","observations":[],"causes":[],"checks":[],"open_questions":[]}`)
	tcs := []struct {
		name string
		args args
		want []string
	}{
		{
			"an edited review fills scope and evidence",
			args{verdict: feedback.VerdictEdit, edited: cleared},
			[]string{"conversion_count"},
		},
		{
			"a metric sent beside from replaces the filled metrics",
			args{verdict: feedback.VerdictReject, metrics: []string{"click_count"}},
			[]string{"click_count"},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := testkit.Open(t)
			c := connect(t, st, "nodloop", "")
			b, err := os.ReadFile(reviewFile)
			require.NoError(t, err)
			var opened struct {
				PendingID string `json:"pending_id"`
			}
			require.NoError(t, c.Call(t, "context", map[string]any{"event_id": plannedEvent}, &opened))
			var reviewed struct {
				TraceID string `json:"trace_id"`
			}
			recordInput := map[string]any{"pending_id": opened.PendingID, "diagnosis": json.RawMessage(b)}
			require.NoError(t, c.Call(t, "record", recordInput, &reviewed))
			require.NoError(t, c.Run(t, "feedback", map[string]any{
				"trace_id": reviewed.TraceID, "verdict": tc.args.verdict, "reason": "planned tracking change", "edited": tc.args.edited,
			}))
			in := map[string]any{
				"kind": knowledge.KindMeaning, "content": "a planned tracking change moves the counts", "from": reviewed.TraceID,
				"metrics": tc.args.metrics,
			}
			var proposed struct {
				ID      string          `json:"id"`
				Scope   knowledge.Scope `json:"scope"`
				Drafted bool            `json:"drafted"`
			}

			require.NoError(t, c.Call(t, "propose", in, &proposed))

			stored, err := st.Ledger.History(ctx, proposed.ID)
			require.NoError(t, err)
			require.Len(t, stored, 1)
			planned := []evidence.Context{evidence.ContextPlannedChange}
			assert.Equal(t, knowledge.Scope{Scope: evidence.Scope{ChangeContexts: planned, Metrics: tc.want}}, proposed.Scope)
			assert.True(t, proposed.Drafted)
			assert.Equal(t, knowledge.Evidence{FeedbackTraceIDs: []string{reviewed.TraceID}}, stored[0].Evidence)
			assert.Equal(t, knowledge.BasisStated, stored[0].Basis)
		})
	}
}

// Once the approval is recorded a later failure rides on the answer because approving again would fail
// The flaky store lets approve read the records and export the vetoes and fails the folder read after them
func TestServerApproveKeepsTheApproval(t *testing.T) {
	type args struct {
		// Files under the veto home keyed by relative path
		// A file named .claude blocks the veto export
		files map[string]string
	}
	type want struct {
		keys   []string
		status knowledge.Status
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"a failed folder read answers the approval and the folder error",
			args{},
			want{[]string{"id", "version", "status", "approver", "veto", "folder_error"}, knowledge.StatusApproved},
		},
		{
			"a failed veto export and folder read answer the approval and both errors",
			args{map[string]string{".claude": ""}},
			want{[]string{"id", "version", "status", "approver", "veto", "veto_export_error", "folder_error"}, knowledge.StatusApproved},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := testkit.Open(t)
			dir, home := t.TempDir(), t.TempDir()
			for rel, content := range tc.args.files {
				require.NoError(t, os.WriteFile(filepath.Join(home, rel), []byte(content), 0o600))
			}
			items, err := knowledgefile.New(dir)
			require.NoError(t, err)
			require.NoError(t, items.Append(ctx, knowledge.Knowledge{
				ID: "k-lag", Version: 1, Kind: knowledge.KindMeaning, Content: "conversions lag clicks",
				Evidence: knowledge.Evidence{ParagraphIDs: []string{knownSegment}}, Basis: knowledge.BasisStated,
				Status: knowledge.StatusCandidate, Author: "author", Time: st.Clock.Now(),
			}))
			flaky := &testkit.FlakyKnowledge{Store: items, Reads: testkit.Reads{Allowed: 2, Err: assert.AnError}}
			st.Ledger = knowledge.NewLedger(flaky, vetofile.NewApprovedFile(home, dir), st.Clock.Now, func(p string) string { return p })
			c := connect(t, st, "nodloop", "")
			var got map[string]any

			require.NoError(t, c.Call(t, "approve", map[string]any{"id": "k-lag", "version": 1, "approver": "jed"}, &got))

			all, err := items.List(ctx)
			require.NoError(t, err)
			require.NotEmpty(t, all)
			assert.ElementsMatch(t, tc.want.keys, slices.Collect(maps.Keys(got)))
			assert.Equal(t, string(tc.want.status), got["status"])
			assert.Equal(t, tc.want.status, all[0].Status)
		})
	}
}

// One server so the second proposal sees the first approval in its folder
func TestServerProposeFolder(t *testing.T) {
	t.Parallel()
	st := testkit.Open(t)
	c := connect(t, st, "nodloop", "")
	type folder struct {
		Chars         int              `json:"chars"`
		Budget        int              `json:"budget"`
		ItemBudget    int              `json:"item_budget"`
		Full          bool             `json:"full"`
		Items         []string         `json:"items"`
		ChangeContext evidence.Context `json:"change_context"`
		CompactionDue bool             `json:"compaction_due"`
	}
	quiet := evidence.ContextNoKnownChange
	// A fresh answer per call so a decoded list never shares an earlier one
	call := func(tool string, in map[string]any) (folder, error) {
		var answer struct {
			Folder folder `json:"folder"`
		}
		err := c.Call(t, tool, in, &answer)
		return answer.Folder, err
	}
	scope := []string{"conversion_count"}
	item := func(id, content string) map[string]any {
		return map[string]any{"id": id, "kind": "meaning", "content": content, "metrics": scope, "paragraph_ids": []string{"p#1"}}
	}
	approve := func(id string) map[string]any { return map[string]any{"id": id, "version": 1, "approver": "jed"} }
	_, err := call("propose", item("k-first", "clicks count once per session"))
	require.NoError(t, err)
	first, err := call("approve", approve("k-first"))
	require.NoError(t, err)

	second, err := call("propose", item("k-second", "conversions arrive late"))
	require.NoError(t, err)
	// Its text alone nearly fills the budget so the folder with k-first overflows it
	full, err := call("propose", item("k-large", strings.Repeat("x", knowledge.ReviewChars-50)))
	require.NoError(t, err)
	_, refused := call("approve", approve("k-large"))
	// Items an event can replay count toward a compaction and items that cite only paragraphs never do
	replayable := func(id string, status knowledge.Status) knowledge.Knowledge {
		k := knowledge.Knowledge{
			ID: id, Version: 1, Kind: knowledge.KindMeaning, Content: "item " + id,
			Scope:    knowledge.Scope{Scope: evidence.Scope{Metrics: scope}},
			Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{"t-" + id}}, Basis: knowledge.BasisStated,
			Status: status, Author: "author", Time: st.Clock.Now(),
		}
		if status == knowledge.StatusApproved {
			k.Approver, k.ApprovedAt = "ann", st.Clock.Now()
		}
		return k
	}
	// Each replayable item cites an approved review so its event has an expected status and a compaction could pass
	for _, id := range []string{"k-b", "k-c", "k-d", "k-e", "k-f", "k-g"} {
		require.NoError(t, st.Traces.Append(context.Background(), trace.Trace{
			ID: "t-" + id, Name: trace.NameDiagnose, Subject: "e-" + id, Time: st.Clock.Now(),
			Output: json.RawMessage(`{"status":"hold"}`),
		}))
		fb, err := feedback.New("t-"+id, feedback.VerdictApprove, "right", nil, "", st.Clock.Now())
		require.NoError(t, err)
		require.NoError(t, st.Feedback.Append(context.Background(), fb))
	}
	require.NoError(t, testkit.Err(st.Ledger.Import(context.Background(), []knowledge.Knowledge{
		replayable("k-b", knowledge.StatusApproved), replayable("k-c", knowledge.StatusApproved),
		replayable("k-d", knowledge.StatusApproved), replayable("k-e", knowledge.StatusApproved),
		replayable("k-f", knowledge.StatusCandidate), replayable("k-g", knowledge.StatusCandidate),
	})))
	fifth, err := call("approve", approve("k-f"))
	require.NoError(t, err)
	sixth, err := call("approve", approve("k-g"))
	require.NoError(t, err)
	paragraphOnly, err := call("approve", approve("k-second"))
	require.NoError(t, err)

	assert.Equal(t, folder{Chars: 84, Budget: knowledge.ReviewChars, ItemBudget: knowledge.ReviewItems, Items: []string{}, ChangeContext: quiet}, first)
	assert.Equal(t, folder{Chars: 163, Budget: knowledge.ReviewChars, ItemBudget: knowledge.ReviewItems, Items: []string{"k-first"}, ChangeContext: quiet}, second)
	assert.Equal(t, folder{
		Chars: 70089, Budget: knowledge.ReviewChars, ItemBudget: knowledge.ReviewItems, Full: true, Items: []string{"k-first"},
		ChangeContext: quiet,
	}, full)
	assert.ErrorIs(t, refused, testkit.ErrTool)
	assert.EqualError(t, refused, testkit.ErrTool.Error()+
		": knowledge: folder may outgrow the review: 70089 of 70000 chars 2 of 10 items in no_known_change with k-first v1 84 chars")
	assert.False(t, fifth.CompactionDue)
	assert.Equal(t, []string{"k-b", "k-c", "k-d", "k-e", "k-f", "k-first"}, sixth.Items)
	assert.True(t, sixth.CompactionDue)
	assert.False(t, paragraphOnly.CompactionDue)
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
			st.Source = src
			c := connect(t, st, "nodloop", "")
			want, err := os.ReadFile(tc.want)
			require.NoError(t, err)

			var got json.RawMessage
			require.NoError(t, c.Call(t, "detail", tc.args.input, &got))
			assert.JSONEq(t, string(want), string(got))
		})
	}
}

// A context a batch run left open when it was interrupted
var batch = trace.Trace{
	ID: "batch", Name: trace.NameContext, SessionID: "s3", Subject: "tq-012", Tags: []string{"feedback:off"},
	Input: json.RawMessage(`{"mode":"batch"}`), Output: json.RawMessage(`{}`),
}

// Recording a context closes it and a context a batch run left open is never listed
func TestServerPending(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := testkit.Open(t)
	require.NoError(t, st.Traces.Append(ctx, batch))
	c := connect(t, st, "nodloop", "")
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
	c := connect(t, st, "nodloop", "")
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
	require.NoError(t, c.Run(t, "feedback", map[string]any{"trace_id": reviewed.TraceID, "verdict": feedback.VerdictApprove}))
	var fresh struct {
		PendingID string `json:"pending_id"`
	}
	require.NoError(t, c.Call(t, "context", map[string]any{"event_id": relatedEvent}, &fresh))
	require.NoError(t, st.Traces.Append(context.Background(), batch))
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
			want: diagnose.ErrRecorded.Error() + ": " + opened.PendingID + " by trace " + reviewed.TraceID,
		},
		{
			name: "record refuses a context an interrupted batch run left open",
			args: args{tool: "record", input: map[string]any{"pending_id": batch.ID, "diagnosis": review}},
			want: diagnose.ErrBatchContext.Error() + ": " + batch.ID + " of tq-012 in session s3 so run the batch command " +
				"that built it again for the event: nodloop diagnose --event tq-012 or nodloop eval seed or holdout " +
				"of that session with --events tq-012",
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
			name: "propose refuses from a review that was approved and never corrected",
			args: args{tool: "propose", input: map[string]any{
				"kind": knowledge.KindMeaning, "content": "a planned tracking change moves the counts", "from": reviewed.TraceID,
			}},
			want: diagnose.ErrNotCorrected.Error(),
		},
		{
			name: "approve refuses a missing approver name",
			args: args{tool: "approve", input: map[string]any{"id": "k-tracking", "version": 1, "approver": ""}},
			want: knowledge.ErrApproverRequired.Error(),
		},
		{
			name: "reaffirm refuses a missing approver name",
			args: args{tool: "reaffirm", input: map[string]any{"id": "k-tracking", "version": 1, "approver": ""}},
			want: knowledge.ErrApproverRequired.Error(),
		},
		{
			name: "queue refuses an audit rate above one",
			args: args{tool: "queue", input: map[string]any{"audit_rate": 2}},
			want: loop.ErrQueueOptions.Error(),
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
