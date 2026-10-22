package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/mcp"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
)

func TestHostOutputJSONSchemas(t *testing.T) {
	tcs := []struct {
		name string
		args [2]string
	}{
		{"compaction schema", [2]string{"compaction", "schema"}},
		{"run output", [2]string{"extraction", "output"}},
		{"edited output", [2]string{"extraction", "edited"}},
		{"extraction schema", [2]string{"extraction", "schema"}},
		{"intervention record", [2]string{"intervene", "intervention"}},
		{"report payload", [2]string{"report", "report"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := testkit.Connect(t, serve(testkit.Open(t), mcp.Caller{}))
			schemas := c.OutputSchemas(t)
			tool, ok := schemas[tc.args[0]].(map[string]any)
			require.True(t, ok)
			props, ok := tool["properties"].(map[string]any)
			require.True(t, ok)
			value, ok := props[tc.args[1]].(map[string]any)
			require.True(t, ok, "nested schemas must remain objects for strict consumers")
			encoded, err := json.Marshal(value)
			require.NoError(t, err)
			assert.JSONEq(t, `{"type":["null","boolean","object","array","number","string"]}`, string(encoded))
		})
	}
}

// Without a record dir every tool answers the open error
func TestHostOpenFails(t *testing.T) {
	t.Parallel()
	reason := errors.New("home directory unknown: set --record-dir or NODLOOP_RECORD_DIR")
	c := testkit.Connect(t, mcp.NewHost(func(context.Context) (*mcp.Server, error) { return nil, reason }, "test", "").ServeTransport)
	// The smallest input each schema accepts so the call reaches the tool
	inputs := map[string]map[string]any{
		"run": {"producer": "session", "output": "x"}, "knowledge_for": {"producer": "session"},
		"feedback": {"trace_id": "t", "verdict": "approve"}, "outcome": {"trace_id": "t", "result": "confirmed"},
		"propose": {"kind": "meaning", "content": "c"}, "approve": {"id": "k", "version": 1, "approver": "sam"},
		"compaction": {"id": "k"}, "propose_compaction": {"anchor": "k", "items": []any{}},
		"check_compaction":   {"compaction": "c", "items": []any{}},
		"approve_compaction": {"compaction": "c", "approver": "sam"},
		"extraction":         {"from": "t"},
		"propose_extraction": {"from": "t", "relation": "add", "kind": "judgment", "content": "x", "critique": map[string]any{"states": true, "holds": true, "fits": true, "why": "ok"}},
		"queue":              {}, "knowledge_health": {}, "reaffirm": {"id": "k", "version": 1, "approver": "sam"},
		"check_call": {"tool": "Bash", "input": map[string]any{"command": "ls"}},
		"report":     {"name": "loop"},
		"records":    {"kind": "run"},
		"intervene":  {"action": "digest"},
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
		return newServer(st, session, mcp.Policy{}), nil
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

// Every listed tool carries an outputSchema of an object
// The SDK checks every answer against it so each handler test also checks its answers
func TestHostOutputSchemas(t *testing.T) {
	objects := map[string]any{}
	for _, name := range mcp.Tools() {
		objects[name] = "object"
	}
	tcs := []struct {
		name string
		// The protocol version of the client and the latest when empty
		args string
		want map[string]any
	}{
		{"the latest protocol lists an object schema for every tool", "", objects},
		{"the first protocol with output schemas lists the same", "2025-06-18", objects},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := testkit.ConnectWith(t, serve(testkit.Open(t), mcp.Caller{}), nil, tc.args)

			got := schemaTypes(c.OutputSchemas(t))

			assert.Equal(t, tc.want, got)
		})
	}
}

// The type of each schema by tool name and nil for a schema that is no object
func schemaTypes(schemas map[string]any) map[string]any {
	out := make(map[string]any, len(schemas))
	for name, schema := range schemas {
		m, _ := schema.(map[string]any)
		out[name] = m["type"]
	}
	return out
}

// The sorted keys of a map and nil for anything else or an empty map
func keys(v any) []string {
	m, _ := v.(map[string]any)
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// The text of the error and empty for none
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
