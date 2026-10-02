package mcp_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/mcp"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
)

// Without a record dir every tool answers the open error
func TestHostOpenFails(t *testing.T) {
	t.Parallel()
	reason := errors.New("home directory unknown: set --record-dir or NODLOOP_RECORD_DIR")
	c := testkit.Connect(t, mcp.NewHost(func(context.Context) (*mcp.Server, error) { return nil, reason }, "test", "").ServeTransport)
	// The smallest input each schema accepts so the call reaches the tool
	inputs := map[string]map[string]any{
		"run": {"producer": "session", "output": "x"}, "knowledge_for": {"producer": "session"},
		"feedback": {"trace_id": "t", "verdict": "approve"}, "outcome": {"trace_id": "t", "result": "confirmed"},
		"propose": {"kind": "meaning", "content": "c"}, "approve": {"id": "k", "version": 1, "approver": "jed"},
		"compaction": {"id": "k"}, "propose_compaction": {"anchor": "k", "items": []any{}},
		"check_compaction":   {"compaction": "c", "items": []any{}},
		"approve_compaction": {"compaction": "c", "approver": "jed"},
		"extraction":         {"from": "t"},
		"propose_extraction": {"from": "t", "relation": "add", "kind": "judgment", "content": "x", "critique": map[string]any{"states": true, "holds": true, "fits": true, "why": "ok"}},
		"queue":              {}, "knowledge_health": {}, "reaffirm": {"id": "k", "version": 1, "approver": "jed"},
	}
	tools := c.Tools(t)
	require.ElementsMatch(t, mcp.Tools(), tools)
	require.Len(t, tools, len(inputs))
	for _, tool := range tools {
		err := c.Run(t, tool, inputs[tool])
		assert.ErrorContains(t, err, reason.Error(), tool)
	}
}

// Every call opens the server anew and one lock serializes the calls so one candidate is approved once
func TestHostSerializesCalls(t *testing.T) {
	t.Parallel()
	st := testkit.Open(t)
	session := mcp.NewSession(st.Clock.Now())
	opened := 0
	open := func(context.Context) (*mcp.Server, error) {
		opened++
		return newServer(st, session), nil
	}
	c := testkit.Connect(t, mcp.NewHost(open, "test", "").ServeTransport)
	runID := recordRun(t, c, "nodloop")
	require.NoError(t, c.Run(t, "propose", map[string]any{
		"id": "k1", "kind": "judgment", "content": "use git -C", "trace_ids": []any{runID},
		"producer": "session", "labels": map[string]any{"repo": []any{"nodloop"}},
	}))
	const calls = 8
	errs := make([]error, calls)
	var wg sync.WaitGroup
	for i := range calls {
		wg.Go(func() {
			errs[i] = c.Run(t, "approve", map[string]any{"id": "k1", "version": 1, "approver": "ann"})
		})
	}

	wg.Wait()

	refused := 0
	for _, err := range errs {
		if err != nil {
			assert.ErrorContains(t, err, knowledge.ErrTransitionInvalid.Error())
			refused++
		}
	}
	assert.Equal(t, calls-1, refused)
	assert.Equal(t, calls+2, opened)
}

// The instructions given at construction reach the client in the handshake before any tool call
func TestHostInstructions(t *testing.T) {
	tcs := []struct {
		name string
		args string
		want string
	}{
		{"none sends none", "", ""},
		{"a sentence is sent as is", "nodloop dev while the plugin runs version 0.5.15", "nodloop dev while the plugin runs version 0.5.15"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reason := errors.New("not set up")
			c := testkit.Connect(t, mcp.NewHost(func(context.Context) (*mcp.Server, error) { return nil, reason }, "test", tc.args).ServeTransport)

			assert.Equal(t, tc.want, c.Instructions())
		})
	}
}
