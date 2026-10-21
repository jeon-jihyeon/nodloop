package otel_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/otel"
)

func TestExporterResponse(t *testing.T) {
	type args struct {
		protocol  otel.Protocol
		status    int
		body      string
		truncated bool
	}
	type want struct {
		rejected int
		terminal bool
		retry    bool
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"JSON success", args{otel.ProtocolJSON, 200, `{}`, false}, want{}},
		{"JSON partial", args{otel.ProtocolJSON, 200,
			`{"partialSuccess":{"rejectedSpans":"1","errorMessage":"private"}}`, false}, want{1, true, false}},
		{"JSON warning", args{otel.ProtocolJSON, 200,
			`{"partialSuccess":{"errorMessage":"private"}}`, false}, want{0, true, false}},
		{"JSON aliases", args{otel.ProtocolJSON, 200,
			`{"partial_success":{"rejected_spans":1}}`, false}, want{1, true, false}},
		{"quoted exponent", args{otel.ProtocolJSON, 200,
			`{"partialSuccess":{"rejectedSpans":"1e0"}}`, false}, want{1, true, false}},
		{"unquoted exponent", args{otel.ProtocolJSON, 200,
			`{"partialSuccess":{"rejectedSpans":1e0}}`, false}, want{1, true, false}},
		{"exact decimal integer", args{otel.ProtocolJSON, 200,
			`{"partialSuccess":{"rejectedSpans":"10e-1"}}`, false}, want{1, true, false}},
		{"fractional count", args{otel.ProtocolJSON, 200,
			`{"partialSuccess":{"rejectedSpans":"1.0000000000000001"}}`, false}, want{0, false, true}},
		{"huge exponent", args{otel.ProtocolJSON, 200,
			`{"partialSuccess":{"rejectedSpans":"1e999999999"}}`, false}, want{0, false, true}},
		{"zero huge exponent", args{otel.ProtocolJSON, 200,
			`{"partialSuccess":{"rejectedSpans":"0e999999999"}}`, false}, want{}},
		{"exponent underflow", args{otel.ProtocolJSON, 200,
			`{"partialSuccess":{"rejectedSpans":"1e-999999999"}}`, false}, want{0, false, true}},
		{"exponent overflow", args{otel.ProtocolJSON, 200,
			`{"partialSuccess":{"rejectedSpans":"1e9999999999999999999999999"}}`, false}, want{0, false, true}},
		{"integer overflow", args{otel.ProtocolJSON, 200,
			`{"partialSuccess":{"rejectedSpans":"9223372036854775808"}}`, false}, want{0, false, true}},
		{"long integer representation", args{otel.ProtocolJSON, 200,
			`{"partialSuccess":{"rejectedSpans":"1` + strings.Repeat("0", 128) + `"}}`, false}, want{0, false, true}},
		{"log count alias ignored for spans", args{otel.ProtocolJSON, 200,
			`{"partial_success":{"rejected_log_records":1}}`, false}, want{}},
		{"protobuf partial", args{otel.ProtocolProtobuf, 200, "\x0a\x02\x08\x01", false}, want{1, true, false}},
		{"protobuf warning", args{otel.ProtocolProtobuf, 200, "\x0a\x03\x12\x01x", false}, want{0, true, false}},
		{"protobuf empty partial", args{otel.ProtocolProtobuf, 200, "\x0a\x00", false}, want{}},
		{"protobuf unknown fields", args{otel.ProtocolProtobuf, 200,
			"\x10\x01\x19abcdefgh\x25abcd\x2a\x01x", false}, want{}},
		{"permanent", args{otel.ProtocolJSON, 400, "private", false}, want{1, true, false}},
		{"other permanent", args{otel.ProtocolJSON, 500, "private", false}, want{1, true, false}},
		{"rate limited", args{otel.ProtocolJSON, 429, "private", false}, want{0, false, true}},
		{"bad gateway", args{otel.ProtocolJSON, 502, "private", false}, want{0, false, true}},
		{"unavailable", args{otel.ProtocolJSON, 503, "private", false}, want{0, false, true}},
		{"gateway timeout", args{otel.ProtocolJSON, 504, "private", false}, want{0, false, true}},
		{"unexpected success status", args{otel.ProtocolJSON, 204, "", false}, want{1, true, false}},
		{"invalid JSON", args{otel.ProtocolJSON, 200, "private", false}, want{0, false, true}},
		{"JSON null", args{otel.ProtocolJSON, 200, "null", false}, want{0, false, true}},
		{"JSON wrong partial type", args{otel.ProtocolJSON, 200, `{"partialSuccess":1}`, false}, want{0, false, true}},
		{"JSON invalid count", args{otel.ProtocolJSON, 200,
			`{"partialSuccess":{"rejectedSpans":"bad"}}`, false}, want{0, false, true}},
		{"negative count", args{otel.ProtocolJSON, 200,
			`{"partialSuccess":{"rejectedSpans":"-1"}}`, false}, want{0, false, true}},
		{"excessive count", args{otel.ProtocolJSON, 200,
			`{"partialSuccess":{"rejectedSpans":"2"}}`, false}, want{0, false, true}},
		{"short response read", args{otel.ProtocolJSON, 200, "{", true}, want{0, false, true}},
		{"large response", args{otel.ProtocolJSON, 200, strings.Repeat(" ", 1<<20) + "{}", false}, want{1, true, false}},
	}
	for _, raw := range []string{"\x00", "\x80", "\x09a", "\x15a", "\x0a\x80", "\x0a\x05a",
		"\x0b", "\x08\x01", "\x0a\x02\x0a\x00", "\x0a\x02\x10\x00", "\x10\x80"} {
		tcs = append(tcs, struct {
			name string
			args args
			want want
		}{
			fmt.Sprintf("malformed protobuf %x", raw), args{otel.ProtocolProtobuf, 200, raw, false}, want{0, false, true}})
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.args.truncated {
					w.Header().Set("Content-Length", "100")
				}
				w.WriteHeader(tc.args.status)
				_, _ = w.Write([]byte(tc.args.body))
			}))
			t.Cleanup(srv.Close)
			var done []string
			e := otel.New(otel.Config{Traces: srv.URL, Protocol: tc.args.protocol, Client: srv.Client()})
			e = e.Checkpoint(nil, func(keys []string) error { done = slices.Clone(keys); return nil })
			sent, err := e.Export(t.Context(), otel.Batch{Runs: []otel.Run{{ID: "r1", Time: at}}})
			assert.Equal(t, tc.want.rejected, sent.RejectedSpans)
			if tc.want.terminal {
				require.ErrorIs(t, err, otel.ErrRejected)
				require.Len(t, done, 1)
				assert.Regexp(t, "^[a-f0-9]{64}$", done[0])
			} else {
				assert.Empty(t, done)
			}
			if tc.want.retry {
				assert.ErrorIs(t, err, otel.ErrRetryable)
			}
			if !tc.want.retry && !tc.want.terminal {
				assert.NoError(t, err)
			}
			if err != nil {
				assert.NotContains(t, err.Error(), "private")
			}
		})
	}
}

func TestExporterTerminalReceiptAcrossAppend(t *testing.T) {
	for _, status := range []int{200, 400} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			t.Parallel()
			var mu sync.Mutex
			var ids [][]string
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					ResourceSpans []struct {
						ScopeSpans []struct {
							Spans []struct {
								SpanID string `json:"spanId"`
							} `json:"spans"`
						} `json:"scopeSpans"`
					} `json:"resourceSpans"`
				}
				if json.NewDecoder(r.Body).Decode(&body) != nil {
					w.WriteHeader(400)
					return
				}
				var got []string
				for _, rs := range body.ResourceSpans {
					for _, ss := range rs.ScopeSpans {
						for _, s := range ss.Spans {
							got = append(got, s.SpanID)
						}
					}
				}
				mu.Lock()
				defer mu.Unlock()
				requests++
				ids = append(ids, got)
				switch requests {
				case 1:
					w.WriteHeader(status)
					if status == 200 {
						_, _ = w.Write([]byte(`{"partialSuccess":{"rejectedSpans":"1"}}`))
					}
				case 2:
					w.WriteHeader(503)
				}
			}))
			t.Cleanup(srv.Close)
			b := otel.Batch{}
			for i := range 401 {
				b.Runs = append(b.Runs, otel.Run{ID: fmt.Sprint("r", i), Time: at})
			}
			var done []string
			base := exporter(srv, false, false)
			save := func(keys []string) error { done = slices.Clone(keys); return nil }
			_, err := base.Checkpoint(nil, save).Export(t.Context(), b)
			require.ErrorIs(t, err, otel.ErrRetryable)
			require.Len(t, done, 200)
			b.Runs = append([]otel.Run{{ID: "appended", Time: at}}, b.Runs...)
			sent, err := base.Checkpoint(done, save).Export(t.Context(), b)
			require.NoError(t, err)
			assert.Equal(t, 202, sent.Runs)
			mu.Lock()
			defer mu.Unlock()
			require.Len(t, ids, 4)
			for _, got := range append(slices.Clone(ids[2]), ids[3]...) {
				assert.NotContains(t, ids[0], got)
			}
		})
	}
}

func TestExporterCheckpointFailure(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(400) }))
	t.Cleanup(srv.Close)
	e := exporter(srv, false, false).Checkpoint(nil, func([]string) error { return assert.AnError })
	sent, err := e.Export(context.Background(), otel.Batch{Runs: []otel.Run{{ID: "r1", Time: at}}})
	assert.ErrorIs(t, err, assert.AnError)
	assert.ErrorIs(t, err, otel.ErrRetryable)
	assert.Zero(t, sent)
}

func TestExporterLogsTerminalReceipt(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var got [][]string
	logs := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/logs" {
			return
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			w.WriteHeader(400)
			return
		}
		var ids []string
		for _, resource := range body["resourceLogs"].([]any) {
			for _, scope := range resource.(map[string]any)["scopeLogs"].([]any) {
				for _, record := range scope.(map[string]any)["logRecords"].([]any) {
					ids = append(ids, attrs(record.(map[string]any))["log.record.uid"].(string))
				}
			}
		}
		mu.Lock()
		defer mu.Unlock()
		logs++
		got = append(got, ids)
		switch logs {
		case 1:
			_, _ = w.Write([]byte(`{"partialSuccess":{"rejectedLogRecords":"3"}}`))
		case 2:
			w.WriteHeader(503)
		}
	}))
	t.Cleanup(srv.Close)
	var done []string
	save := func(keys []string) error { done = slices.Clone(keys); return nil }
	b := otel.Batch{}
	for i := range 201 {
		b.Evaluations = append(b.Evaluations, otel.Evaluation{
			Name: otel.EvaluationVerdict, Span: otel.TraceSpan(fmt.Sprint("v", i)), Time: at,
		})
	}
	base := exporter(srv, true, false)
	sent, err := base.Checkpoint(nil, save).Export(t.Context(), b)
	require.ErrorIs(t, err, otel.ErrRetryable)
	assert.Equal(t, 3, sent.RejectedLogRecords)
	assert.Equal(t, 197, sent.LogRecords)
	require.Len(t, done, 200)
	b.Evaluations = append([]otel.Evaluation{{Name: otel.EvaluationVerdict,
		Span: otel.TraceSpan("appended"), Time: at}}, b.Evaluations...)
	sent, err = base.Checkpoint(done, save).Export(t.Context(), b)
	require.NoError(t, err)
	assert.Equal(t, 2, sent.LogRecords)
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, got, 3)
	for _, id := range got[2] {
		assert.NotContains(t, got[0], id)
	}
}

func TestExporterReceiptScope(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/first" {
			w.WriteHeader(400)
		}
	}))
	t.Cleanup(srv.Close)
	cfg := otel.Config{Traces: srv.URL + "/first", Protocol: otel.ProtocolJSON, Client: srv.Client()}
	var done []string
	save := func(keys []string) error { done = slices.Clone(keys); return nil }
	b := otel.Batch{Runs: []otel.Run{{ID: "r1", Time: at}}}
	_, err := otel.New(cfg).Checkpoint(nil, save).Export(t.Context(), b)
	require.ErrorIs(t, err, otel.ErrRejected)
	require.Len(t, done, 1)
	cfg.Traces = srv.URL + "/second"
	sent, err := otel.New(cfg).Checkpoint(done, save).Export(t.Context(), b)
	require.NoError(t, err)
	assert.Equal(t, 1, sent.Runs)
}
