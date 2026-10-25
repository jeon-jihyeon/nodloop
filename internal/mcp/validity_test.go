package mcp_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

func TestServerKnowledgeLabels(t *testing.T) {
	tcs := []struct {
		name string
		args map[string][]string
		want string
	}{
		{"empty labels", map[string][]string{}, ""},
		{"repeated value", map[string][]string{"context": {"x", "x"}}, ""},
		{"different alternatives", map[string][]string{"context": {"x", "y"}}, trace.ErrLabelValues.Error()},
		{"empty value", map[string][]string{"context": {""}}, trace.ErrLabelEmpty.Error()},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := connect(t, testkit.Open(t))
			err := c.Run(t, "knowledge_for", map[string]any{"producer": "session", "labels": tc.args})
			assert.Equal(t, tc.want == "", err == nil)
			assert.Contains(t, fmt.Sprint(err), tc.want)
		})
	}
}

func TestServerValidity(t *testing.T) {
	tcs := []struct {
		name    string
		advance time.Duration
		want    int
	}{
		{"before the explicit start", 0, 0},
		{"during its validity", time.Hour, 1},
		{"after its explicit end", 3 * time.Hour, 0},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := testkit.Open(t)
			c := connect(t, st)
			at := st.Clock.Now()
			run := recordRun(t, c, "acme")
			require.NoError(t, c.Run(t, "feedback", map[string]any{
				"trace_id": run, "verdict": "reject", "reason": "Keep the temporary condition",
			}))
			require.NoError(t, c.Run(t, "propose", map[string]any{
				"id": "acme", "kind": "judgment", "content": "Use the temporary presentation", "from": run,
				"valid_from":  at.Add(time.Hour).Format(time.RFC3339Nano),
				"valid_until": at.Add(2 * time.Hour).Format(time.RFC3339Nano),
			}))
			require.NoError(t, c.Run(t, "approve", map[string]any{"id": "acme", "version": 1, "approver": "ann"}))
			st.Clock.Advance(tc.advance)
			var got struct {
				Items []struct {
					Scope knowledge.RunScope `json:"scope"`
				} `json:"items"`
			}
			require.NoError(t, c.Call(t, "knowledge_for", map[string]any{
				"producer": "session", "labels": map[string]any{"repo": []string{"acme"}},
			}, &got))
			assert.Len(t, got.Items, tc.want)
		})
	}
}
