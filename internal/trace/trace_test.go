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
		ID:         trace.NewID(base),
		Name:       trace.NameDiagnose,
		SessionID:  "eval-1",
		Subject:    "tq-001",
		Ref:        "c1",
		Time:       base,
		Model:      "sonnet",
		Input:      json.RawMessage(`{"mode":"batch","metrics":["click_count"]}`),
		Output:     json.RawMessage(`{"status":"hold","hold_reasons":["gap in the window"]}`),
		Error:      "timeout",
		Usage:      trace.Usage{InputTokens: 100, OutputTokens: 20, CacheReadTokens: 5, CacheCreateTokens: 6, CostUSD: 0.01},
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
		{"context is known", trace.NameContext, true},
		{"select is known", trace.NameSelect, true},
		{"diagnose is known", trace.NameDiagnose, true},
		{"revise is known", trace.NameRevise, true},
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

func TestTraceCheckReview(t *testing.T) {
	tcs := []struct {
		name string
		args trace.Trace
		want error
	}{
		{"diagnose trace is a review", trace.Trace{ID: "d1", Name: trace.NameDiagnose}, nil},
		{"revise trace is not a review", trace.Trace{ID: "r1", Name: trace.NameRevise}, trace.ErrNotReview},
		{"context trace is not a review", trace.Trace{ID: "c1", Name: trace.NameContext}, trace.ErrNotReview},
		{"select trace is not a review", trace.Trace{ID: "s1", Name: trace.NameSelect}, trace.ErrNotReview},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.ErrorIs(t, tc.args.CheckReview(), tc.want)
		})
	}
}

func TestUsageAdd(t *testing.T) {
	type args struct {
		usage trace.Usage
		other trace.Usage
	}
	full := trace.Usage{InputTokens: 100, OutputTokens: 20, CacheReadTokens: 5, CacheCreateTokens: 6, CostUSD: 0.01}
	tcs := []struct {
		name string
		args args
		want trace.Usage
	}{
		{"nothing plus nothing is nothing", args{}, trace.Usage{}},
		{"nothing keeps the other usage", args{other: full}, full},
		{
			"every count and the cost add up",
			args{full, trace.Usage{InputTokens: 1, OutputTokens: 2, CacheReadTokens: 3, CacheCreateTokens: 4, CostUSD: 0.02}},
			trace.Usage{InputTokens: 101, OutputTokens: 22, CacheReadTokens: 8, CacheCreateTokens: 10, CostUSD: 0.03},
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
		Name:      trace.NameDiagnose,
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
		{"same name matches", trace.Filter{Name: trace.NameDiagnose}, true},
		{"other name does not match", trace.Filter{Name: trace.NameContext}, false},
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

func TestTracesPending(t *testing.T) {
	open := trace.Trace{ID: "c1", Name: trace.NameContext}
	closed := trace.Trace{ID: "c2", Name: trace.NameContext}
	review := trace.Trace{ID: "d1", Name: trace.NameDiagnose, Ref: "c2"}
	selection := trace.Trace{ID: "s1", Name: trace.NameSelect, Ref: "c1"}
	unlinked := trace.Trace{ID: "d2", Name: trace.NameDiagnose}
	later := trace.Trace{ID: "c3", Name: trace.NameContext}
	tcs := []struct {
		name string
		args trace.Traces
		want trace.Traces
	}{
		{"no traces have nothing pending", nil, nil},
		{"context without a diagnose is pending", trace.Traces{open}, trace.Traces{open}},
		{"diagnose referring to a context closes it", trace.Traces{review, closed, open}, trace.Traces{open}},
		{"select referring to a context leaves it pending", trace.Traces{selection, open}, trace.Traces{open}},
		{"diagnose without a ref closes nothing", trace.Traces{unlinked, open}, trace.Traces{open}},
		{"pending contexts keep the input order", trace.Traces{later, open}, trace.Traces{later, open}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Pending())
		})
	}
}
