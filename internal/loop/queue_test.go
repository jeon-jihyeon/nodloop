package loop_test

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/loop"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// Each signal of a run raises it in the queue by its weight
func TestHistoryQueueReasons(t *testing.T) {
	gitC := knowledge.Ref{ID: "git-c", Version: 1}
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
	h := loop.New(runs, verdicts, nil, knowledge.Set{runItem("git-c", 1)})

	got, err := h.Queue(loop.QueueOptions{})

	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, loop.QueueItem{
		TraceID: "r2", Producer: "session", Labels: trace.Labels{"repo": {"nodloop"}}, Time: monday.Add(2 * time.Hour),
		Score: 4, Reasons: []loop.Reason{loop.ReasonPastCorrections, loop.ReasonStatedOnly}, PlaceReviewed: 2, PlaceCorrections: 1,
	}, got[0], "one of the two earlier verdicts in the repo corrected and the version reached an earlier run")
	assert.Equal(t, []loop.Reason{loop.ReasonNoApprovedItem, loop.ReasonNoKnowledge}, got[1].Reasons)
}

func TestHistoryQueue(t *testing.T) {
	gitC := knowledge.Ref{ID: "git-c", Version: 1}
	runs := trace.Traces{sessionRun(t, "top", 0, "nodloop", gitC)}
	for i, id := range []string{"a", "b", "c", "d", "e"} {
		runs = append(runs, sessionRun(t, id, i+1, "nodloop"))
	}
	type want struct {
		ids    []string
		audits []bool
		err    error
	}
	tcs := []struct {
		name string
		args loop.QueueOptions
		want want
	}{
		{
			"no limit lists every run without audit",
			loop.QueueOptions{},
			want{ids: []string{"top", "a", "b", "c", "d", "e"}, audits: []bool{false, false, false, false, false, false}},
		},
		{"a limit without audit keeps the top", loop.QueueOptions{Limit: 2}, want{ids: []string{"top", "a"}, audits: []bool{false, false}}},
		{
			"a short order fits the priority slots",
			loop.QueueOptions{Limit: 10, AuditRate: 0.2, Seed: 1},
			want{ids: []string{"top", "a", "b", "c", "d", "e"}, audits: []bool{false, false, false, false, false, false}},
		},
		{"a negative limit is refused", loop.QueueOptions{Limit: -1}, want{err: loop.ErrQueueOptions}},
		{"a rate above one is refused", loop.QueueOptions{Limit: 1, AuditRate: 1.5}, want{err: loop.ErrQueueOptions}},
		{"a rate that is not a number is refused", loop.QueueOptions{Limit: 1, AuditRate: math.NaN()}, want{err: loop.ErrQueueOptions}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := loop.New(runs, nil, nil, knowledge.Set{runItem("git-c", 1)})
			got, err := h.Queue(tc.args)
			assert.ErrorIs(t, err, tc.want.err)
			var ids []string
			var audits []bool
			for _, item := range got {
				ids, audits = append(ids, item.TraceID), append(audits, item.Audit)
			}
			assert.Equal(t, tc.want.ids, ids)
			assert.Equal(t, tc.want.audits, audits)
			again, err := h.Queue(tc.args)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, got, again, "the same seed draws the same samples")
		})
	}
}

// An audit share draws its slots at random from below the priority slots and the same seed draws the same runs
func TestHistoryQueueAudit(t *testing.T) {
	var runs trace.Traces
	for i, id := range []string{"a", "b", "c", "d", "e", "f"} {
		runs = append(runs, sessionRun(t, id, i, "nodloop"))
	}
	h := loop.New(runs, nil, nil, nil)

	got, err := h.Queue(loop.QueueOptions{Limit: 3, AuditRate: 0.2, Seed: 7})

	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, []bool{false, false, true}, []bool{got[0].Audit, got[1].Audit, got[2].Audit})
	assert.Equal(t, []string{"a", "b"}, []string{got[0].TraceID, got[1].TraceID})
}
