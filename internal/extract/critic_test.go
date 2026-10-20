package extract_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/jeon-jihyeon/nodloop/internal/classify"
	"github.com/jeon-jihyeon/nodloop/internal/extract"
	"github.com/jeon-jihyeon/nodloop/internal/llm"
	"github.com/jeon-jihyeon/nodloop/internal/llm/llmmock"
)

// The claude critic names the model the backend answered with and no effort since it pins none
func TestClaudeCriticReply(t *testing.T) {
	critique := `{"states":true,"holds":false,"fits":true,"why":"only this run"}`
	answers := classify.Answers{
		"states": {Yes: 1, Reason: "only this run"}, "holds": {Yes: 0, Reason: "only this run"}, "fits": {Yes: 1, Reason: "only this run"},
	}
	type args struct {
		model string
		res   llm.Response
		err   error
	}
	type want struct {
		reply classify.Reply
		err   error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"the model the backend answered with", args{"haiku", llm.Response{Output: json.RawMessage(critique), Model: "claude-haiku-4-5"}, nil},
			want{reply: classify.Reply{Answers: answers, Model: "claude-haiku-4-5"}}},
		{"the model asked when the backend names none", args{"haiku", llm.Response{Output: json.RawMessage(critique)}, nil},
			want{reply: classify.Reply{Answers: answers, Model: "haiku"}}},
		{"a failed call keeps the model asked", args{"haiku", llm.Response{}, assert.AnError},
			want{reply: classify.Reply{Model: "haiku"}, err: assert.AnError}},
		{"an answer that does not decode keeps the model asked", args{"haiku", llm.Response{Output: json.RawMessage(`[]`), Model: "claude-haiku-4-5"}, nil},
			want{reply: classify.Reply{Model: "haiku"}, err: extract.ErrDraftInvalid}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := llmmock.NewMockClient(gomock.NewController(t))
			client.EXPECT().Complete(gomock.Any(), gomock.Any()).Return(tc.args.res, tc.args.err)

			got, err := extract.NewClaudeCritic(client, tc.args.model).Reply(context.Background(), classify.Request{State: "the draft"})

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.reply, got)
		})
	}
}
