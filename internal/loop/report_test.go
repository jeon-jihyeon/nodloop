package loop_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/diagnose"
	"github.com/jeon-jihyeon/nodloop/internal/evidence"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/loop"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

func ptr(v float64) *float64 {
	return &v
}

func edited(t *testing.T, traceID string, status evidence.Status, at time.Time) feedback.Feedback {
	t.Helper()
	fb := verdict(traceID, feedback.VerdictEdit, at)
	var err error
	fb.Edited, err = json.Marshal(diagnose.Diagnosis{Status: status})
	require.NoError(t, err)
	return fb
}

// An edit that changes only the status of a review the helper builds
func statusOnly(t *testing.T, traceID string, at time.Time) feedback.Feedback {
	t.Helper()
	fb := verdict(traceID, feedback.VerdictEdit, at)
	var err error
	fb.Edited, err = json.Marshal(diagnose.Diagnosis{
		Status: evidence.StatusNoAction, Causes: []diagnose.Cause{{Summary: "cause", ParagraphIDs: []string{"p#1"}}}, Checks: diagnose.Checks{},
	})
	require.NoError(t, err)
	return fb
}

func audited(traceID string, v feedback.Verdict, at time.Time) feedback.Feedback {
	fb := verdict(traceID, v, at)
	fb.Audit = true
	return fb
}

func coded(fb feedback.Feedback, code feedback.ReasonCode) feedback.Feedback {
	fb.ReasonCode = code
	return fb
}

func TestHistoryReport(t *testing.T) {
	sunday := monday.Add(-10 * time.Hour)
	nextMonday := monday.AddDate(0, 0, 7)
	uses := []diagnose.AppliedKnowledge{applied("k", 1)}
	type args struct {
		reviews  []review
		extra    trace.Traces
		verdicts feedback.Records
		outcomes feedback.Outcomes
		since    time.Time
	}
	tcs := []struct {
		name string
		args args
		want loop.Report
	}{
		{
			"no verdict gives empty weeks",
			args{reviews: []review{{id: "r"}}},
			loop.Report{Weeks: []loop.Week{}, Agreement: loop.Agreement{Confusion: map[evidence.Status]map[evidence.Status]int{}}},
		},
		{
			"verdicts fall into weeks from Monday with audit verdicts apart and the median wait",
			args{
				reviews: []review{{id: "r1", at: sunday.Add(-time.Hour)}, {id: "r2", at: monday}, {id: "r3", at: monday}},
				verdicts: feedback.Records{
					verdict("r1", feedback.VerdictApprove, sunday),
					verdict("r2", feedback.VerdictApprove, monday.Add(time.Minute)),
					audited("r3", feedback.VerdictReject, monday.Add(3*time.Minute)),
				},
			},
			loop.Report{
				Weeks: []loop.Week{
					{
						Start:             monday.AddDate(0, 0, -7).Truncate(24 * time.Hour),
						Verdicts:          loop.Verdicts{Total: 1, Approve: 1, ApproveRate: ptr(1), EditRate: ptr(0), RejectRate: ptr(0)},
						MedianWaitSeconds: ptr(3600),
					},
					{
						Start:             monday.Truncate(24 * time.Hour),
						Verdicts:          loop.Verdicts{Total: 2, Approve: 1, Reject: 1, ApproveRate: ptr(0.5), EditRate: ptr(0), RejectRate: ptr(0.5)},
						Audit:             loop.Verdicts{Total: 1, Reject: 1, ApproveRate: ptr(0), EditRate: ptr(0), RejectRate: ptr(1)},
						MedianWaitSeconds: ptr(120),
					},
				},
				Agreement: loop.Agreement{
					Samples: 2, Matches: 2, Rate: ptr(1), Excluded: 1,
					Confusion: map[evidence.Status]map[evidence.Status]int{evidence.StatusReadyForReview: {evidence.StatusReadyForReview: 2}},
				},
				WithoutKnowledge: loop.Cohort{
					Verdicts: loop.Verdicts{
						Total: 3, Approve: 2, Reject: 1, ApproveRate: ptr(2.0 / 3), EditRate: ptr(0), RejectRate: ptr(1.0 / 3),
					},
					MedianWaitSeconds: ptr(180),
				},
			},
		},
		{
			"reason codes count per week audit and cohort and a correction without one counts only as a verdict",
			args{
				reviews: []review{{id: "r1", at: monday}, {id: "r2", at: monday}, {id: "r3", at: monday}},
				verdicts: feedback.Records{
					coded(verdict("r1", feedback.VerdictReject, monday), feedback.ReasonCause),
					coded(audited("r2", feedback.VerdictReject, monday), feedback.ReasonCause),
					verdict("r3", feedback.VerdictReject, monday),
				},
			},
			loop.Report{
				Weeks: []loop.Week{{
					Start: monday.Truncate(24 * time.Hour),
					Verdicts: loop.Verdicts{
						Total: 3, Reject: 3, ApproveRate: ptr(0), EditRate: ptr(0), RejectRate: ptr(1),
						ReasonCodes: map[feedback.ReasonCode]int{feedback.ReasonCause: 2},
					},
					Audit: loop.Verdicts{
						Total: 1, Reject: 1, ApproveRate: ptr(0), EditRate: ptr(0), RejectRate: ptr(1),
						ReasonCodes: map[feedback.ReasonCode]int{feedback.ReasonCause: 1},
					},
					MedianWaitSeconds: ptr(0),
				}},
				Agreement: loop.Agreement{Excluded: 3, Confusion: map[evidence.Status]map[evidence.Status]int{}},
				WithoutKnowledge: loop.Cohort{
					Verdicts: loop.Verdicts{
						Total: 3, Reject: 3, ApproveRate: ptr(0), EditRate: ptr(0), RejectRate: ptr(1),
						ReasonCodes: map[feedback.ReasonCode]int{feedback.ReasonCause: 2},
					},
					MedianWaitSeconds: ptr(0),
				},
			},
		},
		{
			"a verdict before its review is an invalid wait",
			args{reviews: []review{{id: "r", at: monday}}, verdicts: feedback.Records{verdict("r", feedback.VerdictApprove, sunday)}},
			loop.Report{
				Weeks: []loop.Week{{
					Start:    monday.AddDate(0, 0, -7).Truncate(24 * time.Hour),
					Verdicts: loop.Verdicts{Total: 1, Approve: 1, ApproveRate: ptr(1), EditRate: ptr(0), RejectRate: ptr(0)},
				}},
				Agreement: loop.Agreement{
					Samples: 1, Matches: 1, Rate: ptr(1),
					Confusion: map[evidence.Status]map[evidence.Status]int{evidence.StatusReadyForReview: {evidence.StatusReadyForReview: 1}},
				},
				InvalidWaits: 1,
				WithoutKnowledge: loop.Cohort{Verdicts: loop.Verdicts{
					Total: 1, Approve: 1, ApproveRate: ptr(1), EditRate: ptr(0), RejectRate: ptr(0),
				}},
			},
		},
		{
			"agreement reads the first submission and the corrected status and cohorts split on knowledge",
			args{
				reviews:  []review{{id: "r1", knowledge: uses}, {id: "r2"}},
				extra:    trace.Traces{revise(t, "ctx-r1", evidence.StatusHold)},
				verdicts: feedback.Records{edited(t, "r1", evidence.StatusNoAction, monday), edited(t, "r2", "maybe", monday)},
				outcomes: feedback.Outcomes{outcome("r1", feedback.ResultConfirmed, monday), outcome("r2", feedback.ResultRefuted, sunday)},
				since:    monday,
			},
			loop.Report{
				Since: monday,
				Weeks: []loop.Week{{
					Start:             monday.Truncate(24 * time.Hour),
					Verdicts:          loop.Verdicts{Total: 2, Edit: 2, ApproveRate: ptr(0), EditRate: ptr(1), RejectRate: ptr(0)},
					MedianWaitSeconds: ptr(0),
					MedianEditWidth:   ptr(3),
				}},
				Agreement: loop.Agreement{
					Samples: 1, Rate: ptr(0), Excluded: 1,
					Confusion: map[evidence.Status]map[evidence.Status]int{evidence.StatusHold: {evidence.StatusNoAction: 1}},
				},
				KnowledgeApplied: loop.Cohort{
					Verdicts:          loop.Verdicts{Total: 1, Edit: 1, ApproveRate: ptr(0), EditRate: ptr(1), RejectRate: ptr(0)},
					Outcomes:          loop.Outcomes{Total: 1, Confirmed: 1, ConfirmedRate: ptr(1), RefutedRate: ptr(0), InconclusiveRate: ptr(0)},
					MedianWaitSeconds: ptr(0), MedianEditWidth: ptr(3),
				},
				WithoutKnowledge: loop.Cohort{
					Verdicts:          loop.Verdicts{Total: 1, Edit: 1, ApproveRate: ptr(0), EditRate: ptr(1), RejectRate: ptr(0)},
					MedianWaitSeconds: ptr(0), MedianEditWidth: ptr(3),
				},
			},
		},
		{
			"edit widths and waits split into the week and the cohorts and an approve adds a wait but no width",
			args{
				reviews: []review{{id: "r1", at: monday, knowledge: uses}, {id: "r2", at: monday}, {id: "r3", at: monday}},
				verdicts: feedback.Records{
					statusOnly(t, "r1", monday.Add(time.Minute)),
					edited(t, "r2", evidence.StatusHold, monday.Add(2*time.Minute)),
					verdict("r3", feedback.VerdictApprove, monday.Add(30*time.Second)),
				},
			},
			loop.Report{
				Weeks: []loop.Week{{
					Start:             monday.Truncate(24 * time.Hour),
					Verdicts:          loop.Verdicts{Total: 3, Approve: 1, Edit: 2, ApproveRate: ptr(1.0 / 3), EditRate: ptr(2.0 / 3), RejectRate: ptr(0)},
					MedianWaitSeconds: ptr(60),
					MedianEditWidth:   ptr(2),
				}},
				Agreement: loop.Agreement{
					Samples: 3, Matches: 1, Rate: ptr(1.0 / 3),
					Confusion: map[evidence.Status]map[evidence.Status]int{evidence.StatusReadyForReview: {
						evidence.StatusReadyForReview: 1, evidence.StatusNoAction: 1, evidence.StatusHold: 1,
					}},
				},
				KnowledgeApplied: loop.Cohort{
					Verdicts:          loop.Verdicts{Total: 1, Edit: 1, ApproveRate: ptr(0), EditRate: ptr(1), RejectRate: ptr(0)},
					MedianWaitSeconds: ptr(60), MedianEditWidth: ptr(1),
				},
				WithoutKnowledge: loop.Cohort{
					Verdicts:          loop.Verdicts{Total: 2, Approve: 1, Edit: 1, ApproveRate: ptr(0.5), EditRate: ptr(0.5), RejectRate: ptr(0)},
					MedianWaitSeconds: ptr(75), MedianEditWidth: ptr(3),
				},
			},
		},
		{
			"records before since are left out",
			args{reviews: []review{{id: "r", at: monday}}, verdicts: feedback.Records{verdict("r", feedback.VerdictApprove, monday)}, since: nextMonday},
			loop.Report{
				Since: nextMonday, Weeks: []loop.Week{},
				Agreement: loop.Agreement{Confusion: map[evidence.Status]map[evidence.Status]int{}},
			},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			all := append(traces(t, tc.args.reviews...), tc.args.extra...)
			h, err := loop.New(all, tc.args.verdicts, tc.args.outcomes, nil)
			require.NoError(t, err)
			got := h.Report(tc.args.since)
			assert.Equal(t, tc.want, got)
		})
	}
}
