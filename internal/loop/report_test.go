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

func TestHistoryReport(t *testing.T) {
	gitC := knowledge.Ref{ID: "git-c", Version: 1}
	runs := trace.Traces{
		sessionRun(t, "r1", 0, "nodloop", gitC),
		sessionRun(t, "r2", 1, "nodloop"),
		sessionRun(t, "r3", 2, "nodloop"),
	}
	edit := verdict("r2", feedback.VerdictEdit, monday.Add(3*time.Hour))
	edit.Edited, edit.ReasonCode = []byte(`{"answer":"fixed","note":"x"}`), feedback.ReasonOther
	audit := verdict("r3", feedback.VerdictApprove, monday.Add(-time.Hour))
	audit.Audit = true
	verdicts := feedback.Records{
		verdict("r1", feedback.VerdictApprove, monday.Add(2*time.Hour)),
		edit,
		audit,
	}
	outcomes := feedback.Outcomes{outcome("r1", feedback.ResultConfirmed, monday.Add(4*time.Hour))}
	h := loop.New(runs, verdicts, outcomes, nil)
	tcs := []struct {
		name string
		args time.Time
		want int
	}{
		{"every verdict from the start", time.Time{}, 3},
		{"only the verdicts from since on", monday.Add(150 * time.Minute), 1},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			got := h.Report(tc.args)
			total := 0
			for _, w := range got.Weeks {
				total += w.Verdicts.Total
			}
			assert.Equal(t, tc.want, total)
		})
	}

	got := h.Report(time.Time{})
	require.Len(t, got.Weeks, 1, "every verdict falls in the week from monday")
	assert.Equal(t, 1, got.Weeks[0].Audit.Approve)
	assert.Equal(t, 1, got.InvalidWaits, "a verdict before its run is left out of the waits")
	assert.Equal(t, 1, got.KnowledgeApplied.Verdicts.Approve)
	assert.Equal(t, 1, got.KnowledgeApplied.Outcomes.Confirmed)
	assert.Equal(t, 1, got.WithoutKnowledge.Verdicts.Edit)
	assert.Equal(t, map[feedback.ReasonCode]int{feedback.ReasonOther: 1}, got.WithoutKnowledge.Verdicts.ReasonCodes)
	require.NotNil(t, got.WithoutKnowledge.MedianEditWidth)
	assert.InDelta(t, 2, *got.WithoutKnowledge.MedianEditWidth, 0.001, "the edit changed answer and added note")
	require.NotNil(t, got.KnowledgeApplied.MedianWaitSeconds)
	assert.InDelta(t, 7200, *got.KnowledgeApplied.MedianWaitSeconds, 0.001)
}
