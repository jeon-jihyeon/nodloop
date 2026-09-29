package file_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/evidence"
	"github.com/jeon-jihyeon/nodloop/internal/evidence/file"
)

func TestSourceMetrics(t *testing.T) {
	const header = "event_id,timestamp,metric,value\n"
	const one = header + "a,2026-09-28T00:00:00Z,requests,1\n"
	type want struct {
		metrics []string
		err     error
		text    string
	}
	tcs := []struct {
		name string
		args map[string]string
		want want
	}{
		{"metrics across events without duplicates", map[string]string{"events.csv": header +
			"a,2026-09-28T00:01:00Z,requests,1\n" +
			"b,2026-09-28T00:00:00Z,latency,1\n" +
			"a,2026-09-28T00:00:00Z,errors,1\n" +
			"b,2026-09-28T00:01:00Z,requests,2\n"},
			want{metrics: []string{"errors", "requests", "latency"}}},
		{"missing contexts allowed", map[string]string{"events.csv": one}, want{metrics: []string{"requests"}}},
		{"empty events", map[string]string{"events.csv": header}, want{}},
		{"empty events skip unrelated contexts", map[string]string{
			"events.csv": header, "contexts.csv": "broken\n"}, want{}},
		{"missing events", nil, want{err: os.ErrNotExist, text: "events.csv"}},
		{"malformed point", map[string]string{"events.csv": header + "a,bad,requests,1\n"},
			want{err: evidence.ErrMalformed, text: "events.csv line 2"}},
		{"malformed context", map[string]string{"events.csv": one, "contexts.csv": "broken\n"},
			want{err: evidence.ErrMalformed, text: "contexts.csv missing column"}},
		{"unknown context", map[string]string{"events.csv": one,
			"contexts.csv": "event_id,change_context\na,removed\n"},
			want{err: evidence.ErrUnknownContext, text: "contexts.csv line 2"}},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			for name, content := range tc.args {
				require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600))
			}
			source, err := file.New(dir)
			require.NoError(t, err)
			metrics, err := source.Metrics(ctx)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Contains(t, fmt.Sprint(err), tc.want.text)
			assert.Equal(t, tc.want.metrics, metrics)
		})
	}
}
