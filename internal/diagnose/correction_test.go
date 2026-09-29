package diagnose_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/jeon-jihyeon/nodloop/internal/diagnose"
	"github.com/jeon-jihyeon/nodloop/internal/evidence"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/llm"
	"github.com/jeon-jihyeon/nodloop/internal/llm/llmmock"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

const (
	correctionConfirm = "metric-anomaly-investigation#Metric anomaly investigation/Confirm the signal#1"
	correctionSegment = "metric-anomaly-investigation#Metric anomaly investigation/Check the segment#1"
	correctionOutcome = "metric-anomaly-investigation#Metric anomaly investigation/Check downstream outcomes#1"
)

// A recorded review of tq-005 and the diagnoser that recorded it
type corrected struct {
	testkit.Stores
	diagnoser *diagnose.Diagnoser
	review    diagnose.Diagnosis
	traceID   string
	// The context trace of the review
	contextID string
	moved     []string
}

func recordReview(t *testing.T, client llm.Client) corrected {
	t.Helper()
	ctx := context.Background()
	s := testkit.Open(t)
	d := diagnose.New(s.Source, testkit.Policy(t), client, s.Traces, s.Feedback, s.Ledger, s.Clock.Now)
	c, err := d.Prepare(ctx, "tq-005", diagnose.ModeInteractive, diagnose.Session{})
	require.NoError(t, err)
	review := diagnose.Diagnosis{
		Status: evidence.StatusReadyForReview,
		Causes: []diagnose.Cause{{Summary: "low quality traffic", ParagraphIDs: []string{correctionSegment}}},
		Checks: diagnose.Checks{
			{Step: "confirm the signal", Purpose: "signal", ParagraphIDs: []string{correctionConfirm}},
			{Step: "check the segment", Purpose: "segment", ParagraphIDs: []string{correctionSegment}},
		},
	}
	res, err := d.Record(ctx, c.PendingID, review)
	require.NoError(t, err)
	return corrected{
		Stores: s, diagnoser: d, review: res.Diagnosis, traceID: res.TraceID, contextID: c.PendingID,
		moved: c.Observations.Moved(),
	}
}

// A verdict given on the recorded review oldest first
type correctionVerdict struct {
	verdict  feedback.Verdict
	edited   json.RawMessage
	reviewer string
}

// The review of tq-005 with its verdicts appended
func correctedReview(t *testing.T, client llm.Client, verdicts []correctionVerdict) corrected {
	t.Helper()
	r := recordReview(t, client)
	for _, v := range verdicts {
		fb, err := feedback.New(r.traceID, v.verdict, "a reason", v.edited, v.reviewer, r.Clock.Now())
		require.NoError(t, err)
		require.NoError(t, r.Feedback.Append(context.Background(), fb))
	}
	return r
}

func TestCorrection(t *testing.T) {
	edit := diagnose.Diagnosis{
		Status: evidence.StatusReadyForReview,
		Causes: []diagnose.Cause{{Summary: "clicks that never convert", ParagraphIDs: []string{correctionOutcome}}},
		Checks: diagnose.Checks{
			{Step: "confirm the signal", Purpose: "signal", ParagraphIDs: []string{correctionConfirm}},
			{Step: "check conversions", Purpose: "quality", ParagraphIDs: []string{correctionOutcome}},
		},
	}
	edited, err := json.Marshal(edit)
	require.NoError(t, err)
	type want struct {
		verdict   feedback.Verdict
		corrected *diagnose.Diagnosis
		// Lines the prompt of a draft carries
		present []string
	}
	tcs := []struct {
		name string
		// Appended oldest first
		args []correctionVerdict
		want want
	}{
		{
			"an edit carries both reviews and what changed",
			[]correctionVerdict{{feedback.VerdictEdit, edited, ""}},
			want{
				verdict: feedback.VerdictEdit, corrected: &edit,
				present: []string{
					"# Correction of event tq-005\n\nChange context: no_known_change\n",
					"Verdict: edit\nReason: a reason\nOriginal status: ready_for_review with 1 cause\n" +
						"Corrected status: ready_for_review with 1 cause\nCause paragraphs removed: " + correctionSegment +
						"\nCause paragraphs added: " + correctionOutcome + "\nCheck paragraphs added: " + correctionOutcome + "\n",
					"## Original causes\n\n- low quality traffic [" + correctionSegment + "]\n",
					"## Corrected causes\n\n- clicks that never convert [" + correctionOutcome + "]\n",
				},
			},
		},
		{
			"a reject marks the original review wrong",
			[]correctionVerdict{{feedback.VerdictReject, nil, ""}},
			want{
				verdict: feedback.VerdictReject,
				present: []string{"Original status: ready_for_review with 1 cause. The reviewer rejected this review as wrong\n"},
			},
		},
		{
			"a later verdict of a session is not a person's word",
			[]correctionVerdict{{feedback.VerdictReject, nil, ""}, {feedback.VerdictApprove, nil, feedback.ReviewerSession}},
			want{verdict: feedback.VerdictReject, present: []string{"rejected this review as wrong"}},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := correctedReview(t, nil, tc.args)

			got, err := r.diagnoser.Correction(ctx, r.traceID)

			require.NoError(t, err)
			assert.Equal(t, diagnose.Correction{
				TraceID: r.traceID, EventID: "tq-005", ChangeContext: evidence.ContextNoKnownChange, Moved: r.moved,
				Verdict: tc.want.verdict, Reason: "a reason", Original: r.review, Corrected: tc.want.corrected,
			}, got)
			assert.Equal(t, knowledge.Knowledge{
				Scope: knowledge.Scope{Scope: evidence.Scope{
					ChangeContexts: []evidence.Context{evidence.ContextNoKnownChange}, Metrics: r.moved,
				}},
				Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{r.traceID}},
				Basis:    knowledge.BasisStated,
			}, got.Proposal())
			for _, text := range tc.want.present {
				assert.Contains(t, got.String(), text)
			}
		})
	}
}

func TestCorrectionRefused(t *testing.T) {
	type args struct {
		verdicts []correctionVerdict
		// Empty means the recorded review
		id string
	}
	tcs := []struct {
		name string
		args args
		want error
	}{
		{
			"a later approval is no correction",
			args{verdicts: []correctionVerdict{{feedback.VerdictReject, nil, ""}, {feedback.VerdictApprove, nil, ""}}},
			diagnose.ErrNotCorrected,
		},
		{
			"a review with only a session verdict has no feedback",
			args{verdicts: []correctionVerdict{{feedback.VerdictReject, nil, feedback.ReviewerSession}}},
			diagnose.ErrNoFeedback,
		},
		{"a review without a verdict has no feedback", args{}, diagnose.ErrNoFeedback},
		{
			"an edited review that is no review is malformed",
			args{verdicts: []correctionVerdict{{feedback.VerdictEdit, json.RawMessage(`"rewritten"`), ""}}},
			diagnose.ErrMalformed,
		},
		{"a context trace is no review", args{id: "context"}, diagnose.ErrMalformed},
		{"an unknown trace is not found", args{id: "nope"}, trace.ErrNotFound},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := correctedReview(t, nil, tc.args.verdicts)
			id := map[string]string{"": r.traceID, "context": r.contextID, "nope": "nope"}[tc.args.id]

			got, err := r.diagnoser.Correction(ctx, id)

			assert.ErrorIs(t, err, tc.want)
			assert.Equal(t, diagnose.Correction{}, got)
		})
	}
}

func TestDraftContent(t *testing.T) {
	type args struct {
		// The model answer and its failure
		res  llm.Response
		fail error
	}
	type want struct {
		draft diagnose.ContentDraft
		err   error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"the content is trimmed and carries the cost",
			args{res: llm.Response{
				Output: json.RawMessage(`{"content":" Clicks that never convert are low quality. "}`), CostUSD: 0.004,
			}},
			want{draft: diagnose.ContentDraft{Content: "Clicks that never convert are low quality.", CostUSD: 0.004}},
		},
		{
			"an empty content is a bad output",
			args{res: llm.Response{Output: json.RawMessage(`{"content":" "}`)}},
			want{err: diagnose.ErrBadOutput},
		},
		{
			"an output outside the schema is a bad output",
			args{res: llm.Response{Output: json.RawMessage(`[]`)}},
			want{err: diagnose.ErrBadOutput},
		},
		{"a failed call is returned", args{fail: assert.AnError}, want{err: assert.AnError}},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := llmmock.NewMockClient(gomock.NewController(t))
			r := correctedReview(t, client, []correctionVerdict{{feedback.VerdictReject, nil, ""}})
			c, err := r.diagnoser.Correction(ctx, r.traceID)
			require.NoError(t, err)
			client.EXPECT().Complete(gomock.Any(), llm.Request{
				System: diagnose.DraftRules, Prompt: c.String(), Schema: json.RawMessage(diagnose.DraftSchema), Model: "haiku",
			}).Return(tc.args.res, tc.args.fail)

			got, err := r.diagnoser.DraftContent(ctx, c, "haiku")

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.draft, got)
		})
	}
}

func TestDraftContentWithoutClient(t *testing.T) {
	t.Parallel()
	r := recordReview(t, nil)
	_, err := r.diagnoser.DraftContent(context.Background(), diagnose.Correction{}, "")
	assert.ErrorIs(t, err, diagnose.ErrNoClient)
}
