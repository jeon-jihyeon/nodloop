package trace_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/schema"
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
		Input:     json.RawMessage(`{"mode":"batch","metrics":["row_count"]}`),
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
		{"consolidate is known", trace.NameConsolidate, true},
		{"refill is known", trace.NameRefill, true},
		{"turn is known", trace.NameTurn, true},
		{"compact is known", trace.NameCompact, true},
		{"intervention is known", trace.NameIntervention, true},
		{"instructions is known", trace.NameInstructions, true},
		{"capture is known", trace.NameCapture, true},
		{"decide is known", trace.NameDecide, true},
		{"knob is known", trace.NameKnob, true},
		{"anchor is known", trace.NameAnchor, true},
		{"expose is known", trace.NameExpose, true},
		{"answer is known", trace.NameAnswer, true},
		{"rollup is known", trace.NameRollup, true},
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

func TestTraceTook(t *testing.T) {
	type want struct {
		ms  int64
		err error
	}
	tcs := []struct {
		name string
		args time.Duration
		want want
	}{
		{"a duration is kept in milliseconds", 1500 * time.Millisecond, want{1500, nil}},
		{"zero leaves no duration", 0, want{0, nil}},
		{"a negative duration is refused", -time.Second, want{0, trace.ErrDurationNegative}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := trace.Trace{ID: "r1"}.Took(tc.args)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.ms, got.DurationMS)
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

func TestIDTime(t *testing.T) {
	at := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	type want struct {
		at time.Time
		ok bool
	}
	tcs := []struct {
		name string
		args string
		want want
	}{
		{"an id of NewID gives its millisecond", trace.NewID(at.Add(time.Microsecond)), want{at, true}},
		{"a short id has no time", "r1", want{}},
		{"an id that is not hex has no time", "run-of-acme-1", want{}},
		{"a hex prefix with another suffix has no time", "0199d1c2a400acme-run", want{}},
		{"a signed prefix has no time", "-00000000001abcd1234", want{}},
		{"a hex id of another length has no time", "0199d1c2a400abcd", want{}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := trace.IDTime(tc.args)
			assert.Equal(t, tc.want.ok, ok)
			assert.True(t, tc.want.at.Equal(got), got)
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

// A run of the input every writer builds validates against the published schema of run
func TestNewRunValidates(t *testing.T) {
	got, err := trace.NewRun("session", "commit", trace.Labels{"repo": {"acme"}}, json.RawMessage(`{"applied":[{"id":"git-c","version":1}],"plugin":"0.9.0"}`),
		[]byte("use git -C"), time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC))
	require.NoError(t, err)

	err = schema.Validate(string(trace.NameRun), got.Record())

	assert.NoError(t, err)
}

func TestLabelsHas(t *testing.T) {
	labels := trace.Labels{"repo": {"nodloop"}}
	assert.True(t, labels.Has("repo", "nodloop"))
	assert.False(t, labels.Has("repo", "other"))
	assert.False(t, labels.Has("path", "nodloop"))
}

func TestLabelsPlace(t *testing.T) {
	tcs := []struct {
		name string
		args trace.Labels
		want trace.Labels
	}{
		{"the host is left out", trace.Labels{"repo": {"o"}, "dir": {"."}, "host": {"codex"}}, trace.Labels{"repo": {"o"}, "dir": {"."}}},
		{"labels without a host stay", trace.Labels{"repo": {"o"}}, trace.Labels{"repo": {"o"}}},
		{"the host alone leaves none", trace.Labels{"host": {"cursor"}}, trace.Labels{}},
		{"nil stays nil", nil, nil},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			had := len(tc.args)

			got := tc.args.Place()

			assert.Equal(t, tc.want, got)
			assert.Len(t, tc.args, had, "the labels of the run keep their host")
		})
	}
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
