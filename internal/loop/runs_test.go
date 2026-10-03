package loop_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/loop"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

func TestRunsReport(t *testing.T) {
	at := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	run := func(id string, applied ...knowledge.Ref) trace.Trace {
		in, _ := json.Marshal(map[string][]knowledge.Ref{"applied": applied})
		return trace.Trace{ID: id, Name: trace.NameRun, Producer: "session", Input: in}
	}
	v1 := knowledge.Ref{ID: "git-c", Version: 1}
	verdict := func(id string, v feedback.Verdict, code feedback.ReasonCode, after time.Duration) feedback.Feedback {
		return feedback.Feedback{TraceID: id, Verdict: v, ReasonCode: code, Time: at.Add(after), Reviewer: feedback.ReviewerAuthor}
	}
	inferred := func(id string, v feedback.Verdict, code feedback.ReasonCode, after time.Duration) feedback.Feedback {
		fb := verdict(id, v, code, after)
		fb.Reviewer = feedback.ReviewerSession
		return fb
	}
	item := func() knowledge.Set {
		return knowledge.Set{{
			ID: "git-c", Version: 1, Kind: knowledge.KindJudgment, Content: "use git -C", Status: knowledge.StatusApproved,
			Run: &knowledge.RunScope{Producer: "session"}, Approver: "ann", ApprovedAt: at.Add(2 * time.Hour),
			Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{"taught"}},
		}, {
			ID: "data", Version: 1, Kind: knowledge.KindMeaning, Content: "lag", Status: knowledge.StatusApproved, Approver: "ann",
		}}
	}
	traces := trace.Traces{
		run("taught"), run("a1", v1), run("a2", v1), run("a3", v1), run("a4", v1), run("a5"),
		{ID: "failed", Name: trace.NameRun, Error: "cancelled", Input: json.RawMessage(`{"applied":[{"id":"git-c","version":1}]}`)},
	}
	type args struct {
		verdicts feedback.Records
		items    knowledge.Set
	}
	tcs := []struct {
		name string
		args args
		want []loop.RunItem
	}{
		{
			"followed, repeat on the taught code and settle from the correction",
			args{feedback.Records{
				verdict("taught", feedback.VerdictEdit, feedback.ReasonOther, 0),
				verdict("a1", feedback.VerdictApprove, "", 3*time.Hour),
				verdict("a2", feedback.VerdictReject, feedback.ReasonOther, 3*time.Hour),
				verdict("a3", feedback.VerdictEdit, feedback.ReasonApproach, 3*time.Hour),
				verdict("a5", feedback.VerdictReject, feedback.ReasonOther, 3*time.Hour),
			}, item()},
			[]loop.RunItem{{ID: "git-c", Version: 1, Applied: 4, Judged: 3, Followed: 1, Repeat: 1, Settle: 2 * time.Hour}},
		},
		{
			"a later approve replaces a correction and a session verdict counts apart",
			args{feedback.Records{
				verdict("a1", feedback.VerdictReject, feedback.ReasonOther, 3*time.Hour),
				verdict("a1", feedback.VerdictApprove, "", 4*time.Hour),
				inferred("a2", feedback.VerdictApprove, "", 3*time.Hour),
			}, item()},
			[]loop.RunItem{{ID: "git-c", Version: 1, Applied: 4, Judged: 1, Followed: 1, InferredJudged: 1, InferredFollowed: 1}},
		},
		{
			"a person's verdict wins over a newer inferred one and an inferred correction teaches the code and the settle",
			args{feedback.Records{
				inferred("taught", feedback.VerdictReject, feedback.ReasonForm, time.Hour),
				verdict("a1", feedback.VerdictApprove, "", 3*time.Hour),
				inferred("a1", feedback.VerdictReject, feedback.ReasonForm, 4*time.Hour),
				inferred("a2", feedback.VerdictReject, feedback.ReasonForm, 3*time.Hour),
				inferred("a3", feedback.VerdictReject, feedback.ReasonFact, 3*time.Hour),
			}, item()},
			[]loop.RunItem{{ID: "git-c", Version: 1, Applied: 4, Judged: 1, Followed: 1, InferredJudged: 2, InferredRepeat: 1, Settle: time.Hour}},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, loop.NewRuns(traces, tc.args.verdicts).Report(tc.args.items))
		})
	}
}
