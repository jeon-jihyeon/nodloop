package llm_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/jeon-jihyeon/nodloop/internal/llm"
	"github.com/jeon-jihyeon/nodloop/internal/llm/llmmock"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

func TestMeterTake(t *testing.T) {
	t.Parallel()
	type call struct {
		res llm.Response
		err error
	}
	first := llm.Response{
		Output: json.RawMessage(`{"a":1}`), CostUSD: 0.01, InputTokens: 1, OutputTokens: 2, ThinkingTokens: 1, CacheRead: 3, CacheCreate: 4,
		Duration: time.Second,
	}
	second := llm.Response{Output: json.RawMessage(`{"a":2}`), CostUSD: 0.02, InputTokens: 10, OutputTokens: 20, Duration: 2 * time.Second}
	tcs := []struct {
		name string
		args []call
		want llm.Response
	}{
		{"nothing called takes nothing", nil, llm.Response{}},
		{"every call is summed without an output", []call{{first, nil}, {second, nil}},
			llm.Response{CostUSD: 0.03, InputTokens: 11, OutputTokens: 22, ThinkingTokens: 1, CacheRead: 3, CacheCreate: 4, Duration: 3 * time.Second}},
		{"a failed result adds the cost it was charged", []call{{first, nil}, {llm.Response{}, &llm.ResultError{Subtype: "error_max_turns", CostUSD: 0.05}}},
			llm.Response{CostUSD: 0.06, InputTokens: 1, OutputTokens: 2, ThinkingTokens: 1, CacheRead: 3, CacheCreate: 4, Duration: time.Second}},
		{"another failure adds nothing", []call{{llm.Response{}, assert.AnError}}, llm.Response{}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := llmmock.NewMockClient(gomock.NewController(t))
			for _, c := range tc.args {
				client.EXPECT().Complete(gomock.Any(), gomock.Any()).Return(c.res, c.err)
			}
			m := llm.NewMeter(client)
			var passed []call
			for range tc.args {
				res, err := m.Complete(context.Background(), llm.Request{Schema: json.RawMessage(`{}`)})
				passed = append(passed, call{res, err})
			}

			got := m.Take()
			cost := got.CostUSD
			got.CostUSD = tc.want.CostUSD

			assert.Equal(t, tc.args, passed, "every call passes on as the client answered")
			assert.InDelta(t, tc.want.CostUSD, cost, 1e-9)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, llm.Response{}, m.Take(), "the next sum starts at zero")
		})
	}
}

func TestResponseUsage(t *testing.T) {
	t.Parallel()
	tcs := []struct {
		name string
		args llm.Response
		want trace.Usage
	}{
		{"no usage records none", llm.Response{}, trace.Usage{}},
		{
			"every count goes along without the output and the duration",
			llm.Response{
				Output: json.RawMessage(`{"a":1}`), CostUSD: 0.01, InputTokens: 1, OutputTokens: 2, ThinkingTokens: 3, CacheRead: 4, CacheCreate: 5,
				Duration: time.Second,
			},
			trace.Usage{InputTokens: 1, OutputTokens: 2, ThinkingTokens: 3, CacheReadTokens: 4, CacheCreateTokens: 5, CostUSD: 0.01},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Usage())
		})
	}
}
