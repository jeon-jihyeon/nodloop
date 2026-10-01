package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/diagnose"
	"github.com/jeon-jihyeon/nodloop/internal/mcp"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// Without a config every tool answers the open error
func TestHostOpenFails(t *testing.T) {
	t.Parallel()
	reason := errors.New("NODLOOP_FILE_DIR is not set. Run nodloop setup --data-dir <dir>")
	c := testkit.Connect(t, mcp.NewHost(func(context.Context) (*mcp.Server, error) { return nil, reason }, "test").ServeTransport)
	// The smallest input each schema accepts so the call reaches the tool
	inputs := map[string]map[string]any{
		"knowledge_health": {},
		"reaffirm":         {"id": "k", "version": 1, "approver": "jed"},
		"queue":            {},
		"events":           {}, "pending": {}, "observe": {"event_id": "e"}, "context": {"event_id": "e"}, "detail": {"event_id": "e"},
		"select": {"pending_id": "p", "knowledge": []any{}, "examples": []any{}},
		"record": {"pending_id": "p", "diagnosis": map[string]any{
			"status": "hold", "observations": []any{}, "causes": []any{}, "checks": []any{}, "open_questions": []any{},
		}},
		"feedback": {"trace_id": "t", "verdict": "approve"}, "outcome": {"trace_id": "t", "result": "confirmed"},
		"propose": {"kind": "meaning", "content": "c"}, "approve": {"id": "k", "version": 1, "approver": "jed"},
		"compaction": {"id": "k"}, "propose_compaction": {"anchor": "k", "items": []any{}},
		"approve_compaction": {"compaction": "c", "approver": "jed"},
	}
	tools := c.Tools(t)
	require.ElementsMatch(t, mcp.Tools(), tools)
	require.Len(t, tools, len(inputs))
	for _, tool := range tools {
		err := c.Run(t, tool, inputs[tool])
		assert.ErrorContains(t, err, reason.Error(), tool)
	}
}

// A Diagnoser per call guards nothing on its own so the host lock must keep one record per pending id
func TestHostSerializesCalls(t *testing.T) {
	t.Parallel()
	st := testkit.Open(t)
	session := mcp.NewSession(st.Clock.Now())
	opened := 0
	open := func(context.Context) (*mcp.Server, error) {
		opened++
		return newServer(t, st, session, "nodloop", ""), nil
	}
	c := testkit.Connect(t, mcp.NewHost(open, "test").ServeTransport)
	b, err := os.ReadFile(reviewFile)
	require.NoError(t, err)
	var ctxAnswer struct {
		PendingID string `json:"pending_id"`
	}
	require.NoError(t, c.Call(t, "context", map[string]any{"event_id": spikeEvent}, &ctxAnswer))
	const calls = 8
	errs := make([]error, calls)
	var wg sync.WaitGroup
	for i := range calls {
		wg.Go(func() {
			errs[i] = c.Run(t, "record", map[string]any{"pending_id": ctxAnswer.PendingID, "diagnosis": json.RawMessage(b)})
		})
	}

	wg.Wait()

	refused := 0
	for _, err := range errs {
		if err != nil {
			assert.ErrorContains(t, err, diagnose.ErrRecorded.Error())
			refused++
		}
	}
	assert.Equal(t, calls-1, refused)
	got, err := st.Traces.List(t.Context(), trace.Filter{Name: trace.NameDiagnose, Ref: ctxAnswer.PendingID})
	require.NoError(t, err)
	assert.Len(t, got, 1)
	assert.Equal(t, calls+1, opened)
}
