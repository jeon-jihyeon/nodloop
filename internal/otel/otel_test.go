package otel_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/otel"
)

var at = time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)

func nanos(t time.Time) string {
	return strconv.FormatInt(t.UnixNano(), 10)
}

// The requests a collector received
type collector struct {
	mu     sync.Mutex
	status map[string]int
	paths  []string
	types  []string
	raw    [][]byte
	bodies []map[string]any
}

func (c *collector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var decoded map[string]any
	_ = json.Unmarshal(body, &decoded)
	c.mu.Lock()
	c.paths = append(c.paths, r.URL.Path)
	c.types = append(c.types, r.Header.Get("Content-Type"))
	c.raw = append(c.raw, body)
	c.bodies = append(c.bodies, decoded)
	status := c.status[r.URL.Path]
	c.mu.Unlock()
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
}

// One span with its plain attributes
type gotSpan struct {
	traceID, spanID, parent, name string
	kind                          float64
	start, end                    string
	attrs                         map[string]any
	links                         []string
	status                        float64
}

// The spans of every request by name
func (c *collector) spans() map[string]gotSpan {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string]gotSpan{}
	for _, b := range c.bodies {
		rss, _ := b["resourceSpans"].([]any)
		for _, rs := range rss {
			for _, ss := range rs.(map[string]any)["scopeSpans"].([]any) {
				for _, s := range ss.(map[string]any)["spans"].([]any) {
					m := s.(map[string]any)
					g := gotSpan{
						traceID: m["traceId"].(string), spanID: m["spanId"].(string), name: m["name"].(string), kind: m["kind"].(float64),
						start: m["startTimeUnixNano"].(string), end: m["endTimeUnixNano"].(string), attrs: attrs(m),
					}
					g.parent, _ = m["parentSpanId"].(string)
					links, _ := m["links"].([]any)
					for _, l := range links {
						g.links = append(g.links, l.(map[string]any)["spanId"].(string))
					}
					if st, ok := m["status"].(map[string]any); ok {
						g.status = st["code"].(float64)
					}
					out[g.name] = g
				}
			}
		}
	}
	return out
}

// The log records of every request in order
func (c *collector) logs() []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []map[string]any
	for _, b := range c.bodies {
		rls, _ := b["resourceLogs"].([]any)
		for _, rl := range rls {
			for _, sl := range rl.(map[string]any)["scopeLogs"].([]any) {
				for _, l := range sl.(map[string]any)["logRecords"].([]any) {
					out = append(out, l.(map[string]any))
				}
			}
		}
	}
	return out
}

// The attributes of a span or a log record by key with their plain values
func attrs(m map[string]any) map[string]any {
	out := map[string]any{}
	list, _ := m["attributes"].([]any)
	for _, a := range list {
		kv := a.(map[string]any)
		for kind, v := range kv["value"].(map[string]any) {
			if kind == "arrayValue" {
				var values []any
				for _, item := range v.(map[string]any)["values"].([]any) {
					values = append(values, item.(map[string]any)["stringValue"])
				}
				v = values
			}
			out[kv["key"].(string)] = v
		}
	}
	return out
}

// One record of every kind
func batch() otel.Batch {
	return otel.Batch{
		Runs: []otel.Run{{
			ID: "r1", Time: at.Add(90 * time.Second), Duration: 90 * time.Second, Producer: "session", SessionID: "s1",
			Labels: map[string][]string{"repo": {"nodloop"}}, Withheld: []otel.Ref{{ID: "git-c", Version: 2}}, Drawn: true, Unit: "u1", Share: 0.1,
			Plugin: "0.9.0", Output: json.RawMessage(`"run git -C repo status"`),
		}},
		Evaluations: []otel.Evaluation{
			{
				Name: otel.EvaluationVerdict, Time: at.Add(time.Minute), SessionID: "s1", Label: "reject", Explanation: "wrong flag",
				ReasonCode: "fact", Reviewer: "session", Span: otel.VerdictSpan("r1", at.Add(time.Minute)), Target: otel.RunSpan("r1"),
			},
			{
				Name: otel.EvaluationOutcome, Time: at.Add(time.Hour), SessionID: "s1", Label: "inconclusive", Reviewer: "ann", Human: true,
				Span: otel.OutcomeSpan("r1", at.Add(time.Hour)), Target: otel.RunSpan("r1"),
			},
		},
	}
}

func exporter(srv *httptest.Server, logs bool, content bool) *otel.Exporter {
	cfg := otel.Config{
		Traces: srv.URL + "/v1/traces", Protocol: otel.ProtocolJSON, Version: "0.9.0", Client: srv.Client(), Content: content,
		Headers: map[string]string{"Authorization": "Basic k"},
	}
	if logs {
		cfg.Logs = srv.URL + "/v1/logs"
	}
	return otel.New(cfg)
}

// Every record becomes its spans with the timing, the nesting and the links of what it judged
func TestExporterExportSpans(t *testing.T) {
	c := &collector{}
	srv := httptest.NewServer(c)
	t.Cleanup(srv.Close)

	sent, err := exporter(srv, false, false).Export(context.Background(), batch())

	require.NoError(t, err)
	assert.Equal(t, otel.Sent{Runs: 1, Verdicts: 1, Outcomes: 1}, sent)
	spans := c.spans()
	run := otel.RunSpan("r1")
	turn := spans["invoke_agent session"]
	assert.Equal(t, gotSpan{
		traceID: run.TraceID, spanID: run.SpanID, name: "invoke_agent session", kind: 1, start: nanos(at), end: nanos(at.Add(90 * time.Second)),
		attrs: map[string]any{
			"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": "session", "openinference.span.kind": "AGENT", "nodloop.run.id": "r1",
			"session.id": "s1", "gen_ai.conversation.id": "s1", "nodloop.plugin.version": "0.9.0", "nodloop.label.repo": []any{"nodloop"},
			"nodloop.knowledge.withheld": []any{"git-c:2"}, "nodloop.holdout.unit": "u1", "nodloop.holdout.share": 0.1, "nodloop.holdout.arm": "withheld",
		},
	}, turn, "the output stays home without content")
	verdict := spans["evaluate nodloop.verdict"]
	assert.Equal(t, []string{run.SpanID}, verdict.links)
	assert.Equal(t, map[string]any{
		"openinference.span.kind": "EVALUATOR", "session.id": "s1", "gen_ai.conversation.id": "s1",
		"gen_ai.evaluation.name": "nodloop.verdict", "gen_ai.evaluation.score.label": "reject", "gen_ai.evaluation.score.value": 0.0,
		"annotation.name": "nodloop.verdict", "annotation.label": "reject", "annotation.score": 0.0, "annotation.annotator_kind": "LLM",
		"nodloop.reason_code": "fact", "nodloop.reviewer": "session",
	}, verdict.attrs, "the reason stays home without content")
	assert.NotContains(t, spans["evaluate nodloop.outcome"].attrs, "gen_ai.evaluation.score.value", "an inconclusive check has no score")
	assert.Equal(t, "HUMAN", spans["evaluate nodloop.outcome"].attrs["annotation.annotator_kind"])
	assert.Len(t, spans, 3)
	assert.Equal(t, []string{"/v1/traces"}, c.paths)
}

// Text goes along only with content
func TestExporterExportContent(t *testing.T) {
	c := &collector{}
	srv := httptest.NewServer(c)
	t.Cleanup(srv.Close)

	_, err := exporter(srv, false, true).Export(context.Background(), batch())

	require.NoError(t, err)
	spans := c.spans()
	assert.Equal(t, "run git -C repo status", spans["invoke_agent session"].attrs["output.value"])
	assert.Equal(t, "text/plain", spans["invoke_agent session"].attrs["output.mime_type"])
	assert.Equal(t, "wrong flag", spans["evaluate nodloop.verdict"].attrs["gen_ai.evaluation.explanation"])
	assert.Equal(t, "wrong flag", spans["evaluate nodloop.verdict"].attrs["annotation.explanation"])
}

// Every evaluation goes as a log record of the GenAI conventions parented to what it evaluates after every span
func TestExporterExportLogs(t *testing.T) {
	c := &collector{}
	srv := httptest.NewServer(c)
	t.Cleanup(srv.Close)

	sent, err := exporter(srv, true, false).Export(context.Background(), batch())

	require.NoError(t, err)
	assert.Equal(t, 2, sent.LogRecords)
	assert.Equal(t, []string{"/v1/traces", "/v1/logs"}, c.paths)
	logs := c.logs()
	require.Len(t, logs, 2)
	run := otel.RunSpan("r1")
	verdict := logs[0]
	assert.Equal(t, []any{"gen_ai.evaluation.result", 9.0, run.TraceID, run.SpanID, nanos(at.Add(time.Minute))},
		[]any{verdict["eventName"], verdict["severityNumber"], verdict["traceId"], verdict["spanId"], verdict["timeUnixNano"]})
	a := attrs(verdict)
	assert.Equal(t, otel.VerdictSpan("r1", at.Add(time.Minute)).TraceID, a["log.record.uid"])
	assert.Equal(t, "LLM", a["annotations.0.annotation.annotator_kind"])
	assert.Equal(t, "reject", a["annotations.0.annotation.label"])
	assert.Equal(t, "s1", a["session.id"])
	assert.Equal(t, "HUMAN", attrs(logs[1])["annotations.0.annotation.annotator_kind"])
}

// Two exports of one batch send the same bytes so a collector keyed on ids never counts a record twice
func TestExporterExportIsStable(t *testing.T) {
	c := &collector{}
	srv := httptest.NewServer(c)
	t.Cleanup(srv.Close)
	e := exporter(srv, true, true)

	_, first := e.Export(context.Background(), batch())
	_, second := e.Export(context.Background(), batch())

	require.NoError(t, first)
	require.NoError(t, second)
	require.Len(t, c.raw, 4)
	assert.Equal(t, c.raw[0], c.raw[2])
	assert.Equal(t, c.raw[1], c.raw[3])
}

// A field of a protobuf message as the test reads it back
type field struct {
	num   int
	value []byte
	n     uint64
}

// The fields of one protobuf message in order
func fields(t *testing.T, b []byte) []field {
	t.Helper()
	var out []field
	for len(b) > 0 {
		key, n := uvarint(b)
		require.Positive(t, n)
		b = b[n:]
		f := field{num: int(key >> 3)}
		switch key & 7 {
		case 0:
			v, n := uvarint(b)
			f.n, b = v, b[n:]
		case 1:
			f.n, b = leUint64(b[:8]), b[8:]
		case 2:
			l, n := uvarint(b)
			f.value, b = b[n:n+int(l)], b[n+int(l):]
		default:
			t.Fatalf("wire type %d", key&7)
		}
		out = append(out, f)
	}
	return out
}

func uvarint(b []byte) (uint64, int) {
	var v uint64
	for i, c := range b {
		v |= uint64(c&0x7f) << (7 * i)
		if c < 0x80 {
			return v, i + 1
		}
	}
	return 0, 0
}

func leUint64(b []byte) uint64 {
	var v uint64
	for i := 7; i >= 0; i-- {
		v = v<<8 | uint64(b[i])
	}
	return v
}

// The first field of the number
func first(t *testing.T, fs []field, num int) field {
	t.Helper()
	for _, f := range fs {
		if f.num == num {
			return f
		}
	}
	t.Fatalf("no field %d", num)
	return field{}
}

// The protobuf body holds the ids as bytes, the times as fixed64 and the attributes of the OTLP proto files
func TestExporterExportProtobuf(t *testing.T) {
	c := &collector{}
	srv := httptest.NewServer(c)
	t.Cleanup(srv.Close)
	cfg := otel.Config{Traces: srv.URL + "/v1/traces", Logs: srv.URL + "/v1/logs", Protocol: otel.ProtocolProtobuf, Version: "0.9.0", Client: srv.Client()}
	b := batch()
	b.Evaluations = b.Evaluations[:1]

	_, err := otel.New(cfg).Export(context.Background(), b)

	require.NoError(t, err)
	assert.Equal(t, []string{"application/x-protobuf", "application/x-protobuf"}, c.types)
	resourceSpans := fields(t, first(t, fields(t, c.raw[0]), 1).value)
	resource := fields(t, first(t, resourceSpans, 1).value)
	service := fields(t, first(t, resource, 1).value)
	assert.Equal(t, "service.name", string(first(t, service, 1).value))
	assert.Equal(t, "nodloop", string(first(t, fields(t, first(t, service, 2).value), 1).value))
	scopeSpans := fields(t, first(t, resourceSpans, 2).value)
	assert.Equal(t, "nodloop", string(first(t, fields(t, first(t, scopeSpans, 1).value), 1).value))
	var names []string
	var turn []field
	for _, f := range scopeSpans {
		if f.num == 2 {
			s := fields(t, f.value)
			names = append(names, string(first(t, s, 5).value))
			if string(first(t, s, 5).value) == "invoke_agent session" {
				turn = s
			}
		}
	}
	assert.Equal(t, []string{"invoke_agent session", "evaluate nodloop.verdict"}, names)
	run := otel.RunSpan("r1")
	assert.Equal(t, run.TraceID, hexOf(first(t, turn, 1).value))
	assert.Equal(t, run.SpanID, hexOf(first(t, turn, 2).value))
	assert.Equal(t, uint64(1), first(t, turn, 6).n)
	assert.Equal(t, uint64(at.UnixNano()), first(t, turn, 7).n)
	assert.Equal(t, uint64(at.Add(90*time.Second).UnixNano()), first(t, turn, 8).n)
	resourceLogs := fields(t, first(t, fields(t, c.raw[1]), 1).value)
	record := fields(t, first(t, fields(t, first(t, resourceLogs, 2).value), 2).value)
	assert.Equal(t, uint64(9), first(t, record, 2).n)
	assert.Equal(t, run.SpanID, hexOf(first(t, record, 10).value))
	assert.Equal(t, "gen_ai.evaluation.result", string(first(t, record, 12).value))
}

func hexOf(b []byte) string {
	const digits = "0123456789abcdef"
	var out bytes.Buffer
	for _, c := range b {
		out.WriteByte(digits[c>>4])
		out.WriteByte(digits[c&15])
	}
	return out.String()
}

// A refused request stops the export with the counts of what went out before it
func TestExporterExportRefused(t *testing.T) {
	type want struct {
		sent  otel.Sent
		paths []string
	}
	tcs := []struct {
		name string
		// The path the collector refuses
		args string
		want want
	}{
		{"a refused logs request fails after the spans went out", "/v1/logs",
			want{otel.Sent{Runs: 1, Verdicts: 1, Outcomes: 1}, []string{"/v1/traces", "/v1/logs"}}},
		{"refused spans fail before any log record", "/v1/traces", want{otel.Sent{}, []string{"/v1/traces"}}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := &collector{status: map[string]int{tc.args: http.StatusBadRequest}}
			srv := httptest.NewServer(c)
			t.Cleanup(srv.Close)

			sent, err := exporter(srv, true, false).Export(context.Background(), batch())

			require.ErrorIs(t, err, otel.ErrStatus)
			assert.Equal(t, tc.want.sent, sent)
			assert.Equal(t, tc.want.paths, c.paths)
		})
	}
}

// The error of a failed send names the endpoint without the token a collector may take in its password or query
func TestExporterExportFailsToSend(t *testing.T) {
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	refusing := httptest.NewServer(&collector{status: map[string]int{"/v1/traces": http.StatusUnauthorized}})
	t.Cleanup(refusing.Close)
	type want struct {
		contains string
		// A secret of the endpoint the error must not repeat
		hidden string
	}
	tcs := []struct {
		name string
		// The endpoint of the exporter
		args string
		want want
	}{
		{"a URL no request can be built from fails without the password or the query", "http://ann:secret@bad host/v1/traces?token=secret", want{"invalid character", "secret"}},
		{"a collector that is gone fails without the password or the query", "http://ann:secret@" + closed.Listener.Addr().String() + "/v1/traces?token=secret",
			want{"connection refused", "secret"}},
		{"a refusal names the status without the password or the query", "http://ann:secret@" + refusing.Listener.Addr().String() + "/v1/traces?token=secret",
			want{"answered 401", "secret"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := otel.New(otel.Config{Traces: tc.args, Protocol: otel.ProtocolJSON, Client: http.DefaultClient})

			_, err := e.Export(context.Background(), otel.Batch{Runs: []otel.Run{{ID: "r1"}}})

			require.Error(t, err)
			assert.ErrorContains(t, err, tc.want.contains)
			assert.NotContains(t, err.Error(), tc.want.hidden)
		})
	}
}
