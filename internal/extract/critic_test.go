package extract_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestClaudeCriticTriage(t *testing.T) {
	valid := `{"nature":"preference","mixed":false,"reversibility":"H","protected":"none","lasting":true,"faithful":true,"conflict":false}`
	want := &classify.Triage{Nature: "preference", Reversibility: "H", Protected: "none", Lasting: true, Faithful: true}
	tcs := []struct {
		name string
		args string
		want *classify.Triage
	}{
		{"complete metadata", `,"triage":` + valid, want},
		{"legacy response", "", nil},
		{"null metadata", `,"triage":null`, nil},
		{"partial metadata", `,"triage":{"nature":"preference"}`, nil},
		{"wrong metadata type", `,"triage":"private text"`, nil},
		{"wrong flag type", `,"triage":{"nature":"preference","mixed":"false","reversibility":"H","protected":"none","lasting":true,"faithful":true,"conflict":false}`, nil},
		{"unrecognized nature", `,"triage":{"nature":"private text","mixed":false,"reversibility":"H","protected":"none","lasting":true,"faithful":true,"conflict":false}`, nil},
		{"unrecognized reversibility", `,"triage":{"nature":"preference","mixed":false,"reversibility":"private text","protected":"none","lasting":true,"faithful":true,"conflict":false}`, nil},
		{"unrecognized protection", `,"triage":{"nature":"preference","mixed":false,"reversibility":"H","protected":"private text","lasting":true,"faithful":true,"conflict":false}`, nil},
		{"model provenance is ignored", `,"triage":{"policy":"private text","source":"complete","classify":"claimed",` + valid[1:], want},
		{"uncertain metadata remains conservative", `,"triage":{"nature":"unknown","mixed":true,"reversibility":"unknown","protected":"unknown","lasting":false,"faithful":false,"conflict":true}`,
			&classify.Triage{Nature: "unknown", Mixed: true, Reversibility: "unknown", Protected: "unknown", Conflict: true}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := llmmock.NewMockClient(gomock.NewController(t))
			client.EXPECT().Complete(gomock.Any(), gomock.Any()).Times(1).DoAndReturn(func(_ context.Context, req llm.Request) (llm.Response, error) {
				assert.Equal(t, "the existing draft state", req.Prompt)
				assert.Equal(t, extract.CriticRules, req.System)
				assert.JSONEq(t, extract.CriticSchema, string(req.Schema))
				return llm.Response{Output: json.RawMessage(`{"states":true,"holds":true,"fits":true,"why":"ok"` + tc.args + `}`)}, nil
			})

			got, err := extract.NewClaudeCritic(client, "").Reply(context.Background(), classify.Request{State: "the existing draft state"})

			require.NoError(t, err)
			assert.Equal(t, tc.want, got.Triage)
			assert.Len(t, got.Answers, 3)
			assert.True(t, got.Answers.Sure())
		})
	}
}
