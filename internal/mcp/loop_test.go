package mcp_test

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/diagnose"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/loop"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// A conversation review of the event that applied the knowledge versions
func conversationReview(t *testing.T, id, event string, at time.Time, applied ...diagnose.AppliedKnowledge) trace.Trace {
	t.Helper()
	in, err := json.Marshal(map[string]any{"mode": diagnose.ModeInteractive, "change_context": "no_known_change", "knowledge": applied})
	require.NoError(t, err)
	return trace.Trace{
		ID: id, Name: trace.NameDiagnose, Subject: event, Ref: "ctx-" + id, Time: at, Input: in,
		Output: json.RawMessage(`{"status":"hold","causes":[],"checks":[]}`),
	}
}

func TestServerQueue(t *testing.T) {
	type want struct {
		items int
		audit bool
	}
	tcs := []struct {
		name string
		args map[string]any
		want want
	}{
		{"the defaults list the review", map[string]any{}, want{items: 1}},
		{"a full audit share marks the review", map[string]any{"limit": 1, "audit_rate": 1, "seed": 1}, want{items: 1, audit: true}},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := testkit.Open(t)
			review := conversationReview(t, "review", "tq-001", st.Clock.Now())
			require.NoError(t, st.Traces.Append(ctx, review))
			c := connect(t, st, "nodloop")
			var got struct {
				Items   []loop.QueueItem           `json:"items"`
				Reviews map[string]json.RawMessage `json:"reviews"`
			}
			require.NoError(t, c.Call(t, "queue", tc.args, &got))
			require.Len(t, got.Items, tc.want.items)
			assert.Equal(t, tc.want.audit, got.Items[0].Audit)
			assert.JSONEq(t, string(review.Output), string(got.Reviews["review"]))
			require.NoError(t, c.Run(t, "feedback", map[string]any{
				"trace_id": "review", "verdict": "approve", "audit": got.Items[0].Audit,
			}))
			records, err := st.Feedback.List(ctx, feedback.Filter{})
			require.NoError(t, err)
			require.Len(t, records, 1)
			assert.Equal(t, tc.want.audit, records[0].Audit)
			require.NoError(t, c.Call(t, "queue", tc.args, &got))
			assert.Empty(t, got.Items, "a judged review leaves the queue")
		})
	}
}

// One approved item past its deadline applied by a refuted review
// It cites a demo paragraph and one that does not exist
func TestServerKnowledgeHealthAndReaffirm(t *testing.T) {
	type want struct {
		reaffirmed      []string
		stale           bool
		retireCandidate bool
	}
	tcs := []struct {
		name string
		args []map[string]any
		want want
	}{
		{"health flags the stale retire candidate and its broken reference", nil, want{reaffirmed: []string{}, stale: true, retireCandidate: true}},
		{
			"a reaffirm resets the deadline and leaves the item approved",
			[]map[string]any{{"id": "item", "version": 1, "approver": "ann"}},
			want{reaffirmed: []string{"ann"}, retireCandidate: true},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := testkit.Open(t)
			old := st.Clock.Now().AddDate(0, 0, -knowledge.ReviewDays)
			require.NoError(t, st.Ledger.Import(ctx, []knowledge.Knowledge{{
				ID: "item", Version: 1, Kind: knowledge.KindMeaning, Content: "meaning", Basis: knowledge.BasisStated,
				Status: knowledge.StatusApproved, Author: "author", Approver: "ann", Time: old, ApprovedAt: old,
				Evidence: knowledge.Evidence{ParagraphIDs: []string{knownSegment, "gone#1"}},
			}}))
			review := conversationReview(t, "review", "tq-001", st.Clock.Now(), diagnose.AppliedKnowledge{ID: "item", Version: 1, Chars: 20})
			require.NoError(t, st.Traces.Append(ctx, review))
			require.NoError(t, st.Outcomes.Append(ctx, feedback.Outcome{
				TraceID: "review", Result: feedback.ResultRefuted, Time: st.Clock.Now(), Reviewer: "ann",
			}))
			c := connect(t, st, "nodloop")
			reaffirmed := []string{}
			for _, in := range tc.args {
				var answer struct {
					Approver string `json:"approver"`
				}
				require.NoError(t, c.Call(t, "reaffirm", in, &answer))
				reaffirmed = append(reaffirmed, answer.Approver)
			}
			var got struct {
				Items  []loop.Health `json:"items"`
				Issues []loop.Issue  `json:"issues"`
			}

			require.NoError(t, c.Call(t, "knowledge_health", map[string]any{}, &got))

			require.Len(t, got.Items, 1)
			approved, err := st.Ledger.Approved(ctx, "item", 1)
			require.NoError(t, err)
			assert.Equal(t, tc.want.reaffirmed, reaffirmed)
			assert.Equal(t, tc.want.stale, got.Items[0].Stale)
			assert.Equal(t, tc.want.retireCandidate, got.Items[0].RetireCandidate)
			assert.Equal(t, []loop.Issue{{ID: "item", Version: 1, Field: "paragraph_ids", Reference: "gone#1"}}, got.Issues)
			assert.Equal(t, knowledge.StatusApproved, approved.Status, "health and reaffirm never retire")
		})
	}
}

// An outcome of any result records the check and never proposes knowledge
func TestServerOutcomeAppendsNoProposal(t *testing.T) {
	tcs := []struct {
		name string
		args feedback.Result
		want []string
	}{
		{"a refuted check", feedback.ResultRefuted, []string{"trace_id", "result", "reviewer"}},
		{"a confirmed check", feedback.ResultConfirmed, []string{"trace_id", "result", "reviewer"}},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := testkit.Open(t)
			require.NoError(t, st.Traces.Append(ctx, conversationReview(t, "review", "tq-001", st.Clock.Now())))
			c := connect(t, st, "nodloop")
			var got map[string]any

			require.NoError(t, c.Call(t, "outcome", map[string]any{"trace_id": "review", "result": tc.args}, &got))

			all, err := st.Ledger.All(ctx)
			require.NoError(t, err)
			assert.ElementsMatch(t, tc.want, slices.Collect(maps.Keys(got)))
			assert.Empty(t, all)
		})
	}
}
