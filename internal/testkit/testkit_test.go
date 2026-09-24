package testkit_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	feedbackfile "github.com/jeon-jihyeon/nodloop/internal/feedback/file"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	knowledgefile "github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// The demo source serves the checked in data and every record store starts empty
func TestOpen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := testkit.Open(t)

	ev, err := st.Source.Event(ctx, "tq-001")
	require.NoError(t, err)
	traces, err := st.Traces.List(ctx, trace.Filter{})
	require.NoError(t, err)
	verdicts, err := st.Feedback.List(ctx, feedback.Filter{})
	require.NoError(t, err)
	outcomes, err := st.Outcomes.List(ctx, "")
	require.NoError(t, err)
	items, err := st.Ledger.All(ctx)
	require.NoError(t, err)
	assert.Equal(t, "tq-001", ev.ID)
	assert.Empty(t, traces)
	assert.Empty(t, verdicts)
	assert.Empty(t, outcomes)
	assert.Empty(t, items)
}

func TestDemoDir(t *testing.T) {
	t.Parallel()
	_, err := os.Stat(filepath.Join(testkit.DemoDir(t), "events.csv"))
	assert.NoError(t, err)
}

// Each read returns the current time and moves the clock one step
func TestClockNow(t *testing.T) {
	t.Parallel()
	st := testkit.Open(t)
	first := st.Clock.Now()
	assert.Equal(t, first.Add(testkit.ClockStep), st.Clock.Now())
}

// Two reads show which of them the allowed count lets through
func TestFlakyKnowledgeList(t *testing.T) {
	type want struct {
		items []knowledge.Knowledge
		errs  []error
	}
	tcs := []struct {
		name string
		args int
		want want
	}{
		{
			name: "no allowed read fails the first read",
			args: 0,
			want: want{errs: []error{assert.AnError, assert.AnError}},
		},
		{
			name: "one allowed read serves the store and fails the second",
			args: 1,
			want: want{items: []knowledge.Knowledge{{ID: "a"}}, errs: []error{nil, assert.AnError}},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, err := knowledgefile.New(t.TempDir())
			require.NoError(t, err)
			require.NoError(t, store.Append(ctx, knowledge.Knowledge{ID: "a"}))
			flaky := &testkit.FlakyKnowledge{Store: store, Reads: testkit.Reads{Allowed: tc.args, Err: assert.AnError}}
			items, first := flaky.List(ctx)
			_, second := flaky.List(ctx)
			assert.Equal(t, tc.want, want{items: items, errs: []error{first, second}})
		})
	}
}

// Two reads show which of them the allowed count lets through
func TestFlakyFeedbackList(t *testing.T) {
	type want struct {
		items []feedback.Feedback
		errs  []error
	}
	tcs := []struct {
		name string
		args int
		want want
	}{
		{
			name: "no allowed read fails the first read",
			args: 0,
			want: want{errs: []error{assert.AnError, assert.AnError}},
		},
		{
			name: "one allowed read serves the store and fails the second",
			args: 1,
			want: want{items: []feedback.Feedback{{TraceID: "a"}}, errs: []error{nil, assert.AnError}},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, err := feedbackfile.New(t.TempDir())
			require.NoError(t, err)
			require.NoError(t, store.Append(ctx, feedback.Feedback{TraceID: "a"}))
			flaky := &testkit.FlakyFeedback{Store: store, Reads: testkit.Reads{Allowed: tc.args, Err: assert.AnError}}
			items, first := flaky.List(ctx, feedback.Filter{})
			_, second := flaky.List(ctx, feedback.Filter{})
			assert.Equal(t, tc.want, want{items: items, errs: []error{first, second}})
		})
	}
}
