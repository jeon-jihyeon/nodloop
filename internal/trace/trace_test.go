package trace_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

func TestTraceRoundTrip(t *testing.T) {
	base := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	in := trace.Trace{
		ID:        trace.NewID(base),
		Name:      trace.NameRun,
		SessionID: "session-1",
		Subject:   "commit",
		Producer:  "session",
		Labels:    trace.Labels{"repo": {"nodloop"}},
		Ref:       "c1",
		Time:      base,
		Model:     "sonnet",
		Input:     json.RawMessage(`{"mode":"batch","metrics":["click_count"]}`),
		Output:    json.RawMessage(`{"status":"hold","hold_reasons":["gap in the window"]}`),
		Error:     "timeout",
		Usage: trace.Usage{
			InputTokens: 100, OutputTokens: 20, ThinkingTokens: 7, CacheReadTokens: 5, CacheCreateTokens: 6, CostUSD: 0.01,
		},
		DurationMS: 1500,
		Tags:       []string{"feedback:on"},
	}
	b, err := json.Marshal(in)
	require.NoError(t, err)
	var out trace.Trace
	assert.NoError(t, json.Unmarshal(b, &out))
	assert.Equal(t, in, out)
}

func TestNameValid(t *testing.T) {
	tcs := []struct {
		name string
		args trace.Name
		want bool
	}{
		{"run is known", trace.NameRun, true},
		{"check is known", trace.NameCheck, true},
		{"a name of the data review is unknown now", "diagnose", false},
		{"empty name is unknown", "", false},
		{"misspelled name is unknown", "diagnosis", false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Valid())
		})
	}
}

func TestUsageAdd(t *testing.T) {
	type args struct {
		usage trace.Usage
		other trace.Usage
	}
	full := trace.Usage{
		InputTokens: 100, OutputTokens: 20, ThinkingTokens: 7, CacheReadTokens: 5, CacheCreateTokens: 6, CostUSD: 0.01,
	}
	tcs := []struct {
		name string
		args args
		want trace.Usage
	}{
		{"nothing plus nothing is nothing", args{}, trace.Usage{}},
		{"nothing keeps the other usage", args{other: full}, full},
		{
			"every count and the cost add up",
			args{full, trace.Usage{
				InputTokens: 1, OutputTokens: 2, ThinkingTokens: 1, CacheReadTokens: 3, CacheCreateTokens: 4, CostUSD: 0.02,
			}},
			trace.Usage{
				InputTokens: 101, OutputTokens: 22, ThinkingTokens: 8, CacheReadTokens: 8, CacheCreateTokens: 10, CostUSD: 0.03,
			},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.usage.Add(tc.args.other))
		})
	}
}

func TestUsagePromptTokens(t *testing.T) {
	tcs := []struct {
		name string
		args trace.Usage
		want int
	}{
		{"no usage counts nothing", trace.Usage{}, 0},
		{
			"cached and uncached prompt tokens add up",
			trace.Usage{InputTokens: 100, CacheReadTokens: 5, CacheCreateTokens: 6},
			111,
		},
		{"output tokens are not prompt tokens", trace.Usage{InputTokens: 100, OutputTokens: 20}, 100},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.PromptTokens())
		})
	}
}

func TestFilterMatches(t *testing.T) {
	tr := trace.Trace{
		ID:        "d1",
		Name:      trace.NameRun,
		SessionID: "eval-1",
		Subject:   "tq-001",
		Ref:       "c1",
		Tags:      []string{"feedback:on", "z:3"},
	}
	tcs := []struct {
		name string
		args trace.Filter
		want bool
	}{
		{"empty filter matches all", trace.Filter{}, true},
		{"same id matches", trace.Filter{ID: "d1"}, true},
		{"other id does not match", trace.Filter{ID: "d2"}, false},
		{"same name matches", trace.Filter{Name: trace.NameRun}, true},
		{"other name does not match", trace.Filter{Name: trace.NameCheck}, false},
		{"same session matches", trace.Filter{SessionID: "eval-1"}, true},
		{"other session does not match", trace.Filter{SessionID: "eval-2"}, false},
		{"same subject matches", trace.Filter{Subject: "tq-001"}, true},
		{"other subject does not match", trace.Filter{Subject: "tq-002"}, false},
		{"same ref matches", trace.Filter{Ref: "c1"}, true},
		{"other ref does not match", trace.Filter{Ref: "c2"}, false},
		{"every tag present in any order matches", trace.Filter{Tags: []string{"z:3", "feedback:on"}}, true},
		{"one missing tag does not match", trace.Filter{Tags: []string{"feedback:on", "feedback:off"}}, false},
		{"limit is ignored by matching", trace.Filter{Limit: 1}, true},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Matches(tr))
		})
	}
}

func TestNewID(t *testing.T) {
	tcs := []struct {
		name string
		args time.Time
		want string
	}{
		{"unix epoch gives a zero prefix", time.UnixMilli(0), `^000000000000[0-9a-f]{8}$`},
		{"milliseconds become 12 hex digits", time.UnixMilli(0x19974a1b2c3), `^019974a1b2c3[0-9a-f]{8}$`},
		{
			"sub millisecond precision is dropped",
			time.UnixMilli(0x19974a1b2c3).Add(time.Microsecond),
			`^019974a1b2c3[0-9a-f]{8}$`,
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Regexp(t, tc.want, trace.NewID(tc.args))
		})
	}
}

func TestNewIDDiffersWithinOneMillisecond(t *testing.T) {
	at := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	assert.NotEqual(t, trace.NewID(at), trace.NewID(at))
}

func TestTraceCheckRun(t *testing.T) {
	tcs := []struct {
		name string
		args trace.Trace
		want error
	}{
		{"a run is cited", trace.Trace{ID: "u1", Name: trace.NameRun}, nil},
		{"a review of the data review is refused now", trace.Trace{ID: "d1", Name: "diagnose"}, trace.ErrNotRun},
		{"a failed run is refused", trace.Trace{ID: "u2", Name: trace.NameRun, Error: "cancelled"}, trace.ErrFailedRun},
		{"a check trace is refused", trace.Trace{ID: "c1", Name: trace.NameCheck}, trace.ErrNotRun},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.ErrorIs(t, tc.args.CheckRun(), tc.want)
		})
	}
}

func TestNewRun(t *testing.T) {
	at := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	type args struct {
		producer string
		labels   trace.Labels
		input    string
		output   string
	}
	type want struct {
		labels trace.Labels
		input  string
		output string
		err    error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"JSON output is kept as it is",
			args{producer: "session", output: `{"answer":"use git -C"}`},
			want{input: `{}`, output: `{"answer":"use git -C"}`},
		},
		{
			"text output becomes a JSON string",
			args{producer: "session", output: "use git -C"},
			want{input: `{}`, output: `"use git -C"`},
		},
		{
			"a repeated value counts once",
			args{producer: "session", labels: trace.Labels{"path": {"a.go", "a.go"}}, input: `{"applied":[]}`, output: `1`},
			want{labels: trace.Labels{"path": {"a.go"}}, input: `{"applied":[]}`, output: `1`},
		},
		{
			"two values under one key are refused",
			args{producer: "session", labels: trace.Labels{"path": {"b.go", "a.go"}}, output: "x"},
			want{err: trace.ErrLabelValues},
		},
		{"a run without a producer is refused", args{output: "x"}, want{err: trace.ErrProducerRequired}},
		{"an empty key is refused", args{producer: "session", labels: trace.Labels{"": {"x"}}, output: "x"}, want{err: trace.ErrLabelEmpty}},
		{"a key without values is refused", args{producer: "session", labels: trace.Labels{"repo": nil}, output: "x"}, want{err: trace.ErrLabelEmpty}},
		{"an empty value is refused", args{producer: "session", labels: trace.Labels{"repo": {""}}, output: "x"}, want{err: trace.ErrLabelEmpty}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := trace.NewRun(tc.args.producer, "subject", tc.args.labels, json.RawMessage(tc.args.input), []byte(tc.args.output), at)

			require.ErrorIs(t, err, tc.want.err)
			if tc.want.err != nil {
				return
			}
			assert.Equal(t, trace.NameRun, got.Name)
			assert.Equal(t, tc.args.producer, got.Producer)
			assert.Equal(t, tc.want.labels, got.Labels)
			assert.JSONEq(t, tc.want.input, string(got.Input))
			assert.JSONEq(t, tc.want.output, string(got.Output))
			assert.NoError(t, got.CheckRun())
		})
	}
}

func TestLabelsHas(t *testing.T) {
	labels := trace.Labels{"repo": {"nodloop"}}
	assert.True(t, labels.Has("repo", "nodloop"))
	assert.False(t, labels.Has("repo", "other"))
	assert.False(t, labels.Has("path", "nodloop"))
}

func TestTracesVocabulary(t *testing.T) {
	ts := trace.Traces{
		{Name: trace.NameRun, Producer: "session", Labels: trace.Labels{"repo": {"nodloop"}, "task": {"commit"}}},
		{Name: trace.NameRun, Producer: "session", Labels: trace.Labels{"repo": {"nodloop", "other"}}},
		{Name: trace.NameRun, Producer: "ci", Labels: trace.Labels{"repo": {"ci-only"}}},
		{Name: trace.NameCheck, Producer: "session", Labels: trace.Labels{"repo": {"not-a-run"}}},
	}

	got := ts.Vocabulary("session")

	assert.ElementsMatch(t, []string{"nodloop", "other"}, got["repo"])
	assert.Equal(t, []string{"commit"}, got["task"])
	assert.Empty(t, trace.Traces{}.Vocabulary("session"))
}
