package main

import (
	"bytes"
	"cmp"
	"encoding/json"
	"io"
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
)

func TestRunExportOtel(t *testing.T) {
	records := t.TempDir()
	at := time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)
	out := filepath.Join(t.TempDir(), "out.txt")
	require.NoError(t, os.WriteFile(out, []byte("steps"), 0o600))
	base := func(k string) string { return map[string]string{envRecordDir: records}[k] }
	var id bytes.Buffer
	require.Equal(t, 0, runRun([]string{"record", "--producer", "bot", "--output", out}, base, func() time.Time { return at }, &id, &bytes.Buffer{}))
	run := strings.TrimSpace(id.String())
	require.Equal(t, 0, runFeedback([]string{"add", "--trace", run, "--verdict", "reject", "--reason", "no window"}, base, func() time.Time { return at.Add(time.Minute) }, &bytes.Buffer{}, &bytes.Buffer{}))
	type args struct {
		// {srv} stands for the URL of the collector of the row
		args []string
		env  map[string]string
	}
	type want struct {
		code   int
		stdout string
		stderr string
		// The paths the collector saw and the Authorization header of each request
		paths []string
		auths []string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"the flags name the endpoint and the headers",
			args{[]string{"otel", "--endpoint", "{srv}/v1/traces", "--header", "Authorization=Basic flag"}, nil},
			want{0, "exported 1 runs and 1 verdicts\n", "", []string{"/v1/traces"}, []string{"Basic flag"}},
		},
		{
			"the OTLP environment fills what the flags leave out",
			args{[]string{"otel"}, map[string]string{envOTLPEndpoint: "{srv}/otel/", envOTLPHeaders: "Authorization=Basic%20env"}},
			want{0, "exported 1 runs and 1 verdicts\n", "", []string{"/otel/v1/traces"}, []string{"Basic env"}},
		},
		{
			"a header of the flags wins over the same name in the environment",
			args{[]string{"otel", "--endpoint", "{srv}/v1/traces", "--header", "Authorization=Basic flag"}, map[string]string{envOTLPHeaders: "Authorization=Basic%20env"}},
			want{0, "exported 1 runs and 1 verdicts\n", "", []string{"/v1/traces"}, []string{"Basic flag"}},
		},
		{"a pair without = in the environment fails", args{[]string{"otel", "--endpoint", "{srv}"}, map[string]string{envOTLPHeaders: "Authorization"}},
			want{1, "", "is not name=value", nil, nil}},
		{"a value the environment does not URL encode fails", args{[]string{"otel", "--endpoint", "{srv}"}, map[string]string{envOTLPHeaders: "Authorization=%zz"}},
			want{1, "", "bad --header: OTEL_EXPORTER_OTLP_HEADERS: invalid URL escape", nil, nil}},
		{"since after every run sends no request", args{[]string{"otel", "--endpoint", "{srv}", "--since", "2026-10-08T00:00:00Z"}, nil},
			want{0, "exported 0 runs and 0 verdicts\n", "", nil, nil}},
		{"a since that is no RFC3339 time fails", args{[]string{"otel", "--endpoint", "{srv}", "--since", "yesterday"}, nil},
			want{1, "", `parsing time "yesterday"`, nil, nil}},
		{"no endpoint fails", args{[]string{"otel"}, nil}, want{1, "", "--endpoint is required", nil, nil}},
		{"another target is unknown", args{[]string{"zipkin"}, nil}, want{1, "", "unknown action", nil, nil}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var mu sync.Mutex
			var paths, auths, bodies []string
			srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				mu.Lock()
				defer mu.Unlock()
				paths, auths, bodies = append(paths, r.URL.Path), append(auths, r.Header.Get("Authorization")), append(bodies, string(body))
			}))
			t.Cleanup(srv.Close)
			args := make([]string, 0, len(tc.args.args))
			for _, a := range tc.args.args {
				args = append(args, strings.ReplaceAll(a, "{srv}", srv.URL))
			}
			getenv := func(k string) string {
				return strings.ReplaceAll(cmp.Or(tc.args.env[k], base(k)), "{srv}", srv.URL)
			}
			var stdout, stderr bytes.Buffer

			code := runExport(args, getenv, func() time.Time { return at.Add(time.Hour) }, &stdout, &stderr)

			assert.Equal(t, tc.want.code, code, stderr.String())
			assert.Equal(t, tc.want.stdout, stdout.String())
			assert.Contains(t, stderr.String(), tc.want.stderr)
			mu.Lock()
			defer mu.Unlock()
			assert.Equal(t, tc.want.paths, paths)
			assert.Equal(t, tc.want.auths, auths)
			for _, body := range bodies {
				assert.True(t, json.Valid([]byte(body)))
			}
		})
	}
}
