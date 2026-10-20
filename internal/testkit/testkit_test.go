package testkit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	feedbackfile "github.com/jeon-jihyeon/nodloop/internal/feedback/file"
	knowledgefile "github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// Every record store starts empty
func TestOpen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := testkit.Open(t)

	traces, err := st.Traces.List(ctx, record.TraceFilter{})
	require.NoError(t, err)
	verdicts, err := st.Feedback.List(ctx, record.FeedbackFilter{})
	require.NoError(t, err)
	outcomes, err := st.Outcomes.List(ctx, "")
	require.NoError(t, err)
	items, err := st.Ledger.All(ctx)
	require.NoError(t, err)
	assert.Empty(t, traces)
	assert.Empty(t, verdicts)
	assert.Empty(t, outcomes)
	assert.Empty(t, items)
}

// Each read returns the current time and moves the clock one step
func TestClockNow(t *testing.T) {
	t.Parallel()
	st := testkit.Open(t)
	first := st.Clock.Now()
	assert.Equal(t, first.Add(testkit.ClockStep), st.Clock.Now())
}

// An advance moves the next read on by its duration and reads nothing itself
func TestClockAdvance(t *testing.T) {
	t.Parallel()
	st := testkit.Open(t)
	first := st.Clock.Now()

	st.Clock.Advance(time.Hour)

	assert.Equal(t, first.Add(testkit.ClockStep+time.Hour), st.Clock.Now())
}

// Two reads show which of them the allowed count lets through
func TestFlakyKnowledgeList(t *testing.T) {
	type want struct {
		items []record.Knowledge
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
			want: want{items: []record.Knowledge{{ID: "a"}}, errs: []error{nil, assert.AnError}},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, err := knowledgefile.New(t.TempDir())
			require.NoError(t, err)
			require.NoError(t, store.Append(ctx, record.Knowledge{ID: "a"}))
			flaky := &testkit.FlakyKnowledge{Store: store, Reads: testkit.Reads{Allowed: tc.args, Err: assert.AnError}}
			items, first := flaky.List(ctx)
			_, second := flaky.List(ctx)
			assert.Equal(t, tc.want, want{items: items, errs: []error{first, second}})
		})
	}
}

// A trace of another name is stored and a trace of the name panics before it is stored
func TestPanickingTracesAppend(t *testing.T) {
	tcs := []struct {
		name   string
		args   trace.Name
		panics assert.PanicAssertionFunc
		want   int
	}{
		{"a trace of another name is stored", trace.NameRun, assert.NotPanics, 1},
		{"a trace of the name panics", trace.NameCheck, assert.Panics, 0},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := testkit.Open(t)
			traces := testkit.PanickingTraces{Store: st.Traces, Name: trace.NameCheck}
			tr := trace.Trace{ID: "t-1", Name: tc.args, Time: st.Clock.Now()}
			tc.panics(t, func() { _ = traces.Append(ctx, tr.Record()) })
			stored, err := st.Traces.List(ctx, record.TraceFilter{})
			require.NoError(t, err)
			assert.Len(t, stored, tc.want)
		})
	}
}

// Two reads show which of them the allowed count lets through
func TestFlakyFeedbackList(t *testing.T) {
	type want struct {
		items []record.Feedback
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
			want: want{items: []record.Feedback{{TraceID: "a"}}, errs: []error{nil, assert.AnError}},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, err := feedbackfile.New(t.TempDir())
			require.NoError(t, err)
			require.NoError(t, store.Append(ctx, record.Feedback{TraceID: "a"}))
			flaky := &testkit.FlakyFeedback{Store: store, Reads: testkit.Reads{Allowed: tc.args, Err: assert.AnError}}
			items, first := flaky.List(ctx, record.FeedbackFilter{})
			_, second := flaky.List(ctx, record.FeedbackFilter{})
			assert.Equal(t, tc.want, want{items: items, errs: []error{first, second}})
		})
	}
}

// Two appends show which of them the allowed count lets through
func TestFlakyTracesAppend(t *testing.T) {
	type want struct {
		errs   []error
		stored int
	}
	tcs := []struct {
		name string
		args int
		want want
	}{
		{"no allowed append fails both", 0, want{[]error{assert.AnError, assert.AnError}, 0}},
		{"one allowed append stores the first and fails the second", 1, want{[]error{nil, assert.AnError}, 1}},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := testkit.Open(t)
			flaky := &testkit.FlakyTraces{Store: st.Traces, Appends: testkit.Reads{Allowed: tc.args, Err: assert.AnError}}
			first := flaky.Append(ctx, trace.Trace{ID: "t-1", Name: trace.NameRun, Time: st.Clock.Now()}.Record())
			second := flaky.Append(ctx, trace.Trace{ID: "t-2", Name: trace.NameRun, Time: st.Clock.Now()}.Record())
			stored, err := st.Traces.List(ctx, record.TraceFilter{})
			require.NoError(t, err)

			assert.Equal(t, tc.want, want{[]error{first, second}, len(stored)})
		})
	}
}

// Two lists show which of them the allowed count lets through and no error set fails none
func TestFlakyTracesList(t *testing.T) {
	type args struct {
		allowed int
		err     error
	}
	type want struct {
		errs  []error
		found []int
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"one allowed list reads the first and fails the second", args{1, assert.AnError}, want{[]error{nil, assert.AnError}, []int{1, 0}}},
		{"no error set fails no list", args{0, nil}, want{[]error{nil, nil}, []int{1, 1}}},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := testkit.Open(t)
			require.NoError(t, st.Traces.Append(ctx, trace.Trace{ID: "t-1", Name: trace.NameRun, Time: st.Clock.Now()}.Record()))
			flaky := &testkit.FlakyTraces{Store: st.Traces, Lists: testkit.Reads{Allowed: tc.args.allowed, Err: tc.args.err}}

			first, firstErr := flaky.List(ctx, record.TraceFilter{})
			second, secondErr := flaky.List(ctx, record.TraceFilter{})

			assert.Equal(t, tc.want, want{[]error{firstErr, secondErr}, []int{len(first), len(second)}})
		})
	}
}

// Two gets show which of them the allowed count lets through and no error set fails none
func TestFlakyTracesGet(t *testing.T) {
	type args struct {
		allowed int
		err     error
	}
	type want struct {
		errs  []error
		found []string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"one allowed get reads the first and fails the second", args{1, assert.AnError}, want{[]error{nil, assert.AnError}, []string{"t-1", ""}}},
		{"no error set fails no get", args{0, nil}, want{[]error{nil, nil}, []string{"t-1", "t-1"}}},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := testkit.Open(t)
			require.NoError(t, st.Traces.Append(ctx, trace.Trace{ID: "t-1", Name: trace.NameRun, Time: st.Clock.Now()}.Record()))
			flaky := &testkit.FlakyTraces{Store: st.Traces, Gets: testkit.Reads{Allowed: tc.args.allowed, Err: tc.args.err}}

			first, firstErr := flaky.Get(ctx, "t-1")
			second, secondErr := flaky.Get(ctx, "t-1")

			assert.Equal(t, tc.want, want{[]error{firstErr, secondErr}, []string{first.ID, second.ID}})
		})
	}
}
