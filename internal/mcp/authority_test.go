package mcp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/compact"
	"github.com/jeon-jihyeon/nodloop/internal/extract"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/mcp"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// Only a key that proves a person approves and every other call records an agent proposal
// No call sends a form whatever elicitation the client declares
func TestServerApproveProof(t *testing.T) {
	type args struct {
		// The options of the client given the flag its form handler sets
		options  func(asked *atomic.Bool) *sdk.ClientOptions
		caller   mcp.Caller
		approver string
	}
	type want struct {
		err      error
		errText  string
		status   string
		approver string
		proposal bool
		// The repos whose runs get the item
		repos []string
		// The authority and proof of each intervention recorded and the person it names
		interventions []string
	}
	key, person := mcp.Caller{Person: "ann"}, mcp.Caller{Person: "ann", Proves: true}
	proposal := want{status: "candidate", proposal: true, interventions: []string{"agent none "}}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a client without elicitation records a proposal", args{plainClient, mcp.Caller{}, "ann"}, proposal},
		{"a client that shows forms records a proposal", args{elicitingClient, mcp.Caller{}, "ann"}, proposal},
		{"a key without --person records a proposal", args{elicitingClient, key, ""}, proposal},
		{"a key that proves a person approves under its name", args{elicitingClient, person, "sam"},
			want{status: "approved", approver: "ann", repos: []string{"nodloop"}, interventions: []string{"person key ann"}}},
		{"a call without a name is refused", args{plainClient, mcp.Caller{}, ""},
			want{err: testkit.ErrTool, errText: knowledge.ErrApproverRequired.Error()}},
		{"the reserved name is refused", args{plainClient, mcp.Caller{}, knowledge.ApproverNodloop},
			want{err: testkit.ErrTool, errText: knowledge.ErrApproverReserved.Error()}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var asked atomic.Bool
			st := testkit.Open(t)
			c := testkit.ConnectWith(t, serve(st, tc.args.caller), tc.args.options(&asked), "")
			candidate(t, c)
			var got struct {
				Status   string `json:"status"`
				Approver string `json:"approver"`
				Proposal string `json:"proposal"`
			}

			err := c.Call(t, "approve", map[string]any{"id": "short", "version": 1, "approver": tc.args.approver}, &got)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Contains(t, errText(err), tc.want.errText)
			assert.False(t, asked.Load())
			assert.Equal(t, tc.want.status, got.Status)
			assert.Equal(t, tc.want.approver, got.Approver)
			assert.Equal(t, tc.want.proposal, got.Proposal != "")
			assert.Equal(t, tc.want.repos, reached(t, c))
			assert.Equal(t, tc.want.interventions, interventions(t, st))
		})
	}
}

// Options of a client that declares no elicitation
func plainClient(*atomic.Bool) *sdk.ClientOptions {
	return &sdk.ClientOptions{}
}

// Options of a client that declares form elicitation and records whether the server asked a form
func elicitingClient(asked *atomic.Bool) *sdk.ClientOptions {
	return &sdk.ClientOptions{
		Capabilities: &sdk.ClientCapabilities{Elicitation: &sdk.ElicitationCapabilities{Form: &sdk.FormElicitationCapabilities{}}},
		ElicitationHandler: func(context.Context, *sdk.ElicitRequest) (*sdk.ElicitResult, error) {
			asked.Store(true)
			return &sdk.ElicitResult{Action: "decline"}, nil
		},
	}
}

// Serves the stores to the caller over every tool
func serve(st testkit.Stores, caller mcp.Caller) func(context.Context, sdk.Transport) error {
	return mcp.NewHost(open(st, caller), "test", "").ServeTransport
}

// Opens one server over the stores for the caller
func open(st testkit.Stores, caller mcp.Caller) mcp.Open {
	compactor := compact.New(st.Ledger, st.Traces, st.Feedback, st.Traces, st.Clock.Now)
	srv := mcp.New(st.Traces, st.Feedback, st.Outcomes, st.Ledger, st.Source(), compactor, extract.New(st.Ledger, st.Traces, st.Feedback, st.Clock.Now),
		st.Clock.Now, mcp.NewSession(st.Clock.Now()), "nodloop", "", caller, mcp.Policy{})
	return func(context.Context) (*mcp.Server, error) { return srv, nil }
}

// The authority, the proof and the person of every intervention recorded oldest first
func interventions(t *testing.T, st testkit.Stores) []string {
	t.Helper()
	found, err := st.Traces.List(t.Context(), record.TraceFilter{Name: string(trace.NameIntervention)})
	require.NoError(t, err)
	var out []string
	for _, tr := range slices.Backward(found) {
		var in trace.Intervention
		require.NoError(t, json.Unmarshal(tr.Input, &in))
		out = append(out, fmt.Sprintf("%s %s %s", in.Authority, in.Proof, in.Person))
	}
	return out
}

// The candidate short proposed for repo nodloop from a rejected run while a run of repo other is recorded
func candidate(t *testing.T, c testkit.Client) {
	t.Helper()
	runID := recordRun(t, c, "nodloop")
	recordRun(t, c, "other")
	require.NoError(t, c.Run(t, "feedback", map[string]any{
		"trace_id": runID, "verdict": "reject", "reason_code": "form", "reason": "too long", "reviewer": "ann",
	}))
	require.NoError(t, c.Run(t, "propose", map[string]any{"id": "short", "kind": "judgment", "content": "give only the conclusion", "from": runID}))
}

// The repos of nodloop and other whose runs get an item
func reached(t *testing.T, c testkit.Client) []string {
	t.Helper()
	var repos []string
	for _, repo := range []string{"nodloop", "other"} {
		var got struct {
			Items []any `json:"items"`
		}
		require.NoError(t, c.Call(t, "knowledge_for", map[string]any{"producer": "session", "labels": map[string]any{"repo": []any{repo}}}, &got))
		if len(got.Items) > 0 {
			repos = append(repos, repo)
		}
	}
	return repos
}

// The agent server lists the reviewer role and records every verdict and outcome as the session's
func TestServerAgent(t *testing.T) {
	st := testkit.Open(t)
	c := testkit.Connect(t, mcp.NewHost(open(st, mcp.Caller{Agent: true}), "test", "").ServeAgent)
	runID := recordRun(t, c, "nodloop")
	tcs := []struct {
		name string
		tool string
		args map[string]any
		want string
	}{
		{"a verdict naming a person is the session's", "feedback",
			map[string]any{"trace_id": runID, "verdict": "approve", "reviewer": "ann"}, feedback.ReviewerSession},
		{"a verdict naming nobody is the session's", "feedback", map[string]any{"trace_id": runID, "verdict": "approve"}, feedback.ReviewerSession},
		{"a verdict naming the capture is the session's", "feedback",
			map[string]any{"trace_id": runID, "verdict": "approve", "reviewer": feedback.ReviewerNodloop}, feedback.ReviewerSession},
		{"an outcome naming a person is the session's", "outcome",
			map[string]any{"trace_id": runID, "result": "confirmed", "reviewer": "ann"}, feedback.ReviewerSession},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			var got struct {
				Reviewer string `json:"reviewer"`
			}

			err := c.Call(t, tc.tool, tc.args, &got)

			assert.NoError(t, err)
			assert.Equal(t, tc.want, got.Reviewer)
		})
	}
	listed := c.Tools(t)
	assert.ElementsMatch(t, mcp.RoleReviewer.Tools(), listed)
	assert.NotContains(t, listed, "approve")
	assert.NotContains(t, listed, "reaffirm")
	assert.NotContains(t, listed, "approve_compaction")
}

// Over stdio a verdict and an outcome name their reviewer and under a key they take its name
func TestServerReviewerRequired(t *testing.T) {
	type args struct {
		caller mcp.Caller
		tool   string
		in     map[string]any
	}
	type want struct {
		reviewer string
		errText  string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a verdict over stdio without a reviewer is refused", args{mcp.Caller{}, "feedback", map[string]any{"verdict": "approve"}},
			want{errText: feedback.ErrReviewerRequired.Error()}},
		{"an outcome over stdio without a reviewer is refused", args{mcp.Caller{}, "outcome", map[string]any{"result": "confirmed"}},
			want{errText: feedback.ErrReviewerRequired.Error()}},
		{"a verdict over stdio keeps its reviewer", args{mcp.Caller{}, "feedback", map[string]any{"verdict": "approve", "reviewer": "ann"}},
			want{reviewer: "ann"}},
		{"a verdict under a key takes the key name", args{mcp.Caller{Person: "ci"}, "feedback", map[string]any{"verdict": "approve"}},
			want{reviewer: "ci"}},
		{"a verdict over stdio under the reserved name of the capture is refused",
			args{mcp.Caller{}, "feedback", map[string]any{"verdict": "reject", "reason": "wrong", "reviewer": "nodloop"}},
			want{errText: feedback.ErrReviewerReserved.Error()}},
		{"an outcome over stdio under the reserved name is refused",
			args{mcp.Caller{}, "outcome", map[string]any{"result": "confirmed", "reviewer": "nodloop"}},
			want{errText: feedback.ErrReviewerReserved.Error()}},
		{"a verdict under a key that proves a person is refused under the reserved name",
			args{mcp.Caller{Person: "ann", Proves: true}, "feedback", map[string]any{"verdict": "approve", "reviewer": "nodloop"}},
			want{errText: feedback.ErrReviewerReserved.Error()}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := testkit.Connect(t, serve(testkit.Open(t), tc.args.caller))
			in := map[string]any{"trace_id": recordRun(t, c, "nodloop")}
			for k, v := range tc.args.in {
				in[k] = v
			}
			var got struct {
				Reviewer string `json:"reviewer"`
			}

			err := c.Call(t, tc.args.tool, in, &got)

			assert.Contains(t, errText(err), tc.want.errText)
			assert.Equal(t, tc.want.reviewer, got.Reviewer)
		})
	}
}

// A named verdict carries the proof of its call and an inferred one carries none
func TestServerFeedbackProof(t *testing.T) {
	type want struct {
		reviewer, proof string
	}
	tcs := []struct {
		name string
		args mcp.Caller
		want want
	}{
		{"over stdio a named verdict proves nobody", mcp.Caller{}, want{"ann", "none"}},
		{"under a key without --person a named verdict proves nobody", mcp.Caller{Person: "ci"}, want{"ann", "none"}},
		{"under a key that proves a person the verdict carries the key", mcp.Caller{Person: "ann", Proves: true}, want{"ann", "key"}},
		{"on the agent server the verdict is the session's with no proof", mcp.Caller{Agent: true}, want{feedback.ReviewerSession, ""}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := testkit.Open(t)
			c := testkit.Connect(t, serve(st, tc.args))
			runID := recordRun(t, c, "nodloop")

			err := c.Run(t, "feedback", map[string]any{"trace_id": runID, "verdict": "approve", "reviewer": "ann"})

			require.NoError(t, err)
			found, err := st.Feedback.List(t.Context(), record.FeedbackFilter{})
			require.NoError(t, err)
			require.Len(t, found, 1)
			assert.Equal(t, tc.want, want{found[0].Reviewer, found[0].Proof})
		})
	}
}

// reaffirm and approve_compaction decide only under a key that proves a person and record a proposal otherwise
func TestServerJudgmentProof(t *testing.T) {
	type args struct {
		caller   mcp.Caller
		approver string
	}
	type want struct {
		errText string
		// Whether the answer names a proposal and whether the ledger changed
		proposal bool
		changed  bool
		// The authority, the proof and the person of each intervention the call recorded
		interventions []string
	}
	proposal := want{proposal: true, interventions: []string{"agent none "}}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"over stdio a proposal is recorded", args{mcp.Caller{}, "ann"}, proposal},
		{"under a key without --person a proposal is recorded", args{mcp.Caller{Person: "ci"}, ""}, proposal},
		{"under a key that proves a person it decides", args{mcp.Caller{Person: "ann", Proves: true}, ""},
			want{changed: true, interventions: []string{"person key ann"}}},
		{"the reserved name is refused", args{mcp.Caller{}, knowledge.ApproverNodloop},
			want{errText: knowledge.ErrApproverReserved.Error(), interventions: []string{}}},
	}
	tools := []struct {
		name string
		args func(compaction string) (string, map[string]any)
	}{
		{"reaffirm", func(string) (string, map[string]any) {
			return "reaffirm", map[string]any{"id": "a", "version": 1}
		}},
		{"approve_compaction", func(compaction string) (string, map[string]any) {
			return "approve_compaction", map[string]any{"compaction": compaction}
		}},
	}
	for _, tool := range tools {
		for _, tc := range tcs {
			t.Run(tool.name+": "+tc.name, func(t *testing.T) {
				t.Parallel()
				st := testkit.Open(t)
				compaction := checkedCompaction(t, st)
				before, err := st.Ledger.All(t.Context())
				require.NoError(t, err)
				c := testkit.Connect(t, serve(st, tc.args.caller))
				name, in := tool.args(compaction)
				in["approver"] = tc.args.approver
				var got struct {
					Proposal string `json:"proposal"`
				}

				err = c.Call(t, name, in, &got)

				assert.Contains(t, errText(err), tc.want.errText)
				assert.Equal(t, tc.want.proposal, got.Proposal != "")
				after, err := st.Ledger.All(t.Context())
				require.NoError(t, err)
				assert.Equal(t, tc.want.changed, len(after) != len(before))
				assert.Equal(t, tc.want.interventions, interventions(t, st)[2:])
			})
		}
	}
}

// Approved items a and b of one place and a compaction of them whose coverage passed
// The approvals come under a key of ann that proves her and leave one intervention each before the call under test
func checkedCompaction(t *testing.T, st testkit.Stores) string {
	t.Helper()
	c := testkit.Connect(t, serve(st, mcp.Caller{Person: "ann", Proves: true}))
	runID := recordRun(t, c, "nodloop")
	for _, id := range []string{"a", "b"} {
		require.NoError(t, c.Run(t, "propose", map[string]any{
			"id": id, "kind": "judgment", "content": "rule " + id, "trace_ids": []any{runID},
			"producer": "session", "labels": map[string]any{"repo": []any{"nodloop"}},
		}))
	}
	require.NoError(t, c.Run(t, "approve", map[string]any{"id": "a", "version": 1, "approver": "ann"}))
	require.NoError(t, c.Run(t, "approve", map[string]any{"id": "b", "version": 1, "approver": "ann"}))
	var proposed struct {
		Compaction string `json:"compaction"`
	}
	require.NoError(t, c.Call(t, "propose_compaction", map[string]any{"anchor": "a", "items": []any{map[string]any{
		"id": "a", "kind": "judgment", "content": "rule a and rule b", "from": []any{"a", "b"},
		"producer": "session", "labels": map[string]any{"repo": []any{"nodloop"}},
	}}}, &proposed))
	covered := []any{map[string]any{"old": "a", "covered_by": []any{"a"}}, map[string]any{"old": "b", "covered_by": []any{"a"}}}
	require.NoError(t, c.Run(t, "check_compaction", map[string]any{"compaction": proposed.Compaction, "items": covered}))
	found, err := st.Traces.List(t.Context(), record.TraceFilter{Name: "intervention"})
	require.NoError(t, err)
	require.Len(t, found, 2)
	return proposed.Compaction
}
