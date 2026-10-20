package main

import (
	"bytes"
	"cmp"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

var exportAt = time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)

// One span a collector received
type gotSpan struct {
	TraceID string `json:"traceId"`
	SpanID  string `json:"spanId"`
	Parent  string `json:"parentSpanId"`
	Name    string `json:"name"`
	Start   string `json:"startTimeUnixNano"`
	End     string `json:"endTimeUnixNano"`
	Links   []struct {
		TraceID string `json:"traceId"`
		SpanID  string `json:"spanId"`
	} `json:"links"`
}

// One log record a collector received
type gotLog struct {
	EventName string `json:"eventName"`
	TraceID   string `json:"traceId"`
	SpanID    string `json:"spanId"`
}

// The requests of a collector that reads OTLP JSON
type otlpCollector struct {
	mu     sync.Mutex
	status int
	paths  []string
	types  []string
	auths  []string
	spans  []gotSpan
	logs   []gotLog
}

func (c *otlpCollector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ResourceSpans []struct {
			ScopeSpans []struct {
				Spans []gotSpan `json:"spans"`
			} `json:"scopeSpans"`
		} `json:"resourceSpans"`
		ResourceLogs []struct {
			ScopeLogs []struct {
				LogRecords []gotLog `json:"logRecords"`
			} `json:"scopeLogs"`
		} `json:"resourceLogs"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.paths = append(c.paths, r.URL.Path)
	c.types = append(c.types, r.Header.Get("Content-Type"))
	c.auths = append(c.auths, r.Header.Get("Authorization"))
	for _, rs := range body.ResourceSpans {
		for _, ss := range rs.ScopeSpans {
			c.spans = append(c.spans, ss.Spans...)
		}
	}
	for _, rl := range body.ResourceLogs {
		for _, sl := range rl.ScopeLogs {
			c.logs = append(c.logs, sl.LogRecords...)
		}
	}
	w.WriteHeader(cmp.Or(c.status, http.StatusOK))
}

// Records with one run of bot at 09:00, its reject at 09:01 and an outcome at 09:02
func exportFixture(t *testing.T) string {
	t.Helper()
	records := t.TempDir()
	a := app{cfg: config{recordDir: records}, now: func() time.Time { return exportAt }}
	traces, err := a.traces()
	require.NoError(t, err)
	run, err := trace.NewRun("bot", "", nil, nil, []byte("steps"), exportAt)
	require.NoError(t, err)
	require.NoError(t, traces.Append(t.Context(), run.Record()))
	verdicts, err := a.feedback()
	require.NoError(t, err)
	fb, err := feedback.New(run.ID, feedback.VerdictReject, feedback.ReasonScope, "no window", nil, "ann", exportAt.Add(time.Minute))
	require.NoError(t, err)
	require.NoError(t, verdicts.Append(t.Context(), fb.Record()))
	outcomes, err := a.outcomes()
	require.NoError(t, err)
	o, err := feedback.NewOutcome(run.ID, feedback.ResultConfirmed, "a slow consumer", "", "ann", exportAt.Add(2*time.Minute))
	require.NoError(t, err)
	require.NoError(t, outcomes.Append(t.Context(), o.Record()))
	return records
}

// export otel resolves its endpoint, headers and protocol from the flags and then the OTLP environment
func TestRunExportOtelTarget(t *testing.T) {
	type args struct {
		// {srv} stands for the URL of the collector
		args []string
		env  map[string]string
	}
	type want struct {
		code   int
		stdout string
		stderr string
		paths  []string
		types  []string
		auths  []string
	}
	sent := "exported 1 runs, 1 verdicts and 1 outcomes\n"
	proto, jsonType := "application/x-protobuf", "application/json"
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"the flag names a traces URL and sends traces only in protobuf", args{[]string{"otel", "--endpoint", "{srv}/v1/traces", "--header", "Authorization=Basic flag"}, nil},
			want{0, sent, "", []string{"/v1/traces"}, []string{proto}, []string{"Basic flag"}}},
		{"the environment alone names the endpoint, headers and protocol", args{[]string{"otel"},
			map[string]string{envOTLPEndpoint: "{srv}/e/", envOTLPHeaders: "Authorization=Basic%20env", envOTLPProtocol: "http/json"}},
			want{0, sent, "", []string{"/e/v1/traces", "/e/v1/logs"}, []string{jsonType, jsonType}, []string{"Basic env", "Basic env"}}},
		{"the logs variable of the environment names the logs endpoint", args{[]string{"otel"},
			map[string]string{envOTLPTracesEndpoint: "{srv}/t", envOTLPLogsEndpoint: "{srv}/l"}},
			want{0, sent, "", []string{"/t", "/l"}, []string{proto, proto}, []string{"", ""}}},
		{"the protocol flag wins over the environment", args{[]string{"otel", "--protocol", "http/json"}, map[string]string{envOTLPTracesEndpoint: "{srv}/t", envOTLPProtocol: "http/protobuf"}},
			want{0, sent, "", []string{"/t"}, []string{jsonType}, []string{""}}},
		{"a header flag wins over the same name of the variable", args{[]string{"otel", "--header", "Authorization=Basic flag"},
			map[string]string{envOTLPTracesEndpoint: "{srv}/t", envOTLPHeaders: "Authorization=Basic%20env"}}, want{0, sent, "", []string{"/t"}, []string{proto}, []string{"Basic flag"}}},
		{"grpc of the environment fails", args{[]string{"otel"}, map[string]string{envOTLPEndpoint: "{srv}", envOTLPProtocol: "grpc"}},
			want{1, "", `unknown otel protocol "grpc"`, nil, nil, nil}},
		{"a pair without = in the variable fails without its value", args{[]string{"otel"}, map[string]string{envOTLPEndpoint: "{srv}", envOTLPHeaders: "Authorization"}},
			want{1, "", "bad --header: OTEL_EXPORTER_OTLP_HEADERS holds a pair that is not name=value", nil, nil, nil}},
		{"a value the environment does not URL encode fails", args{[]string{"otel"}, map[string]string{envOTLPEndpoint: "{srv}", envOTLPHeaders: "Authorization=%zz"}},
			want{1, "", "bad --header: OTEL_EXPORTER_OTLP_HEADERS: invalid URL escape", nil, nil, nil}},
		{"since after every record sends no request", args{[]string{"otel", "--endpoint", "{srv}", "--since", "2026-10-08T00:00:00Z"}, nil},
			want{0, "exported 0 runs, 0 verdicts and 0 outcomes\n", "", nil, nil, nil}},
		{"a since that is no RFC3339 time fails", args{[]string{"otel", "--endpoint", "{srv}", "--since", "yesterday"}, nil},
			want{1, "", `parsing time "yesterday"`, nil, nil, nil}},
		{"no endpoint anywhere fails", args{[]string{"otel"}, nil}, want{1, "", "--endpoint is required", nil, nil, nil}},
		{"a header flag without = fails", args{[]string{"otel", "--endpoint", "{srv}", "--header", "Authorization"}, nil},
			want{1, "", `bad --header: "Authorization" is not Name=value`, nil, nil, nil}},
		{"an unknown flag fails", args{[]string{"otel", "--bogus"}, nil}, want{1, "", "flag provided but not defined: -bogus", nil, nil, nil}},
		{"another target is unknown", args{[]string{"zipkin"}, nil}, want{1, "", "unknown action", nil, nil, nil}},
		{"no target fails", args{nil, nil}, want{1, "", "an action is required", nil, nil, nil}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := &otlpCollector{}
			srv := httptest.NewServer(c)
			t.Cleanup(srv.Close)
			records, home := exportFixture(t), t.TempDir()
			args := make([]string, 0, len(tc.args.args))
			for _, a := range tc.args.args {
				args = append(args, strings.ReplaceAll(a, "{srv}", srv.URL))
			}
			getenv := func(k string) string {
				base := map[string]string{envRecordDir: records, "HOME": home}[k]
				return strings.ReplaceAll(cmp.Or(base, tc.args.env[k]), "{srv}", srv.URL)
			}
			var stdout, stderr bytes.Buffer

			code := runExport(args, getenv, func() time.Time { return exportAt.Add(time.Hour) }, &stdout, &stderr)

			assert.Equal(t, tc.want.code, code, stderr.String())
			assert.Equal(t, tc.want.stdout, stdout.String())
			assert.Contains(t, stderr.String(), tc.want.stderr)
			assert.NotContains(t, stderr.String(), "Basic")
			c.mu.Lock()
			defer c.mu.Unlock()
			assert.Equal(t, tc.want.paths, c.paths)
			assert.Equal(t, tc.want.types, c.types)
			assert.Equal(t, tc.want.auths, c.auths)
		})
	}
}

// An export that fails names neither the token of the endpoint query nor a header value
// 1. a collector that refuses the logs fails it after the traces went out
// 2. a collector that redirects fails it so neither the records nor the headers reach the place it names
// 3. records that do not read fail it before anything is sent
func TestRunExportOtelRefused(t *testing.T) {
	type args struct {
		// {srv} stands for the URL of the collector
		flags []string
		// The status the collector answers per path and 200 for any other
		// The Location of every answer points at another server
		status map[string]int
		// Files of the record dir before the export by name and a directory for a name ending in /
		files map[string]string
	}
	type want struct {
		code   int
		stderr string
		// The paths the collector saw
		paths []string
	}
	plain := []string{"otel", "--header", "X-Key=k1"}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a collector that refuses the logs fails after the traces went out", args{plain, map[string]int{"/v1/logs": http.StatusBadRequest}, nil},
			want{1, "/v1/logs answered 400", []string{"/v1/traces", "/v1/logs"}}},
		{
			"a collector that redirects fails without following it",
			args{[]string{"otel", "--endpoint", "{srv}/v1/traces?token=t1", "--header", "X-Key=k1"},
				map[string]int{"/v1/traces": http.StatusTemporaryRedirect}, nil},
			want{1, "answered 307", []string{"/v1/traces"}},
		},
		{"a broken run line fails before anything is sent", args{plain, nil, map[string]string{"traces.jsonl": "not json\n"}},
			want{1, "nodloop export: ", nil}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var mu sync.Mutex
			var reached bool
			elsewhere := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				reached = true
			}))
			t.Cleanup(elsewhere.Close)
			var paths []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				paths = append(paths, r.URL.Path)
				w.Header().Set("Location", elsewhere.URL+r.URL.Path)
				w.WriteHeader(cmp.Or(tc.args.status[r.URL.Path], http.StatusOK))
			}))
			t.Cleanup(srv.Close)
			records := exportFixture(t)
			getenv := func(k string) string { return map[string]string{envRecordDir: records, envOTLPEndpoint: srv.URL}[k] }
			for name, content := range tc.args.files {
				exportRecordFile(t, records, name, content)
			}
			flags := make([]string, 0, len(tc.args.flags))
			for _, f := range tc.args.flags {
				flags = append(flags, strings.ReplaceAll(f, "{srv}", srv.URL))
			}
			var stderr bytes.Buffer

			code := runExport(flags, getenv, func() time.Time { return exportAt.Add(time.Hour) }, &bytes.Buffer{}, &stderr)

			assert.Equal(t, tc.want.code, code)
			assert.Contains(t, stderr.String(), tc.want.stderr)
			assert.NotRegexp(t, "t1|k1", stderr.String())
			mu.Lock()
			defer mu.Unlock()
			assert.Equal(t, tc.want.paths, paths)
			assert.False(t, reached)
		})
	}
}

// Appends the content to the file of the record dir
func exportRecordFile(t *testing.T, records, name, content string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(records, name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = f.WriteString(content)
	require.NoError(t, err)
	require.NoError(t, f.Close())
}
