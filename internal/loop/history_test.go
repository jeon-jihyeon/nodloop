package loop_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/loop"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// A Monday so week bounds are easy to read
var monday = time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)

// One run of producer session in the repo at monday plus offset hours
func sessionRun(t *testing.T, id string, offset int, repo string, applied ...knowledge.Ref) trace.Trace {
	t.Helper()
	in, err := json.Marshal(map[string][]knowledge.Ref{"applied": applied})
	require.NoError(t, err)
	return trace.Trace{
		ID: id, Name: trace.NameRun, Producer: "session", Labels: trace.Labels{"repo": {repo}},
		Time: monday.Add(time.Duration(offset) * time.Hour), Input: in, Output: json.RawMessage(`{"answer":"` + id + `"}`),
	}
}

func verdict(traceID string, v feedback.Verdict, at time.Time) feedback.Feedback {
	return feedback.Feedback{TraceID: traceID, Verdict: v, Time: at, Reviewer: feedback.ReviewerAuthor}
}

func outcome(traceID string, r feedback.Result, at time.Time) feedback.Outcome {
	return feedback.Outcome{TraceID: traceID, Result: r, Time: at, Reviewer: feedback.ReviewerAuthor}
}

// An approved stated item for the runs of the repo nodloop that cites the run taught
func runItem(id string, version int) knowledge.Knowledge {
	return knowledge.Knowledge{
		ID: id, Version: version, Kind: knowledge.KindJudgment, Content: id, Status: knowledge.StatusApproved, Basis: knowledge.BasisStated,
		Run:      &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"nodloop"}}},
		Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{"taught"}}, Author: "author", Approver: "ann",
		ApprovedAt: monday, Time: monday,
	}
}

// Failed runs and traces of other names stay out and the human records are the latest
func TestNew(t *testing.T) {
	gitC := knowledge.Ref{ID: "git-c", Version: 1}
	failed := sessionRun(t, "failed", 1, "nodloop", gitC)
	failed.Error = "cancelled"
	traces := trace.Traces{
		sessionRun(t, "r1", 1, "nodloop", gitC), failed,
		{ID: "check", Name: trace.NameCheck, SessionID: "c-1", Time: monday},
	}
	verdicts := feedback.Records{
		verdict("r1", feedback.VerdictReject, monday.Add(2*time.Hour)),
		verdict("r1", feedback.VerdictApprove, monday.Add(3*time.Hour)),
		{TraceID: "r1", Verdict: feedback.VerdictEdit, Time: monday.Add(4 * time.Hour), Reviewer: feedback.ReviewerSession},
	}

	h := loop.New(traces, verdicts, nil, knowledge.Set{runItem("git-c", 1)})

	got := h.Health(monday)
	require.Len(t, got, 1)
	assert.Equal(t, 1, got[0].Applied, "the failed run applied nothing a person could judge")
	assert.Equal(t, 1, got[0].Approved, "a later approve replaces the reject and a session verdict is no person's word")
	assert.Equal(t, map[string]json.RawMessage{"r1": json.RawMessage(`{"answer":"r1"}`)}, h.Outputs([]string{"r1", "failed", "check"}))
}

// Load reads the run traces of the stores
func TestLoad(t *testing.T) {
	ctx := context.Background()
	s := testkit.Open(t)
	require.NoError(t, s.Traces.Append(ctx, sessionRun(t, "r1", 1, "nodloop")))
	require.NoError(t, s.Feedback.Append(ctx, verdict("r1", feedback.VerdictApprove, monday.Add(time.Hour))))

	h, err := loop.Load(ctx, s.Traces, s.Feedback, s.Outcomes, s.Ledger)

	require.NoError(t, err)
	assert.Equal(t, 1, h.Report(time.Time{}).WithoutKnowledge.Verdicts.Approve)
}
