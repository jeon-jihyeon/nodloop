package feedback_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/schema"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

func TestFeedbackRecord(t *testing.T) {
	at := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	tcs := []struct {
		name string
		args feedback.Feedback
	}{
		{
			"every field",
			feedback.Feedback{
				TraceID: "t1", Time: at, Verdict: feedback.VerdictEdit, ReasonCode: feedback.ReasonScope, Reason: "reorder",
				Edited: json.RawMessage(`{"order":["db"]}`), Reviewer: "author", Proof: trace.ProofTerminal, Audit: true,
			},
		},
		{"only the required fields", feedback.Feedback{TraceID: "t2", Time: at, Verdict: feedback.VerdictApprove, Reviewer: "session"}},
		{"a named verdict of an earlier release keeps no proof", feedback.Feedback{TraceID: "t3", Time: at, Verdict: feedback.VerdictReject, Reviewer: "ann"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model, err := json.Marshal(tc.args)
			require.NoError(t, err)
			want := fmt.Sprintf(`%s,"schema_version":%d}`, model[:len(model)-1], feedback.VerdictVersion)
			got, err := json.Marshal(tc.args.Record())
			require.NoError(t, err)
			assert.Equal(t, want, string(got), "the line encodes to the bytes of the verdict and the version of its schema")
			assert.Equal(t, tc.args, feedback.Restore(tc.args.Record()))
		})
	}
}

func TestOutcomeRecord(t *testing.T) {
	at := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	tcs := []struct {
		name string
		args feedback.Outcome
	}{
		{
			"every field",
			feedback.Outcome{
				TraceID: "t1", Time: at, Result: feedback.ResultConfirmed, ConfirmedCause: "disk full", Note: "checked",
				Reviewer: "author",
			},
		},
		{"only the required fields", feedback.Outcome{TraceID: "t2", Time: at, Result: feedback.ResultRefuted, Reviewer: "author"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model, err := json.Marshal(tc.args)
			require.NoError(t, err)
			want := fmt.Sprintf(`%s,"schema_version":%d}`, model[:len(model)-1], feedback.OutcomeVersion)
			got, err := json.Marshal(tc.args.Record())
			require.NoError(t, err)
			assert.Equal(t, want, string(got), "the line encodes to the bytes of the outcome and the version of its schema")
			assert.Equal(t, tc.args, feedback.RestoreOutcome(tc.args.Record()))
		})
	}
}

func TestRestoreAll(t *testing.T) {
	at := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	reject := feedback.Feedback{TraceID: "t1", Time: at, Verdict: feedback.VerdictReject, Reviewer: "ann"}
	approve := feedback.Feedback{TraceID: "t2", Time: at, Verdict: feedback.VerdictApprove, Reviewer: "ann"}
	tcs := []struct {
		name string
		args []feedback.Feedback
		want feedback.Records
	}{
		{"no lines stay nil", nil, nil},
		{"lines keep the order the store listed them", []feedback.Feedback{approve, reject}, feedback.Records{approve, reject}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var lines []record.Feedback
			for _, fb := range tc.args {
				lines = append(lines, fb.Record())
			}
			assert.Equal(t, tc.want, feedback.RestoreAll(lines))
		})
	}
}

func TestRestoreOutcomes(t *testing.T) {
	at := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	refuted := feedback.Outcome{TraceID: "t1", Time: at, Result: feedback.ResultRefuted, Reviewer: "ann"}
	confirmed := feedback.Outcome{TraceID: "t2", Time: at, Result: feedback.ResultConfirmed, Reviewer: "ann"}
	tcs := []struct {
		name string
		args []feedback.Outcome
		want feedback.Outcomes
	}{
		{"no lines stay nil", nil, nil},
		{"lines keep the order the store listed them", []feedback.Outcome{confirmed, refuted}, feedback.Outcomes{confirmed, refuted}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var lines []record.Outcome
			for _, o := range tc.args {
				lines = append(lines, o.Record())
			}
			assert.Equal(t, tc.want, feedback.RestoreOutcomes(lines))
		})
	}
}

// A verdict, a captured verdict and an outcome of the constructors validate against their published schemas
func TestRecordsValidate(t *testing.T) {
	at := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	edited, err := feedback.New("t1", feedback.VerdictEdit, feedback.ReasonScope, "reorder", json.RawMessage(`{"order":["db"]}`), "ann", at)
	require.NoError(t, err)
	captured, err := feedback.NewCaptured("t1", feedback.VerdictReject, feedback.ReasonOther, "no, quote the window", at)
	require.NoError(t, err)
	checked, err := feedback.NewOutcome("t1", feedback.ResultConfirmed, "disk full", "checked", "ann", at)
	require.NoError(t, err)

	verdict := schema.Validate("verdict", edited.WithProof(trace.ProofTerminal).Record())
	capture := schema.Validate("verdict", captured.Record())
	outcome := schema.Validate("outcome", checked.Record())

	assert.NoError(t, verdict)
	assert.NoError(t, capture)
	assert.NoError(t, outcome)
}
