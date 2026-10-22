package mcp_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/testkit"
)

func TestServerExtractionJSONValues(t *testing.T) {
	type args struct {
		output, edited any
		verdict        string
	}
	type want struct{ output, edited string }
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"string", args{"answer", "edited", "edit"}, want{`"answer"`, `"edited"`}},
		{"number", args{42, 43, "edit"}, want{`42`, `43`}},
		{"boolean", args{true, false, "edit"}, want{`true`, `false`}},
		{"null", args{nil, nil, "reject"}, want{`null`, ""}},
		{"array", args{[]any{"one", 2}, []any{"two"}, "edit"}, want{`["one",2]`, `["two"]`}},
		{"object", args{map[string]any{"answer": "one"}, map[string]any{"answer": "two"}, "edit"},
			want{`{"answer":"one"}`, `{"answer":"two"}`}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := connect(t, testkit.Open(t))
			var run struct {
				TraceID string `json:"trace_id"`
			}
			require.NoError(t, c.Call(t, "run", map[string]any{"producer": "acme", "output": tc.args.output}, &run))
			require.NoError(t, c.Run(t, "feedback", map[string]any{
				"trace_id": run.TraceID, "verdict": tc.args.verdict, "reviewer": "ann", "reason": "example correction",
				"edited_output": tc.args.edited,
			}))
			var got struct{ Output, Edited json.RawMessage }
			require.NoError(t, c.Call(t, "extraction", map[string]any{"from": run.TraceID}, &got))
			assert.Equal(t, tc.want, want{string(got.Output), string(got.Edited)})
		})
	}
}
