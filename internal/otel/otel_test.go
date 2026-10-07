package otel_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/otel"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// The requests a collector received
type collector struct {
	mu      sync.Mutex
	bodies  []map[string]any
	headers []http.Header
	status  int
}

func (c *collector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var decoded map[string]any
	_ = json.Unmarshal(body, &decoded)
	c.mu.Lock()
	c.bodies = append(c.bodies, decoded)
	c.headers = append(c.headers, r.Header.Clone())
	c.mu.Unlock()
	w.WriteHeader(c.status)
	_, _ = w.Write([]byte(`{"partialSuccess":{}}`))
}

// The spans of every request in order
func (c *collector) spans() []map[string]any {
	var out []map[string]any
	for _, b := range c.bodies {
		for _, rs := range b["resourceSpans"].([]any) {
			for _, ss := range rs.(map[string]any)["scopeSpans"].([]any) {
				for _, s := range ss.(map[string]any)["spans"].([]any) {
					out = append(out, s.(map[string]any))
				}
			}
		}
	}
	return out
}

// The attributes of a span or an event by key with their plain values
func attrs(m map[string]any) map[string]any {
	out := map[string]any{}
	for _, a := range m["attributes"].([]any) {
		kv := a.(map[string]any)
		for _, v := range kv["value"].(map[string]any) {
			out[kv["key"].(string)] = v
		}
	}
	return out
}

func TestExporterExport(t *testing.T) {
	at := time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)
	run := trace.Trace{
		ID: "01a1", Name: trace.NameRun, Producer: "support-bot", SessionID: "s1", Time: at,
		Labels: trace.Labels{"tenant": {"acme"}}, Output: json.RawMessage(`"Here are the refund steps"`),
	}
	verdicts := []feedback.Feedback{
		{TraceID: "01a1", Time: at.Add(time.Minute), Verdict: feedback.VerdictReject, ReasonCode: feedback.ReasonScope, Reason: "the window was missing", Reviewer: "ann"},
		{TraceID: "other", Time: at, Verdict: feedback.VerdictApprove, Reviewer: "ann"},
	}
	c := &collector{status: http.StatusOK}
	srv := httptest.NewServer(c)
	t.Cleanup(srv.Close)
	e := otel.New(srv.URL+"/v1/traces", map[string]string{"Authorization": "Basic x"}, "0.6.7", srv.Client())

	require.NoError(t, e.Export(context.Background(), trace.Traces{run}, verdicts, true))
	require.NoError(t, e.Export(context.Background(), trace.Traces{run}, nil, false))

	require.Len(t, c.bodies, 2)
	assert.Equal(t, "Basic x", c.headers[0].Get("Authorization"))
	assert.Equal(t, "application/json", c.headers[0].Get("Content-Type"))
	spans := c.spans()
	first, again := spans[0], spans[1]
	assert.Equal(t, "invoke_agent support-bot", first["name"])
	assert.Equal(t, first["traceId"], again["traceId"], "a run sent again keeps its ids")
	assert.Equal(t, first["spanId"], again["spanId"])
	assert.Len(t, first["traceId"], 32)
	assert.Equal(t, "1791363600000000000", first["startTimeUnixNano"])
	assert.Equal(t, map[string]any{
		"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": "support-bot", "nodloop.run.id": "01a1", "session.id": "s1",
		"nodloop.label.tenant": map[string]any{"values": []any{map[string]any{"stringValue": "acme"}}},
		"output.value":         "Here are the refund steps",
	}, attrs(first))
	assert.NotContains(t, attrs(again), "output.value", "the output goes along only on request")
	events := first["events"].([]any)
	require.Len(t, events, 1, "a verdict on another run stays off the span")
	event := events[0].(map[string]any)
	assert.Equal(t, "gen_ai.evaluation.result", event["name"])
	assert.Equal(t, map[string]any{
		"gen_ai.evaluation.name": "nodloop.verdict", "gen_ai.evaluation.score.label": "reject", "gen_ai.evaluation.score.value": 0.0,
		"gen_ai.evaluation.explanation": "the window was missing", "nodloop.reason_code": "scope", "nodloop.reviewer": "ann",
	}, attrs(event))
}

func TestExporterExportBatchesAndFails(t *testing.T) {
	runs := make(trace.Traces, 0, 450)
	for i := range 450 {
		runs = append(runs, trace.Trace{ID: string(rune('a' + i%26)), Name: trace.NameRun, Producer: "bot"})
	}
	type want struct {
		requests int
		err      error
	}
	tcs := []struct {
		name string
		// The status the collector answers
		args int
		want want
	}{
		{"runs go out in batches of 200", http.StatusOK, want{3, nil}},
		{"a refusal stops the export with the status", http.StatusUnauthorized, want{1, otel.ErrStatus}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := &collector{status: tc.args}
			srv := httptest.NewServer(c)
			t.Cleanup(srv.Close)

			err := otel.New(srv.URL, nil, "", srv.Client()).Export(context.Background(), runs, nil, false)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Len(t, c.bodies, tc.want.requests)
		})
	}
}
