package diagnose_test

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
			proposed, err := got.Proposal(knowledge.Knowledge{})
			require.NoError(t, err)
			assert.Equal(t, knowledge.Knowledge{
				Scope: knowledge.Scope{Scope: evidence.Scope{
					ChangeContexts: []evidence.Context{evidence.ContextNoKnownChange}, Metrics: r.moved,
				}},
				Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{r.traceID}},
				Basis:    knowledge.BasisStated,
			}, proposed)
			for _, text := range tc.want.present {
				assert.Contains(t, got.String(), text)
			}
		})
	}
}

// Where no metric moved an empty metric axis would reach every event of the change context
// so only change contexts the draft names itself are accepted
// A metric scope would never reach an event where no metric moved
func TestCorrectionProposal(t *testing.T) {
	type args struct {
		moved []string
		// The scope of the draft
		contexts []evidence.Context
		metrics  []string
	}
	type want struct {
		proposed knowledge.Knowledge
		err      error
	}
	noKnownChange := []evidence.Context{evidence.ContextNoKnownChange}
	filled := func(metrics []string) knowledge.Knowledge {
		return knowledge.Knowledge{
			Content: "c", Scope: knowledge.Scope{Scope: evidence.Scope{ChangeContexts: noKnownChange, Metrics: metrics}},
			Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{"d1"}}, Basis: knowledge.BasisStated,
		}
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"moved metrics fill the metric axis", args{moved: []string{"click_count"}}, want{proposed: filled([]string{"click_count"})}},
		{"no metric moved and no scope named fails", args{}, want{err: diagnose.ErrQuietScope}},
		{
			"no metric moved and a change context named accepts every event of it",
			args{contexts: noKnownChange},
			want{proposed: filled(nil)},
		},
		{
			"no metric moved and a metric named fails because the scope misses events like the corrected one",
			args{metrics: []string{"conversion_count"}},
			want{err: diagnose.ErrQuietMetricScope},
		},
		{
			"no metric moved and a change context and a metric named fails",
			args{contexts: noKnownChange, metrics: []string{"conversion_count"}},
			want{err: diagnose.ErrQuietMetricScope},
		},
		{
			"moved metrics and a metric named keep the narrowed metric",
			args{moved: []string{"click_count"}, metrics: []string{"conversion_count"}},
			want{proposed: filled([]string{"conversion_count"})},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := diagnose.Correction{TraceID: "d1", ChangeContext: evidence.ContextNoKnownChange, Moved: tc.args.moved}
			draft := knowledge.Knowledge{Content: "c", Scope: knowledge.Scope{Scope: evidence.Scope{
				ChangeContexts: tc.args.contexts, Metrics: tc.args.metrics,
			}}}

			got, err := c.Proposal(draft)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.proposed, got)
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
		{"a context trace is no review", args{id: "context"}, trace.ErrNotReview},
		{"a failed review with a reject has nothing to correct", args{id: "failed"}, trace.ErrFailedReview},
		{"an unknown trace is not found", args{id: "nope"}, trace.ErrNotFound},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := correctedReview(t, nil, tc.args.verdicts)
			// A review that failed on the context of another event carries a reject too
			failed := trace.Trace{ID: "failed", Name: trace.NameDiagnose, Subject: "tq-007", Error: "model timed out", Time: r.Clock.Now()}
			require.NoError(t, r.Traces.Append(ctx, failed))
			fb, err := feedback.New(failed.ID, feedback.VerdictReject, "a reason", nil, "", r.Clock.Now())
			require.NoError(t, err)
			require.NoError(t, r.Feedback.Append(ctx, fb))
			id := map[string]string{"": r.traceID, "context": r.contextID, "failed": failed.ID, "nope": "nope"}[tc.args.id]

			got, err := r.diagnoser.Correction(ctx, id)

			assert.ErrorIs(t, err, tc.want)
			assert.Equal(t, diagnose.Correction{}, got)
		})
	}
}

const (
	checkDecide  = "metric-anomaly-investigation#Metric anomaly investigation/Decide#1"
	checkLanding = "landing-page-check#Landing page check/Broken landing page#1"
	checkLoad    = "landing-page-check#Landing page check/Load the landing page#1"
	// The new first step once a section moves in front of Confirm the signal
	checkScope   = "metric-anomaly-investigation#Metric anomaly investigation/Scope the change#1"
	checkShare   = "segment-concentration-review#Segment concentration review/Compare against total volume#1"
	checkRenamed = "metric-anomaly-investigation#Metric anomaly investigation/Check conversions#1"
)

// One edit of a demo procedure file made after the review
type procedureEdit struct {
	file, old, replacement string
}

var (
	// A section moved to the front makes it the first step
	movedSection = procedureEdit{"metric-anomaly-investigation.md", "## Confirm the signal", "## Scope the change\n\nFind the window first.\n\n## Confirm the signal"}
	// A heading rename moves the ids of its paragraphs
	renamedHeading = procedureEdit{"metric-anomaly-investigation.md", "## Check downstream outcomes", "## Check conversions"}
	// A front matter scope takes the procedure away from a no_known_change event
	scopedAway = procedureEdit{"segment-concentration-review.md", "", "---\nchange_contexts: [planned_operational_change]\n---\n"}
	// A runbook written after the review whose scope fits every event
	landingRunbook = evidence.Procedure{Slug: "landing-page-check", Paragraphs: []evidence.Paragraph{
		{ID: "landing-page-check#Landing page check#1"}, {ID: checkLoad}, {ID: checkLanding},
		{ID: "landing-page-check#Landing page check/Decide#1"},
	}}
)

// The demo procedures after the edit with the added ones at the end
func currentProcedures(t *testing.T, edit procedureEdit, added ...evidence.Procedure) evidence.Procedures {
	t.Helper()
	src := editedDemo(t, cmp.Or(edit.file, "data-integrity-hold.md"), edit.old, edit.replacement)
	ps, err := src.Procedures(context.Background())
	require.NoError(t, err)
	return append(ps, added...)
}

// A ready_for_review whose one cause cites ids
func citing(ids ...string) string {
	return fmt.Sprintf(`{"status":"ready_for_review","causes":[{"summary":"clicks that never convert","paragraph_ids":[%s]}]}`,
		`"`+strings.Join(ids, `","`)+`"`)
}

func TestCheckEdit(t *testing.T) {
	type args struct {
		// The trace the edit is checked against
		// Empty means the context trace of the recorded review
		built string
		// The edit of the data set after the review
		edit procedureEdit
		// Procedures added to the data set after the review
		added []evidence.Procedure
		// Set when the procedures folder could not be read so the caller passes none
		unread bool
		edited string
	}
	type want struct {
		err error
		// Text the refusal carries
		text []string
		// Text the refusal ends with
		suffix string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"an edit citing a listed paragraph passes", args{edited: citing(correctionOutcome)}, want{}},
		{"an edit of the status to hold alone passes", args{edited: `{"status":"hold"}`}, want{}},
		{"an edit of the status to no_action alone passes", args{edited: `{"status":"no_action"}`}, want{}},
		{"a misspelled key is refused", args{edited: `{"status":"hold","cause":[]}`}, want{err: diagnose.ErrEditInvalid}},
		{"a status outside the valid set is refused", args{edited: `{"status":"needs-review"}`}, want{err: diagnose.ErrEditInvalid}},
		{"a missing status is refused", args{edited: `{"observations":["x"]}`}, want{err: diagnose.ErrEditInvalid}},
		{
			"a paragraph id no procedure holds is refused naming the id and the procedures that apply",
			args{edited: citing("made-up#1")},
			want{err: diagnose.ErrEditInvalid, text: []string{
				"paragraph ids the procedures that apply to event tq-005 do not list now: made-up#1",
				"Procedures that apply: data-integrity-hold, metric-anomaly-investigation, outcome-rate-degradation, segment-concentration-review",
			}},
		},
		{
			"a paragraph id of a procedure added after the review passes",
			args{added: []evidence.Procedure{landingRunbook}, edited: citing(checkLanding)}, want{},
		},
		{
			"a paragraph id of a procedure added after the review is refused once the procedure is gone",
			args{edited: citing(checkLanding)}, want{err: diagnose.ErrEditInvalid},
		},
		{"a cause citing only a first step is refused", args{edited: citing(correctionConfirm)}, want{err: diagnose.ErrEditInvalid}},
		{
			"a cause citing only the first step of a procedure added after the review is refused",
			args{added: []evidence.Procedure{landingRunbook}, edited: citing(checkLoad)}, want{err: diagnose.ErrEditInvalid},
		},
		{"a cause citing only a Decide paragraph is refused", args{edited: citing(checkDecide)}, want{err: diagnose.ErrEditInvalid}},
		{"a cause citing a first step and a later step passes", args{edited: citing(correctionConfirm, correctionSegment)}, want{}},
		{
			"a cause citing three ids with the stating one last is refused naming the cut",
			args{edited: citing(correctionConfirm, checkDecide, correctionOutcome)},
			want{err: diagnose.ErrEditInvalid, text: []string{"causes cite more than 2 paragraph ids: clicks that never convert"}},
		},
		{
			"a cause citing three ids with the stating one first is refused naming the cut",
			args{edited: citing(correctionOutcome, correctionConfirm, checkDecide)},
			want{err: diagnose.ErrEditInvalid, text: []string{"causes cite more than 2 paragraph ids: clicks that never convert"}},
		},
		{
			"a cause repeating an id within two distinct ids passes",
			args{edited: citing(correctionOutcome, correctionOutcome, correctionSegment)}, want{},
		},
		{
			"a no_action with a cause is refused",
			args{edited: `{"status":"no_action","causes":[{"summary":"x","paragraph_ids":["` + correctionSegment + `"]}]}`},
			want{err: diagnose.ErrEditInvalid},
		},
		{"a ready_for_review without a cause is refused", args{edited: `{"status":"ready_for_review"}`}, want{err: diagnose.ErrEditInvalid}},
		{
			"the old first step passes once a section moved in front of it",
			args{edit: movedSection, edited: citing(correctionConfirm)}, want{},
		},
		{
			"the section moved to the front is the first step and is refused alone",
			args{edit: movedSection, edited: citing(checkScope)},
			want{err: diagnose.ErrEditInvalid, text: []string{"the gate would hold a review that follows it"}},
		},
		{
			"the id a heading rename removed is refused naming it",
			args{edit: renamedHeading, edited: citing(correctionOutcome)},
			want{err: diagnose.ErrEditInvalid, text: []string{"do not list now: " + correctionOutcome}},
		},
		{"the id a heading rename made passes", args{edit: renamedHeading, edited: citing(checkRenamed)}, want{}},
		{
			"a hold that keeps the id a heading rename removed is refused",
			args{edit: renamedHeading, edited: `{"status":"hold","checks":[{"step":"s","purpose":"p","paragraph_ids":["` + correctionOutcome + `"]}]}`},
			want{err: diagnose.ErrEditInvalid, text: []string{"cite the id the paragraph has now or drop the id"}},
		},
		{
			"an id of a procedure whose scope no longer fits the event is refused",
			args{edit: scopedAway, edited: citing(checkShare)},
			want{err: diagnose.ErrEditInvalid, text: []string{"do not list now: " + checkShare}, suffix: "Procedures that apply: " +
				"data-integrity-hold, metric-anomaly-investigation, outcome-rate-degradation"},
		},
		{
			"an id of the context is refused when the procedures could not be read",
			args{unread: true, edited: citing(correctionOutcome)},
			want{err: diagnose.ErrEditInvalid, suffix: "Procedures that apply: none"},
		},
		{"a hold alone passes when the procedures could not be read", args{unread: true, edited: `{"status":"hold"}`}, want{}},
		{
			"a review trace in place of its context is refused",
			args{built: "review", edited: `{"status":"hold"}`}, want{err: diagnose.ErrNotContext},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := recordReview(t, nil)
			built, err := r.Traces.Get(ctx, map[string]string{"": r.contextID, "review": r.traceID}[tc.args.built])
			require.NoError(t, err)
			var current evidence.Procedures
			if !tc.args.unread {
				current = currentProcedures(t, tc.args.edit, tc.args.added...)
			}

			err = diagnose.CheckEdit(built, current, json.RawMessage(tc.args.edited))

			assert.ErrorIs(t, err, tc.want.err)
			for _, text := range tc.want.text {
				assert.ErrorContains(t, err, text)
			}
			if tc.want.suffix != "" {
				require.Error(t, err)
				assert.True(t, strings.HasSuffix(err.Error(), tc.want.suffix), err.Error())
			}
		})
	}
}

// An edit passes exactly when a later review of the event that follows it is recorded without a forced hold
// The later review reads the edited data set and is recorded after its one send back
func TestCheckEditAgreesWithTheNextReview(t *testing.T) {
	type args struct {
		edit   procedureEdit
		review diagnose.Diagnosis
	}
	ready := func(ids ...string) diagnose.Diagnosis {
		return diagnose.Diagnosis{
			Status: evidence.StatusReadyForReview,
			Causes: []diagnose.Cause{{Summary: "clicks that never convert", ParagraphIDs: ids}},
		}
	}
	tcs := []struct {
		name string
		args args
		// Whether the later review is held by force
		want bool
	}{
		{"a listed later step", args{review: ready(correctionOutcome)}, false},
		{"the old first step after a section moved in front of it", args{edit: movedSection, review: ready(correctionConfirm)}, false},
		{"the section moved to the front", args{edit: movedSection, review: ready(checkScope)}, true},
		{"the id a heading rename removed", args{edit: renamedHeading, review: ready(correctionOutcome)}, true},
		{"the id a heading rename made", args{edit: renamedHeading, review: ready(checkRenamed)}, false},
		{"an id of a procedure scoped away from the event", args{edit: scopedAway, review: ready(checkShare)}, true},
		{"the first step of the context", args{review: ready(correctionConfirm)}, true},
		{"three ids with the stating one last", args{review: ready(correctionConfirm, checkDecide, correctionOutcome)}, true},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := recordReview(t, nil)
			built, err := r.Traces.Get(ctx, r.contextID)
			require.NoError(t, err)
			edited, err := json.Marshal(tc.args.review)
			require.NoError(t, err)
			src := editedDemo(t, cmp.Or(tc.args.edit.file, "data-integrity-hold.md"), tc.args.edit.old, tc.args.edit.replacement)
			current, err := src.Procedures(ctx)
			require.NoError(t, err)
			later := diagnose.New(src, testkit.Policy(t), nil, r.Traces, r.Feedback, r.Ledger, r.Clock.Now)
			c, err := later.Prepare(ctx, "tq-005", diagnose.ModeInteractive, diagnose.Session{})
			require.NoError(t, err)

			checkErr := diagnose.CheckEdit(built, current, edited)
			res, err := later.Record(ctx, c.PendingID, tc.args.review)
			require.NoError(t, err)
			if len(res.Revisions) > 0 {
				res, err = later.Record(ctx, c.PendingID, tc.args.review)
				require.NoError(t, err)
			}

			assert.Equal(t, []bool{tc.want, tc.want}, []bool{errors.Is(checkErr, diagnose.ErrEditInvalid), res.Forced})
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
