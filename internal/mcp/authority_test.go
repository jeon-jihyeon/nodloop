package mcp_test

import (
	"context"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"

	"github.com/jeon-jihyeon/nodloop/internal/compact"
	"github.com/jeon-jihyeon/nodloop/internal/extract"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/mcp"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
)

// Serves the stores to the caller over every tool
func serve(st testkit.Stores, caller mcp.Caller) func(context.Context, sdk.Transport) error {
	return mcp.NewHost(open(st, caller), "test", "").ServeTransport
}

// Opens one server over the stores for the caller
func open(st testkit.Stores, caller mcp.Caller) mcp.Open {
	compactor := compact.New(st.Ledger, st.Traces, st.Feedback, st.Traces, st.Clock.Now)
	srv := mcp.New(st.Traces, st.Feedback, st.Outcomes, st.Ledger, compactor, extract.New(st.Ledger, st.Traces, st.Feedback, st.Clock.Now),
		st.Clock.Now, mcp.NewSession(st.Clock.Now()), "nodloop", "", caller, mcp.Policy{})
	return func(context.Context) (*mcp.Server, error) { return srv, nil }
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
		{"a verdict over stdio keeps its reviewer", args{mcp.Caller{}, "feedback", map[string]any{"verdict": "approve", "reviewer": "ann"}},
			want{reviewer: "ann"}},
		{"a verdict under a key takes the key name", args{mcp.Caller{Person: "ci"}, "feedback", map[string]any{"verdict": "approve"}},
			want{reviewer: "ci"}},
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
