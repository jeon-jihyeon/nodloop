//go:build unix

package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/otel"
)

// A pending export that finds the mark held by another ends at once and sends nothing
func TestRunExportOtelPendingSkipsAHeldMark(t *testing.T) {
	tcs := []struct {
		name string
		// The flock another process holds on the mark
		args int
	}{
		{"an exclusive lock skips the export", syscall.LOCK_EX},
		{"a shared lock skips the export", syscall.LOCK_SH},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := &otlpCollector{status: http.StatusOK}
			srv := httptest.NewServer(c)
			t.Cleanup(srv.Close)
			records := exportFixture(t)
			getenv := func(k string) string { return map[string]string{envRecordDir: records, envOTLPEndpoint: srv.URL}[k] }
			held, err := os.OpenFile(filepath.Join(records, otel.MarkFile), os.O_CREATE|os.O_RDWR, 0o600)
			require.NoError(t, err)
			t.Cleanup(func() { _ = held.Close() })
			require.NoError(t, syscall.Flock(int(held.Fd()), tc.args))
			var stdout, stderr bytes.Buffer

			code := runExport([]string{"otel", "--pending"}, getenv, func() time.Time { return exportAt.Add(time.Hour) }, &stdout, &stderr)

			assert.Equal(t, 0, code, stderr.String())
			assert.Empty(t, stdout.String())
			assert.Empty(t, stderr.String())
			c.mu.Lock()
			defer c.mu.Unlock()
			assert.Empty(t, c.paths)
		})
	}
}
