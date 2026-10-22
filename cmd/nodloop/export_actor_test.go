package main

import (
	"bytes"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

func TestRunExportOutcomeActor(t *testing.T) {
	type args struct {
		actor string
		proof trace.Proof
	}
	type want struct {
		actor            string
		annotator, proof any
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"legacy name is not human", args{"", trace.ProofNone}, want{"unknown", nil, nil}},
		{"code check is code", args{"code", atTerminal}, want{"code", "CODE", nil}},
		{"model check is model", args{"model", trace.ProofNone}, want{"model", "LLM", nil}},
		{"person claim keeps proof separate", args{"person", trace.ProofNone}, want{"person", "HUMAN", "none"}},
		{"terminal proves a person", args{"", atTerminal}, want{"person", "HUMAN", "terminal"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, records := t.TempDir(), t.TempDir()
			getenv := envOf(map[string]string{"HOME": home, envRecordDir: records})
			now := func() time.Time { return exportAt }
			output := filepath.Join(home, "output.json")
			require.NoError(t, os.WriteFile(output, []byte(`"answer"`), 0600))
			var id, stderr bytes.Buffer
			require.Equal(t, 0, runRun([]string{"record", "--producer", "acme", "--output", output}, getenv, now, &id, &stderr))
			run := strings.TrimSpace(id.String())
			flags := []string{"outcome", "--trace", run, "--result", "confirmed", "--reviewer", "ann", "--actor", tc.args.actor}
			flags = append(flags, "--note", "private observation")
			require.Equal(t, 0, runFeedback(flags, getenv, now, tc.args.proof, &bytes.Buffer{}, &stderr), stderr.String())
			collector := &curateCollector{}
			srv := httptest.NewServer(collector)
			defer srv.Close()
			flags = []string{"otel", "--endpoint", srv.URL, "--protocol", "http/json"}
			require.Equal(t, 0, runExport(flags, getenv, now, &bytes.Buffer{}, &stderr), stderr.String())
			collector.mu.Lock()
			var attrs map[string]any
			for _, span := range collector.spans {
				if span["name"] == "evaluate nodloop.outcome" {
					attrs = otlpAttributes(span)
				}
			}
			collector.mu.Unlock()
			require.NotNil(t, attrs)
			actor, _ := attrs["nodloop.actor"].(string)
			assert.Equal(t, tc.want, want{actor, attrs["annotation.annotator_kind"], attrs["nodloop.proof"]})
			assert.NotContains(t, attrs, "annotation.explanation")
			assert.NotContains(t, attrs, "gen_ai.evaluation.explanation")
		})
	}
}
