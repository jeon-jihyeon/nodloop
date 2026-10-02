package loop_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/loop"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// One run of producer session in the repo nodloop at monday plus offset hours
func sessionRun(t *testing.T, id string, offset int, repo string, applied ...knowledge.Ref) trace.Trace {
	t.Helper()
	in, err := json.Marshal(map[string][]knowledge.Ref{"applied": applied})
	require.NoError(t, err)
	return trace.Trace{
		ID: id, Name: trace.NameRun, Producer: "session", Labels: trace.Labels{"repo": {repo}},
		Time: monday.Add(time.Duration(offset) * time.Hour), Input: in, Output: json.RawMessage(`"answer"`),
	}
}

func runItem(id string, basis knowledge.Basis) knowledge.Knowledge {
	k := item(id, 1, knowledge.StatusApproved)
	k.Basis, k.Evidence = basis, knowledge.Evidence{FeedbackTraceIDs: []string{"taught"}}
	k.Run = &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"nodloop"}}}
	return k
}

// Runs join the views the way reviews do, read through what a run can tell
func TestHistoryRuns(t *testing.T) {
	gitC := knowledge.Ref{ID: "git-c", Version: 1}
	items := knowledge.Set{runItem("git-c", knowledge.BasisStated)}
	runs := trace.Traces{
		sessionRun(t, "taught", 0, "nodloop"),
		sessionRun(t, "r1", 1, "nodloop", gitC),
		sessionRun(t, "r2", 2, "nodloop", gitC),
		sessionRun(t, "r3", 3, "other"),
	}
	verdicts := feedback.Records{
		verdict("taught", feedback.VerdictEdit, monday.Add(30*time.Minute)),
		verdict("r1", feedback.VerdictApprove, monday.Add(90*time.Minute)),
	}
	outcomes := feedback.Outcomes{outcome("r1", feedback.ResultConfirmed, monday.Add(2*time.Hour))}
	h, err := loop.New(runs, verdicts, outcomes, items)
	require.NoError(t, err)

	t.Run("health counts the verdicts and outcomes of runs that applied the version", func(t *testing.T) {
		got := h.Health(monday)
		require.Len(t, got, 1)
		assert.Equal(t, 2, got[0].Applied)
		assert.Equal(t, 1, got[0].Approved)
		assert.Equal(t, 1, got[0].Confirmed)
		assert.True(t, got[0].PromotionCandidate)
		assert.Equal(t, []string{"r1"}, h.ConfirmedTraces("git-c", 1))
	})
	t.Run("the queue ranks unjudged runs by their own signals", func(t *testing.T) {
		got, err := h.Queue(loop.QueueOptions{})
		require.NoError(t, err)
		byID := map[string]loop.QueueItem{}
		for _, q := range got {
			byID[q.TraceID] = q
		}
		require.Len(t, byID, 2)
		assert.Equal(t, []loop.Reason{loop.ReasonPastCorrections, loop.ReasonStatedOnly}, byID["r2"].Reasons,
			"one of the two earlier verdicts in the repo corrected and the version reached an earlier run")
		assert.Equal(t, 2, byID["r2"].ContextReviewed)
		assert.Equal(t, []loop.Reason{loop.ReasonNoApprovedContext, loop.ReasonNoKnowledge}, byID["r3"].Reasons)
		assert.Zero(t, byID["r3"].Citations)
	})
	t.Run("the report counts run verdicts and leaves them out of the status agreement", func(t *testing.T) {
		got := h.Report(time.Time{})
		assert.Equal(t, 1, got.KnowledgeApplied.Verdicts.Approve)
		assert.Equal(t, 1, got.WithoutKnowledge.Verdicts.Edit)
		assert.Zero(t, got.Agreement.Samples)
		assert.Zero(t, got.Agreement.Excluded)
	})
	t.Run("a label no run carries any more is a broken reference", func(t *testing.T) {
		moved := runItem("moved", knowledge.BasisStated)
		moved.Run.Labels = trace.Labels{"repo": {"renamed"}}
		h, err := loop.New(runs, verdicts, outcomes, knowledge.Set{moved})
		require.NoError(t, err)
		assert.Equal(t, []loop.Issue{{ID: "moved", Version: 1, Field: "labels", Reference: "repo=renamed"}},
			h.BrokenReferences(nil, nil, nil, nil))
	})
}
