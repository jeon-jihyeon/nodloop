package feedback_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
)

func TestResultValid(t *testing.T) {
	tcs := []struct {
		name string
		args feedback.Result
		want bool
	}{
		{"confirmed is valid", feedback.ResultConfirmed, true},
		{"refuted is valid", feedback.ResultRefuted, true},
		{"inconclusive is valid", feedback.ResultInconclusive, true},
		{"unknown result is invalid", "maybe", false},
		{"empty result is invalid", "", false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Valid())
		})
	}
}

func TestNewOutcome(t *testing.T) {
	now := time.Date(2026, 9, 23, 9, 0, 0, 0, time.FixedZone("KST", 9*60*60))
	utc := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	type args struct {
		traceID        string
		result         feedback.Result
		confirmedCause string
		reviewer       string
	}
	type want struct {
		outcome feedback.Outcome
		err     error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"confirmed without a reviewer falls back to author",
			args{traceID: "t1", result: feedback.ResultConfirmed, confirmedCause: "cause"},
			want{outcome: feedback.Outcome{
				TraceID: "t1", Time: utc, Result: feedback.ResultConfirmed, ConfirmedCause: "cause", Note: "note",
				Reviewer: "author",
			}},
		},
		{
			"refuted keeps the named reviewer",
			args{traceID: "t1", result: feedback.ResultRefuted, reviewer: "jed"},
			want{outcome: feedback.Outcome{
				TraceID: "t1", Time: utc, Result: feedback.ResultRefuted, Note: "note", Reviewer: "jed",
			}},
		},
		{
			"inconclusive is a known result",
			args{traceID: "t1", result: feedback.ResultInconclusive},
			want{outcome: feedback.Outcome{
				TraceID: "t1", Time: utc, Result: feedback.ResultInconclusive, Note: "note", Reviewer: "author",
			}},
		},
		{
			"refuted with a confirmed cause fails",
			args{traceID: "t1", result: feedback.ResultRefuted, confirmedCause: "cause"},
			want{err: feedback.ErrCauseUnexpected},
		},
		{
			"inconclusive with a confirmed cause fails",
			args{traceID: "t1", result: feedback.ResultInconclusive, confirmedCause: "cause"},
			want{err: feedback.ErrCauseUnexpected},
		},
		{"unknown result fails", args{traceID: "t1", result: "maybe"}, want{err: feedback.ErrResultUnknown}},
		{"missing trace id fails", args{result: feedback.ResultConfirmed}, want{err: feedback.ErrTraceIDRequired}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := feedback.NewOutcome(
				tc.args.traceID, tc.args.result, tc.args.confirmedCause, "note", tc.args.reviewer, now,
			)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.outcome, got)
		})
	}
}

func TestOutcomeFilterMatches(t *testing.T) {
	o := feedback.Outcome{TraceID: "t1", Result: feedback.ResultConfirmed}
	tcs := []struct {
		name string
		args feedback.OutcomeFilter
		want bool
	}{
		{"empty filter matches all", feedback.OutcomeFilter{}, true},
		{"same trace id matches", feedback.OutcomeFilter{TraceID: "t1"}, true},
		{"other trace id does not match", feedback.OutcomeFilter{TraceID: "t2"}, false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Matches(o))
		})
	}
}
