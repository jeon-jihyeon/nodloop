package feedback_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
)

func TestFeedbackRoundTrip(t *testing.T) {
	in := feedback.Feedback{
		TraceID:  "00019974a1b2c3d4deadbeef",
		Time:     time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC),
		Verdict:  feedback.VerdictEdit,
		Reason:   "reorder checks",
		Edited:   json.RawMessage(`{"order":["db","cache"]}`),
		Reviewer: "author",
	}
	b, err := json.Marshal(in)
	require.NoError(t, err)
	var out feedback.Feedback
	assert.NoError(t, json.Unmarshal(b, &out))
	assert.Equal(t, in, out)
}

func TestVerdictValid(t *testing.T) {
	tcs := []struct {
		name string
		args feedback.Verdict
		want bool
	}{
		{"approve is valid", feedback.VerdictApprove, true},
		{"edit is valid", feedback.VerdictEdit, true},
		{"reject is valid", feedback.VerdictReject, true},
		{"unknown verdict is invalid", "maybe", false},
		{"empty verdict is invalid", "", false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Valid())
		})
	}
}

func TestNew(t *testing.T) {
	now := time.Date(2026, 9, 23, 9, 0, 0, 0, time.FixedZone("KST", 9*60*60))
	utc := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	hold := json.RawMessage(`{"status":"hold"}`)
	type args struct {
		traceID  string
		verdict  feedback.Verdict
		edited   json.RawMessage
		reviewer string
	}
	type want struct {
		feedback feedback.Feedback
		err      error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"approve without a reviewer falls back to author",
			args{traceID: "t1", verdict: feedback.VerdictApprove},
			want{feedback: feedback.Feedback{
				TraceID: "t1", Time: utc, Verdict: feedback.VerdictApprove, Reason: "why", Reviewer: "author",
			}},
		},
		{
			"edit keeps the named reviewer and the edited review",
			args{traceID: "t1", verdict: feedback.VerdictEdit, edited: hold, reviewer: "jed"},
			want{feedback: feedback.Feedback{
				TraceID: "t1", Time: utc, Verdict: feedback.VerdictEdit, Reason: "why", Edited: hold, Reviewer: "jed",
			}},
		},
		{"missing trace id fails", args{verdict: feedback.VerdictApprove}, want{err: feedback.ErrTraceIDRequired}},
		{"unknown verdict fails", args{traceID: "t1", verdict: "maybe"}, want{err: feedback.ErrVerdictUnknown}},
		{
			"edited review that is not JSON fails",
			args{traceID: "t1", verdict: feedback.VerdictEdit, edited: json.RawMessage("{")},
			want{err: feedback.ErrEditedInvalid},
		},
		{
			"edit without an edited review fails",
			args{traceID: "t1", verdict: feedback.VerdictEdit},
			want{err: feedback.ErrEditedRequired},
		},
		{
			"approve with an edited review fails",
			args{traceID: "t1", verdict: feedback.VerdictApprove, edited: json.RawMessage(`{}`)},
			want{err: feedback.ErrEditedUnexpected},
		},
		{
			"reject with an edited review fails",
			args{traceID: "t1", verdict: feedback.VerdictReject, edited: json.RawMessage(`{}`)},
			want{err: feedback.ErrEditedUnexpected},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := feedback.New(tc.args.traceID, tc.args.verdict, "why", tc.args.edited, tc.args.reviewer, now)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.feedback, got)
		})
	}
}

func TestFeedbackCorrects(t *testing.T) {
	tcs := []struct {
		name string
		args feedback.Verdict
		want bool
	}{
		{"approve does not correct", feedback.VerdictApprove, false},
		{"edit corrects", feedback.VerdictEdit, true},
		{"reject corrects", feedback.VerdictReject, true},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, feedback.Feedback{Verdict: tc.args}.Corrects())
		})
	}
}

func TestFilterMatches(t *testing.T) {
	fb := feedback.Feedback{TraceID: "t1", Verdict: feedback.VerdictReject, Reviewer: "author"}
	tcs := []struct {
		name string
		args feedback.Filter
		want bool
	}{
		{"empty filter matches all", feedback.Filter{}, true},
		{"same trace id matches", feedback.Filter{TraceID: "t1"}, true},
		{"other trace id does not match", feedback.Filter{TraceID: "t2"}, false},
		{
			"verdict in the list matches",
			feedback.Filter{Verdicts: []feedback.Verdict{feedback.VerdictEdit, feedback.VerdictReject}},
			true,
		},
		{
			"verdict outside the list does not match",
			feedback.Filter{Verdicts: []feedback.Verdict{feedback.VerdictApprove}},
			false,
		},
		{"same reviewer matches", feedback.Filter{Reviewer: "author"}, true},
		{"other reviewer does not match", feedback.Filter{Reviewer: "session"}, false},
		{"limit is ignored by matching", feedback.Filter{Limit: 1}, true},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Matches(fb))
		})
	}
}

func TestRecordsLatest(t *testing.T) {
	base := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	approved := feedback.Feedback{TraceID: "t1", Time: base.Add(2 * time.Minute), Verdict: feedback.VerdictApprove}
	rejected := feedback.Feedback{TraceID: "t1", Time: base, Verdict: feedback.VerdictReject}
	other := feedback.Feedback{TraceID: "t2", Time: base, Verdict: feedback.VerdictReject}
	sameTime := feedback.Feedback{TraceID: "t2", Time: base, Verdict: feedback.VerdictApprove}
	tcs := []struct {
		name string
		args feedback.Records
		want feedback.Records
	}{
		{"no records give nothing", nil, nil},
		{
			"newest record wins when listed first",
			feedback.Records{approved, other, rejected},
			feedback.Records{approved, other},
		},
		{
			"newest record wins when listed last",
			feedback.Records{rejected, other, approved},
			feedback.Records{approved, other},
		},
		{"tie in time keeps the record listed first", feedback.Records{other, sameTime}, feedback.Records{other}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Latest())
		})
	}
}
