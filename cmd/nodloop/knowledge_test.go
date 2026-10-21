package main

import (
	"bytes"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// The actions that call a model start the OTLP export when they end and only while config.json holds an otel target
// An action that ended in an error starts it too since its traces wait to be sent
func TestRunKnowledgeStartingExport(t *testing.T) {
	type args struct {
		action string
		saved  string
	}
	type want struct {
		code int
		// How often the export started
		exports int
	}
	otel := `{"otel":{"endpoint":"http://192.0.2.1:4318"}}`
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"an extract starts the export", args{"extract", otel}, want{1, 1}},
		{"a consolidate starts the export", args{"consolidate", otel}, want{0, 1}},
		{"a curate starts the export", args{"curate", otel}, want{0, 1}},
		{"no otel target starts nothing", args{"consolidate", ""}, want{0, 0}},
		{"an action that calls no model starts nothing", args{"list", otel}, want{0, 0}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, records := t.TempDir(), t.TempDir()
			hookSaveConfig(t, home, tc.args.saved)
			getenv := envOf(map[string]string{"HOME": home, envRecordDir: records})
			var started [][]string
			start := func(args []string) error {
				started = append(started, args)
				return nil
			}
			var stderr bytes.Buffer

			code := runKnowledgeStarting([]string{tc.args.action}, getenv, nil, func() time.Time { return curateNow }, trace.ProofNone, start,
				&bytes.Buffer{}, &stderr)

			export := []string{"export", "otel", "--pending", "--record-dir", records}
			assert.Equal(t, tc.want.code, code, stderr.String())
			assert.ElementsMatch(t, slices.Repeat([][]string{export}, tc.want.exports), started)
		})
	}
}
