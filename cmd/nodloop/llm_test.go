package main

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/jeon-jihyeon/nodloop/internal/llm"
	"github.com/jeon-jihyeon/nodloop/internal/llm/llmmock"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
)

func TestRunLLM(t *testing.T) {
	type args struct {
		args []string
		// The model the request carries
		model string
		res   llm.Response
		err   error
	}
	type want struct {
		code   int
		stdout string
		stderr string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"success prints the cost and both durations",
			args{
				[]string{"probe", "--model", "sonnet"}, "sonnet",
				llm.Response{
					Output: json.RawMessage(`{"answer":42}`), CostUSD: 0.03, InputTokens: 5, Duration: 800 * time.Millisecond,
				},
				nil,
			},
			want{
				0, "output {\"answer\":42}\ncost $0.0300\ntokens in 5 out 0 cache_read 0 cache_create 0\n" +
					"duration 800ms (wall 1s)\n", "",
			},
		},
		{
			"no model flag asks haiku",
			args{[]string{"probe"}, "haiku", llm.Response{Output: json.RawMessage(`{"answer":42}`)}, nil},
			want{
				0, "output {\"answer\":42}\ncost $0.0000\ntokens in 0 out 0 cache_read 0 cache_create 0\n" +
					"duration 0s (wall 1s)\n", "",
			},
		},
		{
			"wrong answer fails",
			args{[]string{"probe"}, "haiku", llm.Response{Output: json.RawMessage(`{"answer":7}`)}, nil},
			want{1, "", "nodloop llm probe: wrong answer: 7 in {\"answer\":7}\n"},
		},
		{
			"undecodable output names the decode error",
			args{[]string{"probe"}, "haiku", llm.Response{Output: json.RawMessage(`{"answer":"x"}`)}, nil},
			want{1, "", "nodloop llm probe: unexpected output: {\"answer\":\"x\"}: " +
				"json: cannot unmarshal string into Go struct field .answer of type int\n"},
		},
		{
			"call failure fails",
			args{[]string{"probe"}, "haiku", llm.Response{}, assert.AnError},
			want{1, "", "nodloop llm probe: " + assert.AnError.Error() + "\n"},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := llmmock.NewMockClient(gomock.NewController(t))
			req := llm.Request{
				Prompt: "Return the number 42 as the field answer.", Schema: json.RawMessage(probeSchema), Model: tc.args.model,
			}
			client.EXPECT().Complete(gomock.Any(), req).Return(tc.args.res, tc.args.err)
			var stdout, stderr bytes.Buffer

			got := runLLM(tc.args.args, client, testkit.Open(t).Clock.Now, &stdout, &stderr)

			assert.Equal(t, tc.want.code, got)
			assert.Equal(t, tc.want.stdout, stdout.String())
			assert.Equal(t, tc.want.stderr, stderr.String())
		})
	}
}
