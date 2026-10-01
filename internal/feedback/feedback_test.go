package feedback_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
)

func TestFeedbackRoundTrip(t *testing.T) {
	in := feedback.Feedback{
		TraceID:    "00019974a1b2c3d4deadbeef",
		Time:       time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC),
		Verdict:    feedback.VerdictEdit,
		ReasonCode: feedback.ReasonChecks,
		Reason:     "reorder checks",
		Edited:     json.RawMessage(`{"order":["db","cache"]}`),
		Reviewer:   "author",
		Audit:      true,
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
		code     feedback.ReasonCode
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
		{
			"an edit keeps its reason code",
			args{traceID: "t1", verdict: feedback.VerdictEdit, code: feedback.ReasonChecks, edited: hold},
			want{feedback: feedback.Feedback{
				TraceID: "t1", Time: utc, Verdict: feedback.VerdictEdit, ReasonCode: feedback.ReasonChecks, Reason: "why",
				Edited: hold, Reviewer: "author",
			}},
		},
		{
			"a reject keeps its reason code",
			args{traceID: "t1", verdict: feedback.VerdictReject, code: feedback.ReasonCause},
			want{feedback: feedback.Feedback{
				TraceID: "t1", Time: utc, Verdict: feedback.VerdictReject, ReasonCode: feedback.ReasonCause, Reason: "why",
				Reviewer: "author",
			}},
		},
		{
			"an unknown reason code fails",
			args{traceID: "t1", verdict: feedback.VerdictReject, code: "typo"},
			want{err: feedback.ErrReasonCodeUnknown},
		},
		{
			"an approval with a reason code fails",
			args{traceID: "t1", verdict: feedback.VerdictApprove, code: feedback.ReasonStatus},
			want{err: feedback.ErrReasonCodeUnexpected},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := feedback.New(tc.args.traceID, tc.args.verdict, tc.args.code, "why", tc.args.edited, tc.args.reviewer, now)
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

func TestFeedbackImplicit(t *testing.T) {
	tcs := []struct {
		name string
		args string
		want bool
	}{
		{"session reviewer is implicit", feedback.ReviewerSession, true},
		{"author reviewer is a person", feedback.ReviewerAuthor, false},
		{"named reviewer is a person", "jed", false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, feedback.Feedback{Reviewer: tc.args}.Implicit())
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

func TestRecordsHuman(t *testing.T) {
	base := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	edited := feedback.Feedback{TraceID: "t1", Time: base, Verdict: feedback.VerdictEdit, Reviewer: feedback.ReviewerAuthor}
	session := feedback.Feedback{
		TraceID: "t1", Time: base.Add(time.Minute), Verdict: feedback.VerdictApprove, Reviewer: feedback.ReviewerSession,
	}
	named := feedback.Feedback{TraceID: "t2", Time: base, Verdict: feedback.VerdictReject, Reviewer: "jed"}
	tcs := []struct {
		name string
		args feedback.Records
		want feedback.Records
	}{
		{"no records give nothing", nil, nil},
		{"a session record is dropped", feedback.Records{session, edited, named}, feedback.Records{edited, named}},
		{
			"a later session record never replaces a record of a person",
			feedback.Records{session, edited}, feedback.Records{edited},
		},
		{"only session records give nothing", feedback.Records{session}, nil},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Human().Latest())
		})
	}
}

func TestFeedbackEditWidth(t *testing.T) {
	original := json.RawMessage(`{"status":"hold","causes":[],"checks":[{"purpose":"a"}]}`)
	type args struct {
		verdict feedback.Verdict
		edited  string
	}
	tcs := []struct {
		name string
		args args
		want int
	}{
		{"an approve changes nothing", args{feedback.VerdictApprove, ""}, 0},
		{"a reject changes nothing", args{feedback.VerdictReject, ""}, 0},
		{"an edit of one field counts one", args{feedback.VerdictEdit, `{"status":"no_action","causes":[],"checks":[{"purpose":"a"}]}`}, 1},
		{
			"a removed field and an added field count one each",
			args{feedback.VerdictEdit, `{"status":"hold","causes":[],"open_questions":["q"]}`},
			2,
		},
		{"an edit equal to the review counts nothing", args{feedback.VerdictEdit, string(original)}, 0},
		{"an edit that is not an object counts nothing", args{feedback.VerdictEdit, `["status"]`}, 0},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fb := feedback.Feedback{Verdict: tc.args.verdict, Edited: json.RawMessage(tc.args.edited)}
			assert.Equal(t, tc.want, fb.EditWidth(original))
		})
	}
}

// Secrets are built from parts so no scanner reads the fixtures as leaked keys
func TestNewRedactsSessionRecords(t *testing.T) {
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	pem := "-----BEGIN RSA PRIVATE KEY-----\nMIIEow\n-----END RSA PRIVATE KEY-----"
	tcs := []struct {
		name string
		args string
		want string
	}{
		{"a text without a secret is kept", "the cause was a campaign launch", "the cause was a campaign launch"},
		{"a private key block", "key " + pem + " end", "key [redacted] end"},
		{"an aws access key id", "id AKIA" + strings.Repeat("Q", 16) + " here", "id [redacted] here"},
		{"a github token", "ghp_" + strings.Repeat("a", 36), "[redacted]"},
		{"a fine grained github token", "github_pat_" + strings.Repeat("b", 30), "[redacted]"},
		{"a slack token", "xoxb-" + strings.Repeat("1", 12), "[redacted]"},
		{"a google api key", "AIza" + strings.Repeat("c", 35), "[redacted]"},
		{"a key of the sk shape", "sk-" + strings.Repeat("d", 24), "[redacted]"},
		{"a json web token", "eyJ" + strings.Repeat("e", 10) + "." + strings.Repeat("f", 10) + "." + strings.Repeat("g", 10), "[redacted]"},
		{"the token after bearer", "Authorization: Bearer " + strings.Repeat("h", 20), "Authorization: Bearer [redacted]"},
		{"the password of a url", "clone https://jed:" + "hunter2@example.com/repo", "clone https://jed:[redacted]@example.com/repo"},
		{"a named value", "set password=" + "hunter2, then retry", "set password=[redacted], then retry"},
		{"a named value after a colon", "API_KEY: " + "abc123", "API_KEY: [redacted]"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			session, err := feedback.New("t1", feedback.VerdictReject, "", tc.args, nil, feedback.ReviewerSession, now)
			require.NoError(t, err)
			person, err := feedback.New("t1", feedback.VerdictReject, "", tc.args, nil, "jed", now)
			require.NoError(t, err)
			outcome, err := feedback.NewOutcome("t1", feedback.ResultConfirmed, tc.args, tc.args, feedback.ReviewerSession, now)
			require.NoError(t, err)

			assert.Equal(t, tc.want, session.Reason)
			assert.Equal(t, tc.args, person.Reason)
			assert.Equal(t, []string{tc.want, tc.want}, []string{outcome.ConfirmedCause, outcome.Note})
		})
	}
}

func TestNewRedactsSessionEditsAsJSON(t *testing.T) {
	t.Parallel()
	edited := json.RawMessage(`{"status":"hold","hold_reasons":["token=` + "s3cr3t" + `\n` + "ghp_" + strings.Repeat("z", 36) + `"]}`)

	got, err := feedback.New("t1", feedback.VerdictEdit, "", "", edited, feedback.ReviewerSession, time.Time{})

	require.NoError(t, err)
	assert.JSONEq(t, `{"status":"hold","hold_reasons":["token=[redacted]\n[redacted]"]}`, string(got.Edited))
}
