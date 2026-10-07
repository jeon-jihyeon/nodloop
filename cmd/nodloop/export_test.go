package main

import (
	"bytes"
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
	var mu sync.Mutex
	var bodies []string
	var auth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies, auth = append(bodies, r.URL.Path+" "+string(body)), append(auth, r.Header.Get("Authorization"))
		mu.Unlock()
	}))
	t.Cleanup(srv.Close)
	type args struct {
		args []string
		env  map[string]string
	}
	type want struct {
		code   int
		stdout string
		stderr string
		// The path the collector saw and its Authorization header
		path string
		auth string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"the flags name the endpoint and the headers",
			args{[]string{"otel", "--endpoint", srv.URL + "/v1/traces", "--header", "Authorization=Basic flag"}, nil},
			want{0, "exported 1 runs and 1 verdicts\n", "", "/v1/traces", "Basic flag"},
		},
		{
			"the OTLP environment fills what the flags leave out",
			args{[]string{"otel"}, map[string]string{envOTLPEndpoint: srv.URL + "/otel/", envOTLPHeaders: "Authorization=Basic%20env"}},
			want{0, "exported 1 runs and 1 verdicts\n", "", "/otel/v1/traces", "Basic env"},
		},
		{
			"a header of the flags wins over the same name in the environment",
			args{[]string{"otel", "--endpoint", srv.URL + "/v1/traces", "--header", "Authorization=Basic flag"}, map[string]string{envOTLPHeaders: "Authorization=Basic%20env"}},
			want{0, "exported 1 runs and 1 verdicts\n", "", "/v1/traces", "Basic flag"},
		},
		{"a pair without = in the environment fails", args{[]string{"otel", "--endpoint", srv.URL}, map[string]string{envOTLPHeaders: "Authorization"}},
			want{1, "", "is not name=value", "", ""}},
		{"since after every run sends no request", args{[]string{"otel", "--endpoint", srv.URL, "--since", "2026-10-08T00:00:00Z"}, nil}, want{0, "exported 0 runs and 0 verdicts\n", "", "", ""}},
		{"no endpoint fails", args{[]string{"otel"}, nil}, want{1, "", "--endpoint is required", "", ""}},
		{"another target is unknown", args{[]string{"zipkin"}, nil}, want{1, "", "unknown action", "", ""}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			mu.Lock()
			bodies, auth = nil, nil
			mu.Unlock()
			getenv := func(k string) string {
				if v, ok := tc.args.env[k]; ok {
					return v
				}
				return base(k)
			}
			var stdout, stderr bytes.Buffer

			code := runExport(tc.args.args, getenv, time.Now, &stdout, &stderr)

			assert.Equal(t, tc.want.code, code, stderr.String())
			assert.Equal(t, tc.want.stdout, stdout.String())
			assert.Contains(t, stderr.String(), tc.want.stderr)
			if tc.want.path == "" {
				assert.Empty(t, bodies)
				return
			}
			require.Len(t, bodies, 1)
			path, body, _ := strings.Cut(bodies[0], " ")
			assert.Equal(t, tc.want.path, path)
			assert.Equal(t, tc.want.auth, auth[0])
			assert.True(t, json.Valid([]byte(body)))
		})
	}
}
