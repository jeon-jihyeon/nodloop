package loop_test

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
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
	h := loop.New(runs, verdicts, nil, knowledge.Set{runItem("git-c", 1)}, time.Time{})

	got, err := h.Queue(loop.QueueOptions{})

	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, loop.QueueItem{
		TraceID: "r2", Producer: "session", Labels: trace.Labels{"repo": {"nodloop"}}, Time: monday.Add(2 * time.Hour),
		Score: 4, Reasons: []loop.Reason{loop.ReasonPastCorrections, loop.ReasonStatedOnly}, PlaceReviewed: 2, PlaceCorrections: 1,
	}, got[0], "one of the two earlier verdicts in the repo corrected and the version reached an earlier run")
	assert.Equal(t, []loop.Reason{loop.ReasonNoApprovedItem, loop.ReasonNoKnowledge}, got[1].Reasons)
}

// Only the first run ever to receive a version is new knowledge whatever window the history reads
func TestHistoryQueueNewKnowledgeOverEveryRun(t *testing.T) {
	gitC := knowledge.Ref{ID: "git-c", Version: 1}
	runs := trace.Traces{sessionRun(t, "later", 2, "nodloop", gitC), sessionRun(t, "first", 0, "nodloop", gitC)}
	tcs := []struct {
		name string
		args time.Time
		want map[string]bool
	}{
		{"every run read marks the first", time.Time{}, map[string]bool{"first": true, "later": false}},
		{"a window after the first run marks none", monday.Add(time.Hour), map[string]bool{"later": false}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := loop.New(runs, nil, nil, knowledge.Set{runItem("git-c", 1)}, tc.args)

			items, err := h.Queue(loop.QueueOptions{})

			require.NoError(t, err)
			got := map[string]bool{}
			for _, item := range items {
				got[item.TraceID] = slices.Contains(item.Reasons, loop.ReasonNewKnowledge)
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

// A drawn run of the applied arm or the withheld arm in the repo nodloop with the refs
func drawnRun(t *testing.T, id string, offset int, withheld bool, refs ...knowledge.Ref) trace.Trace {
	t.Helper()
	in := knowledge.RunInput{Applied: refs, Drawn: true, Share: 0.5, Scheme: knowledge.DrawScheme}
	if withheld {
		in.Applied, in.Withheld = []knowledge.Ref{}, refs
	}
	raw, err := json.Marshal(in)
	require.NoError(t, err)
	tr := sessionRun(t, id, offset, "nodloop")
	tr.Input = raw
	return tr
}

// A withheld run ranks on the items it would have received so its score and reasons never tell its arm
func TestHistoryQueueIsBlindToTheArm(t *testing.T) {
	gitC := knowledge.Ref{ID: "git-c", Version: 1}
	runs := trace.Traces{drawnRun(t, "first", 0, false, gitC), drawnRun(t, "applied", 1, false, gitC), drawnRun(t, "withheld", 1, true, gitC)}
	verdicts := feedback.Records{verdict("first", feedback.VerdictApprove, monday.Add(30*time.Minute))}
	h := loop.New(runs, verdicts, nil, knowledge.Set{runItem("git-c", 1)}, time.Time{})

	got, err := h.Queue(loop.QueueOptions{})

	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, got[0].Score, got[1].Score)
	assert.Equal(t, got[0].Reasons, got[1].Reasons)
	assert.NotContains(t, got[1].Reasons, loop.ReasonNoKnowledge)
}

// The audit takes one run of each arm in turn so a small withheld arm still gets audit samples
func TestHistoryQueueAuditDrawsEachArm(t *testing.T) {
	gitC := knowledge.Ref{ID: "git-c", Version: 1}
	var runs trace.Traces
	for i := range 20 {
		runs = append(runs, drawnRun(t, fmt.Sprintf("a%02d", i), i, false, gitC))
	}
	runs = append(runs, drawnRun(t, "w1", 30, true, gitC), sessionRun(t, "bare", 31, "nodloop"))
	h := loop.New(runs, nil, nil, knowledge.Set{runItem("git-c", 1)}, time.Time{})
	type want struct {
		withheld, bare bool
	}
	tcs := []struct {
		name string
		args int64
		want want
	}{
		{"seed 1", 1, want{true, true}},
		{"seed 2", 2, want{true, true}},
		{"seed 3", 3, want{true, true}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := h.Queue(loop.QueueOptions{Limit: 5, AuditRate: 0.6, Seed: tc.args})
			require.NoError(t, err)
			audited := map[string]bool{}
			for _, item := range got {
				if item.Audit {
					audited[item.TraceID] = true
				}
			}
			assert.Len(t, audited, 3)
			assert.Equal(t, tc.want, want{audited["w1"], audited["bare"]})
		})
	}
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
			h := loop.New(runs, nil, nil, knowledge.Set{runItem("git-c", 1)}, time.Time{})
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
	h := loop.New(runs, nil, nil, nil, time.Time{})

	got, err := h.Queue(loop.QueueOptions{Limit: 3, AuditRate: 0.2, Seed: 7})

	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, []bool{false, false, true}, []bool{got[0].Audit, got[1].Audit, got[2].Audit})
	assert.Equal(t, []string{"a", "b"}, []string{got[0].TraceID, got[1].TraceID})
}
