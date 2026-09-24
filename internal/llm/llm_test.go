package llm_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jeon-jihyeon/nodloop/internal/llm"
)

func TestResultErrorError(t *testing.T) {
	t.Parallel()
	tcs := []struct {
		name string
		args llm.ResultError
		want string
	}{
		{
			name: "subtype and errors are joined into the message",
			args: llm.ResultError{
				Subtype: "error_max_turns", Errors: []string{"Reached maximum number of turns (3)", "retry later"}, CostUSD: 0.02,
			},
			want: "claude result error_max_turns: Reached maximum number of turns (3); retry later",
		},
		{
			name: "subtype alone when claude reports no errors",
			args: llm.ResultError{Subtype: "error_max_budget_usd"},
			want: "claude result error_max_budget_usd",
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Error())
		})
	}
}
