package loop_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/diagnose"
	"github.com/jeon-jihyeon/nodloop/internal/evidence"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/loop"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// A Monday so week bounds are easy to read
var monday = time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)

// One diagnose trace as the conversation records it
// The zero value is a conversation review under no known change that applied nothing and cites one paragraph
type review struct {
	id, ref   string
	batch     bool
	failed    bool
	context   evidence.Context
	knowledge []diagnose.AppliedKnowledge
	status    evidence.Status
	cites     []string
	tags      []string
	at        time.Time
}

func (r review) trace(t *testing.T) trace.Trace {
	t.Helper()
	mode := diagnose.ModeInteractive
	if r.batch {
		mode = diagnose.ModeBatch
	}
	in, err := json.Marshal(map[string]any{
		"mode": mode, "change_context": orContext(r.context), "knowledge": orKnowledge(r.knowledge),
	})
	require.NoError(t, err)
	status := r.status
	if status == "" {
		status = evidence.StatusReadyForReview
	}
	cites := r.cites
	if cites == nil {
		cites = []string{"p#1"}
	}
	out, err := json.Marshal(diagnose.Diagnosis{
		Status: status, Causes: []diagnose.Cause{{Summary: "cause", ParagraphIDs: cites}}, Checks: diagnose.Checks{},
	})
	require.NoError(t, err)
	tr := trace.Trace{
		ID: r.id, Name: trace.NameDiagnose, Subject: "ev-" + r.id, Ref: r.ref, Time: r.at, Input: in, Output: out, Tags: r.tags,
	}
	if tr.Ref == "" {
		tr.Ref = "ctx-" + r.id
	}
	if tr.Time.IsZero() {
		tr.Time = monday
	}
	if r.failed {
		tr.Error, tr.Output = "model failed", nil
	}
	return tr
}

func orContext(c evidence.Context) evidence.Context {
	if c == "" {
		return evidence.ContextNoKnownChange
	}
	return c
}

func orKnowledge(k []diagnose.AppliedKnowledge) []diagnose.AppliedKnowledge {
	if k == nil {
		return []diagnose.AppliedKnowledge{}
	}
	return k
}

func traces(t *testing.T, reviews ...review) trace.Traces {
	t.Helper()
	out := make(trace.Traces, 0, len(reviews))
	for _, r := range reviews {
		out = append(out, r.trace(t))
	}
	return out
}

// The first submission of a context sent back with the status
func revise(t *testing.T, ref string, status evidence.Status) trace.Trace {
	t.Helper()
	out, err := json.Marshal(diagnose.Diagnosis{Status: status})
	require.NoError(t, err)
	return trace.Trace{ID: "revise-" + ref, Name: trace.NameRevise, Ref: ref, Time: monday, Output: out}
}

func applied(id string, version int) diagnose.AppliedKnowledge {
	return diagnose.AppliedKnowledge{ID: id, Version: version, Chars: 40}
}

func verdict(traceID string, v feedback.Verdict, at time.Time) feedback.Feedback {
	return feedback.Feedback{TraceID: traceID, Verdict: v, Time: at, Reviewer: feedback.ReviewerAuthor}
}

func outcome(traceID string, r feedback.Result, at time.Time) feedback.Outcome {
	return feedback.Outcome{TraceID: traceID, Result: r, Time: at, Reviewer: feedback.ReviewerAuthor}
}

func item(id string, version int, status knowledge.Status) knowledge.Knowledge {
	return knowledge.Knowledge{
		ID: id, Version: version, Kind: knowledge.KindMeaning, Content: id, Status: status, Basis: knowledge.BasisStated,
		Evidence: knowledge.Evidence{ParagraphIDs: []string{"p#1"}}, Author: "author", Approver: "ann",
		ApprovedAt: monday, Time: monday,
	}
}

func TestNew(t *testing.T) {
	failed := review{id: "r2", failed: true}.trace(t)
	failed.Output = json.RawMessage(`not json`)
	brokenRevise := revise(t, "ctx-r3", evidence.StatusHold)
	brokenRevise.Output = json.RawMessage(`not json`)
	type args struct {
		traces   trace.Traces
		verdicts feedback.Records
	}
	tcs := []struct {
		name string
		args args
		want int
	}{
		{"a failed review is never read", args{traces: trace.Traces{failed}}, 0},
		{"a revise output that does not read is skipped", args{traces: trace.Traces{brokenRevise}}, 0},
		{"a batch review stays out of the queue", args{traces: traces(t, review{id: "r4", batch: true})}, 0},
		{
			"a session verdict leaves the review pending",
			args{
				traces: traces(t, review{id: "r5"}),
				verdicts: feedback.Records{{
					TraceID: "r5", Verdict: feedback.VerdictApprove, Time: monday, Reviewer: feedback.ReviewerSession,
				}},
			},
			1,
		},
		{
			"a human verdict takes the review off the queue",
			args{traces: traces(t, review{id: "r6"}), verdicts: feedback.Records{verdict("r6", feedback.VerdictApprove, monday)}},
			0,
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h, err := loop.New(tc.args.traces, tc.args.verdicts, nil, nil)
			require.NoError(t, err)
			got, err := h.Queue(loop.QueueOptions{})
			require.NoError(t, err)
			assert.Len(t, got, tc.want)
		})
	}
}

func TestLoad(t *testing.T) {
	ctx := context.Background()
	st := testkit.Open(t)
	require.NoError(t, st.Traces.Append(ctx, review{id: "r1"}.trace(t)))
	type want struct {
		loaded bool
		err    error
	}
	tcs := []struct {
		name string
		args loop.FeedbackStore
		want want
	}{
		{"the stores are joined", st.Feedback, want{loaded: true}},
		{
			"a failing feedback read fails the load",
			&testkit.FlakyFeedback{Store: st.Feedback, Reads: testkit.Reads{Err: assert.AnError}},
			want{err: assert.AnError},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h, err := loop.Load(ctx, st.Traces, tc.args, st.Outcomes, st.Ledger)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.loaded, h != nil)
		})
	}
}

func TestHistoryReviews(t *testing.T) {
	all := traces(t, review{id: "r1"}, review{id: "r2"}, review{id: "batch", batch: true})
	h, err := loop.New(all, nil, nil, nil)
	require.NoError(t, err)
	tcs := []struct {
		name string
		args []string
		want map[string]json.RawMessage
	}{
		{"a named review comes back with its output", []string{"r2"}, map[string]json.RawMessage{"r2": all[1].Output}},
		{"an id of a batch review or of no trace is left out", []string{"batch", "nope"}, map[string]json.RawMessage{}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, h.Reviews(tc.args))
		})
	}
}

func TestNewMalformed(t *testing.T) {
	broken := review{id: "r1"}.trace(t)
	broken.Output = json.RawMessage(`{"status":"maybe"}`)
	tcs := []struct {
		name string
		args trace.Traces
		want error
	}{
		{"a malformed review fails the join", trace.Traces{broken}, diagnose.ErrMalformed},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h, err := loop.New(tc.args, nil, nil, nil)
			assert.ErrorIs(t, err, tc.want)
			assert.Nil(t, h)
		})
	}
}
