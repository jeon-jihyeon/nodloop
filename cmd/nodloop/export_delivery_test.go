package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/otel"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

func TestRunExportDeliveryMark(t *testing.T) {
	type args struct {
		status int
		body   string
	}
	type want struct {
		advanced bool
		code     int
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"full", args{200, `{}`}, want{true, 0}},
		{"partial", args{200, `{"partialSuccess":{"rejectedSpans":"1"}}`}, want{true, 1}},
		{"permanent", args{400, `private`}, want{true, 1}},
		{"rate limited", args{429, `private`}, want{false, 1}},
		{"unavailable", args{503, `private`}, want{false, 1}},
		{"malformed", args{200, `private`}, want{false, 1}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.args.status)
				_, _ = w.Write([]byte(tc.args.body))
			}))
			t.Cleanup(srv.Close)
			dir := exportFixture(t)
			getenv := func(k string) string {
				if k == envRecordDir {
					return dir
				}
				return ""
			}
			var stdout, stderr bytes.Buffer
			code := runExport([]string{"otel", "--pending", "--protocol", "http/json", "--endpoint", srv.URL},
				getenv, func() time.Time { return exportAt }, &stdout, &stderr)
			assert.Equal(t, tc.want.code, code, stderr.String())
			assert.NotContains(t, stderr.String(), "private")
			b, err := os.ReadFile(filepath.Join(dir, otel.MarkFile))
			require.NoError(t, err)
			var mark otel.Mark
			if len(b) > 0 {
				require.NoError(t, json.Unmarshal(b, &mark))
			}
			assert.Equal(t, tc.want.advanced, !mark.At.IsZero())
			assert.Empty(t, mark.Completed)
			if tc.want.advanced {
				assert.NotEmpty(t, mark.After)
			} else {
				assert.Empty(t, mark.After)
			}
		})
	}
}

func TestRunExportDeliveryRestartAfterAppend(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a := app{cfg: config{recordDir: dir}, now: func() time.Time { return exportAt }}
	store, err := a.traces()
	require.NoError(t, err)
	for i := range 401 {
		r, err := trace.NewRun("bot", "", nil, nil, []byte(fmt.Sprint(i)), exportAt)
		require.NoError(t, err)
		require.NoError(t, store.Append(t.Context(), r.Record()))
	}
	var mu sync.Mutex
	var batches [][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ResourceSpans []struct {
				ScopeSpans []struct {
					Spans []gotSpan `json:"spans"`
				} `json:"scopeSpans"`
			} `json:"resourceSpans"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			w.WriteHeader(400)
			return
		}
		var ids []string
		for _, rs := range body.ResourceSpans {
			for _, ss := range rs.ScopeSpans {
				for _, s := range ss.Spans {
					ids = append(ids, s.SpanID)
				}
			}
		}
		mu.Lock()
		defer mu.Unlock()
		batches = append(batches, ids)
		switch len(batches) {
		case 1:
			_, _ = w.Write([]byte(`{"partialSuccess":{"rejectedSpans":"1"}}`))
		case 2:
			w.WriteHeader(503)
		}
	}))
	t.Cleanup(srv.Close)
	getenv := func(k string) string {
		if k == envRecordDir {
			return dir
		}
		return ""
	}
	flags := []string{"otel", "--pending", "--protocol", "http/json", "--endpoint", srv.URL}
	var out, stderr bytes.Buffer
	assert.Equal(t, 1, runExport(flags, getenv, a.now, &out, &stderr), stderr.String())
	readMark := func() otel.Mark {
		b, err := os.ReadFile(filepath.Join(dir, otel.MarkFile))
		require.NoError(t, err)
		var mark otel.Mark
		require.NoError(t, json.Unmarshal(b, &mark))
		return mark
	}
	mark := readMark()
	assert.True(t, mark.At.IsZero())
	assert.Empty(t, mark.After)
	require.Len(t, mark.Completed, 200)
	for _, key := range mark.Completed {
		assert.Regexp(t, "^[a-f0-9]{64}$", key)
	}
	r, err := trace.NewRun("bot", "", nil, nil, []byte("appended"), exportAt.Add(time.Hour))
	require.NoError(t, err)
	require.NoError(t, store.Append(t.Context(), r.Record()))
	stderr.Reset()
	assert.Equal(t, 0, runExport(flags, getenv, a.now, &out, &stderr), stderr.String())
	mark = readMark()
	assert.Equal(t, exportAt.Add(time.Hour), mark.At)
	assert.NotEmpty(t, mark.After)
	assert.Empty(t, mark.Completed)
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, batches, 4)
	for _, id := range append(slices.Clone(batches[2]), batches[3]...) {
		assert.NotContains(t, batches[0], id)
	}
}
