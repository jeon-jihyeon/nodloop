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

func TestHistoryOutcomeActors(t *testing.T) {
	type args struct {
		actor feedback.Actor
		newer bool
	}
	type want struct {
		confirmed, refuted, inconclusive int
		retire, promote                  bool
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"legacy observation is retained", args{"", false}, want{0, 1, 0, true, false}},
		{"code observation is retained", args{feedback.ActorCode, false}, want{0, 1, 0, true, false}},
		{"model observation is retained", args{feedback.ActorModel, false}, want{0, 1, 0, true, false}},
		{"person observation is retained", args{feedback.ActorPerson, false}, want{0, 1, 0, true, false}},
		{"machine inconclusive clears a previous refutation", args{feedback.ActorCode, true}, want{0, 0, 1, false, false}},
		{"legacy inconclusive clears a previous refutation", args{"", true}, want{0, 0, 1, false, false}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ref := knowledge.Ref{ID: "git-c", Version: 1}
			run := sessionRun(t, "r1", 1, "nodloop", ref)
			input := knowledge.RunInput{Applied: []knowledge.Ref{ref}, Drawn: true, Unit: "unit", Share: 0.5}
			var err error
			run.Input, err = json.Marshal(input)
			require.NoError(t, err)
			checks := feedback.Outcomes{{
				TraceID: "r1", Result: feedback.ResultRefuted, Time: monday, Reviewer: "ann", Actor: tc.args.actor,
			}}
			if tc.args.newer {
				checks = append(checks, feedback.Outcome{
					TraceID: "r1", Result: feedback.ResultInconclusive, Time: monday.Add(time.Hour),
					Reviewer: "ann", Actor: tc.args.actor,
				})
			}
			runs := trace.Traces{run}
			h := loop.New(runs, nil, checks, knowledge.Set{runItem("git-c", 1)}, time.Time{})
			rows := h.Health(monday)
			require.Len(t, rows, 1)
			row := rows[0]
			assert.Equal(t, tc.want, want{
				row.Confirmed, row.Refuted, row.Inconclusive, row.RetireCandidate, row.PromotionCandidate,
			})
			effect := loop.NewRuns(runs, nil).Effect(nil, checks, nil, time.Time{})
			require.Len(t, effect, 2)
			assert.Equal(t, tc.want.refuted, effect[0].Refuted)
			assert.Len(t, h.RefutedTraces(ref.ID, ref.Version), tc.want.refuted)
		})
	}
}

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
			h := loop.New(runs, verdicts, tc.args, knowledge.Set{runItem("git-c", 1)}, time.Time{})

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

	h := loop.New(runs, nil, outcomes, knowledge.Set{merged, elsewhere, runItem("old", 1)}, time.Time{})

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

	h := loop.New(runs, nil, outcomes, knowledge.Set{runItem("git-c", 1)}, time.Time{})

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

	h := loop.New(runs, verdicts, nil, knowledge.Set{ok, moved, lost}, time.Time{})

	assert.ElementsMatch(t, []loop.Issue{
		{ID: "moved", Version: 1, Field: "labels", Reference: "repo=renamed"},
		{ID: "moved", Version: 1, Field: "except", Reference: "dir=gone"},
		{ID: "lost", Version: 1, Field: "feedback_trace_ids", Reference: "missing"},
		{ID: "lost", Version: 1, Field: "outcome_trace_ids", Reference: "taught"},
		{ID: "lost", Version: 1, Field: "knowledge", Reference: "x v1"},
	}, h.BrokenReferences())
}

// An approved item no run applied for IdleDays is idle and one corrected again for its reason is contested
// A contested item lists the approved items one run carries with it
func TestHistoryLifetime(t *testing.T) {
	gitC := knowledge.Ref{ID: "git-c", Version: 1}
	later := monday.Add(loop.IdleDays * 24 * time.Hour)
	type args struct {
		// Runs that applied git-c and the inferred verdicts on them
		runs     trace.Traces
		verdicts feedback.Records
		now      time.Time
		// The cutoff of the retention window
		since time.Time
	}
	type want struct {
		idle, contested bool
		compactWith     []string
	}
	inferred := func(id string, v feedback.Verdict, code feedback.ReasonCode) feedback.Feedback {
		return feedback.Feedback{TraceID: id, Verdict: v, ReasonCode: code, Time: monday.Add(5 * time.Hour), Reviewer: feedback.ReviewerSession}
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"an item applied after approval is neither", args{trace.Traces{sessionRun(t, "r1", 1, "nodloop", gitC)}, nil, later, time.Time{}}, want{}},
		{"an item no run applied since approval is idle once IdleDays passed", args{nil, nil, later, time.Time{}}, want{idle: true}},
		{"an item no run applied is not idle before IdleDays", args{nil, nil, monday.Add(time.Hour), time.Time{}}, want{}},
		{
			"an item applied after approval before the retention window is not idle",
			args{trace.Traces{sessionRun(t, "r1", 1, "nodloop", gitC)}, nil, later, monday.Add(2 * time.Hour)}, want{},
		},
		{"an inferred correction of a run that applied it makes it contested",
			args{
				trace.Traces{sessionRun(t, "r1", 1, "nodloop", gitC)}, feedback.Records{inferred("r1", feedback.VerdictReject, feedback.ReasonForm)},
				later, time.Time{},
			},
			want{contested: true, compactWith: []string{"pr-body"}}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := loop.New(tc.args.runs, tc.args.verdicts, nil, knowledge.Set{runItem("git-c", 1), runItem("pr-body", 1)}, tc.args.since)

			got := h.Health(tc.args.now)

			require.Len(t, got, 2)
			assert.Equal(t, "git-c", got[0].ID)
			assert.Equal(t, tc.want.idle, got[0].Idle)
			assert.Equal(t, tc.want.contested, got[0].Contested)
			assert.Equal(t, tc.want.compactWith, got[0].CompactWith)
		})
	}
}
