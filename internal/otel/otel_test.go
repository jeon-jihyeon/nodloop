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

	"github.com/jeon-jihyeon/nodloop/internal/otel"
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
	judged := otel.Run{
		ID: "01a1", Producer: "support-bot", SessionID: "s1", Time: at,
		Labels: map[string][]string{"tenant": {"acme"}}, Output: json.RawMessage(`"Here are the refund steps"`),
		Verdict: &otel.Verdict{Time: at.Add(time.Minute), Verdict: "reject", ReasonCode: "scope", Reason: "the window was missing", Reviewer: "ann"},
	}
	bare := judged
	bare.Verdict = nil
	c := &collector{status: http.StatusOK}
	srv := httptest.NewServer(c)
	t.Cleanup(srv.Close)
	e := otel.New(srv.URL+"/v1/traces", map[string]string{"Authorization": "Basic x"}, "0.6.7", srv.Client())

	_, err := e.Export(context.Background(), []otel.Run{judged}, true)
	require.NoError(t, err)
	_, err = e.Export(context.Background(), []otel.Run{bare}, false)
	require.NoError(t, err)

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
	assert.NotContains(t, again, "events", "a run without a verdict has no event")
	events := first["events"].([]any)
	require.Len(t, events, 1, "the verdict of the run goes as one event")
	event := events[0].(map[string]any)
	assert.Equal(t, "gen_ai.evaluation.result", event["name"])
	assert.Equal(t, map[string]any{
		"gen_ai.evaluation.name": "nodloop.verdict", "gen_ai.evaluation.score.label": "reject", "gen_ai.evaluation.score.value": 0.0,
		"gen_ai.evaluation.explanation": "the window was missing", "nodloop.reason_code": "scope", "nodloop.reviewer": "ann",
	}, attrs(event))
}

func TestExporterExportBatchesAndFails(t *testing.T) {
	runs := make([]otel.Run, 0, 450)
	for i := range 450 {
		runs = append(runs, otel.Run{ID: string(rune('a' + i%26)), Producer: "bot"})
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

			_, err := otel.New(srv.URL, nil, "", srv.Client()).Export(context.Background(), runs, false)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Len(t, c.bodies, tc.want.requests)
		})
	}
}

func TestExporterExportSpan(t *testing.T) {
	at := time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)
	type want struct {
		// Attributes of the span checked by key
		attrs map[string]any
		// Score labels of the events in order
		events []string
		// The outcome events Export counts
		sent int
	}
	gitC := []otel.Ref{{ID: "git-c", Version: 2}}
	tcs := []struct {
		name string
		args otel.Run
		want want
	}{
		{
			"the subject goes along as an attribute",
			otel.Run{ID: "r1", Producer: "bot", Subject: "ticket 7"},
			want{attrs: map[string]any{"nodloop.subject": "ticket 7"}},
		},
		{
			"an output that is no JSON string goes along as written",
			otel.Run{ID: "r1", Producer: "bot", Output: json.RawMessage(`{"steps":2}`)},
			want{attrs: map[string]any{"output.value": `{"steps":2}`}},
		},
		{
			"a drawn run names its arm and its items",
			otel.Run{ID: "r1", Producer: "bot", Applied: gitC, Drawn: true},
			want{attrs: map[string]any{
				"nodloop.holdout.arm":       "applied",
				"nodloop.knowledge.applied": map[string]any{"values": []any{map[string]any{"stringValue": "git-c:2"}}},
			}},
		},
		{
			"a withheld run names the items the holdout kept",
			otel.Run{ID: "r1", Producer: "bot", Withheld: gitC, Drawn: true},
			want{attrs: map[string]any{
				"nodloop.holdout.arm":        "withheld",
				"nodloop.knowledge.withheld": map[string]any{"values": []any{map[string]any{"stringValue": "git-c:2"}}},
			}},
		},
		{
			"a run with items and no draw is undrawn",
			otel.Run{ID: "r1", Producer: "bot", Applied: gitC},
			want{attrs: map[string]any{"nodloop.holdout.arm": "undrawn"}},
		},
		{
			"a run without items has no arm",
			otel.Run{ID: "r1", Producer: "bot", Drawn: true},
			want{attrs: map[string]any{"nodloop.holdout.arm": nil}},
		},
		{
			"the outcome goes as an evaluation after the verdict",
			otel.Run{
				ID: "r1", Producer: "bot", Verdict: &otel.Verdict{Time: at, Verdict: "reject", Reviewer: "ann"},
				Outcome: &otel.Outcome{Time: at.Add(time.Hour), Result: "confirmed", ConfirmedCause: "a slow consumer", Reviewer: "ann"},
			},
			want{attrs: map[string]any{"nodloop.run.id": "r1"}, events: []string{"reject", "confirmed"}, sent: 1},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := &collector{status: http.StatusOK}
			srv := httptest.NewServer(c)
			t.Cleanup(srv.Close)

			sent, err := otel.New(srv.URL, nil, "", srv.Client()).Export(context.Background(), []otel.Run{tc.args}, true)

			require.NoError(t, err)
			assert.Equal(t, tc.want.sent, sent)
			spans := c.spans()
			require.Len(t, spans, 1)
			got := attrs(spans[0])
			for k, v := range tc.want.attrs {
				assert.Equal(t, v, got[k], k)
			}
			var labels []string
			events, _ := spans[0]["events"].([]any)
			for _, e := range events {
				labels = append(labels, attrs(e.(map[string]any))["gen_ai.evaluation.score.label"].(string))
			}
			assert.Equal(t, tc.want.events, labels)
		})
	}
}

func TestExporterExportFailsToSend(t *testing.T) {
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	tcs := []struct {
		name string
		// The endpoint of the exporter
		args string
		want string
	}{
		{"a URL no request can be built from fails", "http://bad host/v1/traces", "invalid character"},
		{"a collector that is gone fails", closed.URL, "connection refused"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := otel.New(tc.args, nil, "", http.DefaultClient).Export(context.Background(), []otel.Run{{ID: "r1"}}, false)
			assert.ErrorContains(t, err, tc.want)
		})
	}
}
