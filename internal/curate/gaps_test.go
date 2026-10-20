package curate_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// The corrected runs of the window no extraction read and those whose only extraction failed
func TestCuratorGaps(t *testing.T) {
	ctx := context.Background()
	b := newBench(t, nil, []knowledge.Knowledge{lesson("k-cited", 1, "Pin the base image", acme("acme"), knowledge.StatusCandidate, "")}, nil)
	now := b.stores.Clock.Now()
	labels := trace.Labels{"repo": {"acme"}}
	for _, id := range []string{"r-gap", "r-failed-once", "r-failed-twice", "r-refused", "r-young"} {
		b.correct(t, id, labels, []feedback.Verdict{feedback.VerdictReject})
	}
	b.correct(t, "r-approved", labels, []feedback.Verdict{feedback.VerdictReject, feedback.VerdictApprove})
	young, err := feedback.New("r-young", feedback.VerdictReject, "", "", nil, "ann", now.Add(-10*time.Minute))
	require.NoError(t, err)
	require.NoError(t, b.stores.Feedback.Append(ctx, young.Record()))
	inferred, err := trace.NewRun("bot", "", trace.Labels{"unit": {"a"}}, json.RawMessage(`{"applied":[]}`), []byte("an answer"), written)
	require.NoError(t, err)
	inferred.ID = "r-inferred"
	require.NoError(t, b.stores.Traces.Append(ctx, inferred.Record()))
	said, err := feedback.New("r-inferred", feedback.VerdictReject, "", "", nil, feedback.ReviewerSession, written.Add(time.Minute))
	require.NoError(t, err)
	require.NoError(t, b.stores.Feedback.Append(ctx, said.Record()))
	failed, err := trace.NewRun("session", "", labels, json.RawMessage(`{"applied":[]}`), []byte("an answer"), written)
	require.NoError(t, err)
	failed.ID, failed.Error = "r-error", "the run failed"
	require.NoError(t, b.stores.Traces.Append(ctx, failed.Record()))
	onFailed, err := feedback.New("r-error", feedback.VerdictReject, "", "", nil, "ann", written.Add(time.Minute))
	require.NoError(t, err)
	require.NoError(t, b.stores.Feedback.Append(ctx, onFailed.Record()))
	for i, e := range []struct{ run, conclusion string }{
		{"r-failed-once", "failed"}, {"r-failed-twice", "failed"}, {"r-failed-twice", "failed"}, {"r-refused", "refused"},
	} {
		extract := trace.Trace{
			ID: fmt.Sprintf("e-%d", i), Name: trace.NameExtract, Ref: e.run, Time: written.Add(time.Hour), Input: json.RawMessage(`{}`),
			Output: json.RawMessage(`{"conclusion":"` + e.conclusion + `"}`),
		}
		require.NoError(t, b.stores.Traces.Append(ctx, extract.Record()))
	}

	got, err := b.curator().Gaps(ctx, time.Time{})

	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"r-gap", "r-failed-once", "r-inferred"}, got)
}

// The gaps fail on verdicts or knowledge that cannot be read
func TestCuratorGapsFails(t *testing.T) {
	type args struct {
		// Reads that pass before every later one fails
		lists, verdicts, knowledge int
	}
	tcs := []struct {
		name string
		args args
	}{
		{"extractions that cannot be read", args{1, -1, 1 << 20}},
		{"verdicts that cannot be read", args{-1, 0, 1 << 20}},
		{"knowledge that cannot be read", args{-1, -1, 0}},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := newBench(t, nil, nil, nil)
			traces := &testkit.FlakyTraces{Store: b.stores.Traces, Lists: failing(tc.args.lists)}
			verdicts := &testkit.FlakyFeedback{Store: b.stores.Feedback, Reads: failing(tc.args.verdicts)}
			curator := curate.New(flakyLedger(t, b, tc.args.knowledge), traces, verdicts, b.client, nil, seal, b.stores.Clock.Now)

			got, err := curator.Gaps(ctx, time.Time{})

			assert.ErrorIs(t, err, assert.AnError)
			assert.Nil(t, got)
		})
	}
}
