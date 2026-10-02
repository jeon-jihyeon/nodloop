package loop_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/loop"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

func TestHistoryHealth(t *testing.T) {
	gitC := knowledge.Ref{ID: "git-c", Version: 1}
	runs := trace.Traces{
		sessionRun(t, "r1", 1, "nodloop", gitC),
		sessionRun(t, "r2", 2, "nodloop", gitC),
		sessionRun(t, "r3", 3, "nodloop", gitC),
	}
	type want struct {
		applied, approved, rejected, confirmed, refuted int
		retire, promote                                 bool
	}
	tcs := []struct {
		name string
		args feedback.Outcomes
		want want
	}{
		{"a confirmed run makes a stated version a promotion candidate", feedback.Outcomes{
			outcome("r1", feedback.ResultConfirmed, monday.Add(5*time.Hour)),
		}, want{applied: 3, approved: 1, rejected: 1, confirmed: 1, promote: true}},
		{"a refuted run with no more confirmed makes it a retire candidate", feedback.Outcomes{
			outcome("r1", feedback.ResultConfirmed, monday.Add(5*time.Hour)),
			outcome("r2", feedback.ResultRefuted, monday.Add(5*time.Hour)),
		}, want{applied: 3, approved: 1, rejected: 1, confirmed: 1, refuted: 1, retire: true}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			verdicts := feedback.Records{
				verdict("r1", feedback.VerdictApprove, monday.Add(4*time.Hour)),
				verdict("r2", feedback.VerdictReject, monday.Add(4*time.Hour)),
			}
			h := loop.New(runs, verdicts, tc.args, knowledge.Set{runItem("git-c", 1)})

			got := h.Health(monday)

			require.Len(t, got, 1)
			assert.Equal(t, tc.want.applied, got[0].Applied)
			assert.Equal(t, tc.want.approved, got[0].Approved)
			assert.Equal(t, tc.want.rejected, got[0].Rejected)
			assert.Equal(t, tc.want.confirmed, got[0].Confirmed)
			assert.Equal(t, tc.want.refuted, got[0].Refuted)
			assert.Equal(t, tc.want.retire, got[0].RetireCandidate)
			assert.Equal(t, tc.want.promote, got[0].PromotionCandidate)
		})
	}
}

// A version a compaction made takes over the outcomes of runs that applied the versions it merged while it still reaches them
func TestHistoryCarriedOutcomes(t *testing.T) {
	old := knowledge.Ref{ID: "old", Version: 1}
	merged := runItem("new", 1)
	merged.Compaction, merged.Evidence.Knowledge = "c-1", []knowledge.Ref{old}
	elsewhere := runItem("narrow", 1)
	elsewhere.Compaction, elsewhere.Evidence.Knowledge = "c-2", []knowledge.Ref{old}
	elsewhere.Run = &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"other"}}}
	runs := trace.Traces{sessionRun(t, "r1", 1, "nodloop", old)}
	outcomes := feedback.Outcomes{outcome("r1", feedback.ResultRefuted, monday.Add(time.Hour))}

	h := loop.New(runs, nil, outcomes, knowledge.Set{merged, elsewhere, runItem("old", 1)})

	rows := map[string]loop.Health{}
	for _, row := range h.Health(monday) {
		rows[row.ID] = row
	}
	assert.Equal(t, 1, rows["new"].CarriedRefuted)
	assert.Zero(t, rows["narrow"].CarriedRefuted, "a merged version that no longer reaches the run takes nothing over")
	assert.Equal(t, []string{"r1"}, h.RefutedTraces("new", 1))
}

func TestHistoryRefutedValues(t *testing.T) {
	gitC := knowledge.Ref{ID: "git-c", Version: 1}
	docs := sessionRun(t, "r1", 1, "nodloop", gitC)
	docs.Labels["dir"] = []string{"docs"}
	runs := trace.Traces{docs, sessionRun(t, "r2", 2, "nodloop", gitC)}
	outcomes := feedback.Outcomes{
		outcome("r1", feedback.ResultRefuted, monday.Add(3*time.Hour)),
		outcome("r2", feedback.ResultConfirmed, monday.Add(3*time.Hour)),
	}

	h := loop.New(runs, nil, outcomes, knowledge.Set{runItem("git-c", 1)})

	assert.Equal(t, []string{"docs"}, h.RefutedValues("git-c", 1, "dir"))
	assert.Empty(t, h.RefutedValues("git-c", 1, "task"))
	assert.Equal(t, []string{"r1"}, h.RefutedTraces("git-c", 1))
	assert.Equal(t, []string{"r2"}, h.ConfirmedTraces("git-c", 1))
}

func TestHistoryBrokenReferences(t *testing.T) {
	runs := trace.Traces{sessionRun(t, "taught", 0, "nodloop")}
	verdicts := feedback.Records{verdict("taught", feedback.VerdictEdit, monday)}
	ok := runItem("ok", 1)
	moved := runItem("moved", 1)
	moved.Run.Labels = trace.Labels{"repo": {"renamed"}}
	moved.Run.Except = trace.Labels{"dir": {"gone"}}
	lost := runItem("lost", 1)
	lost.Evidence = knowledge.Evidence{FeedbackTraceIDs: []string{"missing"}, OutcomeTraceIDs: []string{"taught"}, Knowledge: []knowledge.Ref{{ID: "x", Version: 1}}}

	h := loop.New(runs, verdicts, nil, knowledge.Set{ok, moved, lost})

	assert.ElementsMatch(t, []loop.Issue{
		{ID: "moved", Version: 1, Field: "labels", Reference: "repo=renamed"},
		{ID: "moved", Version: 1, Field: "except", Reference: "dir=gone"},
		{ID: "lost", Version: 1, Field: "feedback_trace_ids", Reference: "missing"},
		{ID: "lost", Version: 1, Field: "outcome_trace_ids", Reference: "taught"},
		{ID: "lost", Version: 1, Field: "knowledge", Reference: "x v1"},
	}, h.BrokenReferences())
}
