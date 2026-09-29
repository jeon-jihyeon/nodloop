package eval_test

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/diagnose"
	"github.com/jeon-jihyeon/nodloop/internal/eval"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// Seven feedback:off reviews of session triage
// A second repeat that answers hold everywhere must not count
func TestTriage(t *testing.T) {
	const na = -1
	at := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	expected := map[string]string{
		"tq-003": "no_action", "tq-004": "no_action", "tq-007": "ready_for_review", "tq-008": "ready_for_review",
		"tq-011": "ready_for_review", "tq-012": "ready_for_review", "tq-015": "ready_for_review",
	}
	review := func(id, event, status string, tags ...string) trace.Trace {
		return trace.Trace{
			ID: id, Name: trace.NameDiagnose, SessionID: "triage", Subject: event, Time: at,
			Tags: append([]string{string(eval.ConditionBaseline)}, tags...), Input: json.RawMessage(`{}`),
			Output: json.RawMessage(fmt.Sprintf(`{"status":%q}`, status)),
		}
	}
	right := func(tags []string, events ...string) []trace.Trace {
		var out []trace.Trace
		for _, event := range events {
			out = append(out, review(event, event, expected[event], tags...))
		}
		return out
	}
	failed := func(events ...string) []trace.Trace {
		var out []trace.Trace
		for _, event := range events {
			tr := review(event, event, "")
			tr.Error, tr.Output = "model failure", nil
			out = append(out, tr)
		}
		return out
	}
	secondRepeat := make([]trace.Trace, 0, len(expected))
	for _, event := range slices.Sorted(maps.Keys(expected)) {
		secondRepeat = append(secondRepeat, review(event+"-2", event, "hold", "repeat:2"))
	}
	forcedHold := review("tq-003", "tq-003", "hold", diagnose.TagGateHold)
	type want struct {
		errors, unranked, top, rest int
		recall, topRate             float64
	}
	tcs := []struct {
		name string
		args []trace.Trace
		want want
	}{
		{
			"a forced hold ranks first and a failure stays in the recall denominator",
			slices.Concat(right(nil, "tq-004", "tq-007", "tq-008", "tq-011", "tq-012"), []trace.Trace{forcedHold}, failed("tq-015")),
			want{errors: 2, unranked: 1, top: 5, rest: 1, recall: 0.5, topRate: 0.2},
		},
		{
			"no wrong status has no recall",
			right(nil, "tq-003", "tq-004", "tq-007", "tq-008", "tq-011", "tq-012", "tq-015"),
			want{top: 5, rest: 2, recall: na, topRate: 0},
		},
		{
			"every review failed",
			failed("tq-003", "tq-004", "tq-007", "tq-008", "tq-011", "tq-012", "tq-015"),
			want{errors: 7, unranked: 7, recall: 0, topRate: na},
		},
		{
			"only the first repeat is ranked",
			slices.Concat(
				right([]string{"repeat:1"}, "tq-004", "tq-007", "tq-008", "tq-011", "tq-012", "tq-015"),
				[]trace.Trace{review("tq-003", "tq-003", "hold", diagnose.TagGateHold, "repeat:1")}, secondRepeat,
			),
			want{errors: 1, top: 5, rest: 2, recall: 1, topRate: 0.2},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := testkit.Open(t)
			for _, tr := range tc.args {
				require.NoError(t, s.Traces.Append(ctx, tr))
			}
			r := eval.New(s.Source, nil, s.Traces, s.Feedback, s.Ledger)
			rows, err := r.Triage(ctx, "triage")
			require.NoError(t, err)
			require.Len(t, rows, 1)
			got := rows[0]
			assert.Equal(t, 7, got.Events)
			assert.Equal(t, tc.want.errors, got.Errors)
			assert.Equal(t, tc.want.unranked, got.Unranked)
			assert.Equal(t, tc.want.top, got.Top.Events)
			assert.Equal(t, tc.want.rest, got.Rest.Events)
			assert.InDelta(t, tc.want.recall, got.RecallAtK, 1e-12)
			assert.InDelta(t, tc.want.topRate, got.Top.ErrorRate, 1e-12)
			rep, err := r.Report(ctx, "triage")
			require.NoError(t, err)
			rep.Triage = rows
			assert.Contains(t, rep.Table(), "Triage order")
		})
	}
}

func TestTriageWithoutSession(t *testing.T) {
	tcs := []struct {
		name string
		args string
		want error
	}{
		{"a session without reviews is refused", "none", eval.ErrNoTraces},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := testkit.Open(t)
			_, err := eval.New(s.Source, nil, s.Traces, s.Feedback, s.Ledger).Triage(ctx, tc.args)
			assert.ErrorIs(t, err, tc.want)
		})
	}
}
