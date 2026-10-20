package trace_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

func TestTraceRecord(t *testing.T) {
	base := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	draw := trace.NewDraw("2026-09-21", "0.10.0+1a2b3c4d", "seed", "d1", "audit", 0.15)
	decide := trace.Trace{
		ID: "d1", Name: "decide", Ref: "c1", Time: base, Input: json.RawMessage(`{}`), Draw: &draw, Source: trace.SourcePush,
	}
	full := trace.Trace{
		ID: trace.NewID(base), Name: trace.NameRun, SessionID: "session-1", Subject: "commit", Ref: "c1", Time: base,
		Model: "sonnet", Input: json.RawMessage(`{"applied":[]}`), Output: json.RawMessage(`"out"`), Error: "timeout",
		Usage: trace.Usage{
			InputTokens: 100, OutputTokens: 20, ThinkingTokens: 7, CacheReadTokens: 5, CacheCreateTokens: 6, CostUSD: 0.01,
		},
		DurationMS: 1500, Tags: []string{"feedback:on"}, Producer: "session", Labels: trace.Labels{"repo": {"nodloop"}},
	}
	tcs := []struct {
		name string
		args trace.Trace
	}{
		{"every field", full},
		{"only the required fields", trace.Trace{ID: "t1", Name: trace.NameCheck, Time: base, Input: json.RawMessage(`{}`)}},
		{"empty labels stay empty", trace.Trace{ID: "t2", Name: trace.NameRun, Time: base, Labels: trace.Labels{}}},
		{"a decision keeps its draw and its source", decide},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model, err := json.Marshal(tc.args)
			require.NoError(t, err)
			want := fmt.Sprintf(`%s,"schema_version":%d}`, model[:len(model)-1], trace.SchemaVersion)
			got, err := json.Marshal(tc.args.Record())
			require.NoError(t, err)
			assert.Equal(t, want, string(got), "the line encodes to the bytes of the trace and the version of its schema")
			assert.Equal(t, tc.args, trace.Restore(tc.args.Record()))
		})
	}
}

func TestRestoreAll(t *testing.T) {
	at := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	older := trace.Trace{ID: "t1", Name: trace.NameRun, Time: at}
	newer := trace.Trace{ID: "t2", Name: trace.NameCheck, Time: at.Add(time.Second)}
	tcs := []struct {
		name string
		args []trace.Trace
		want trace.Traces
	}{
		{"no lines stay nil", nil, nil},
		{"lines keep the order the store listed them", []trace.Trace{newer, older}, trace.Traces{newer, older}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var lines []record.Trace
			for _, tr := range tc.args {
				lines = append(lines, tr.Record())
			}
			assert.Equal(t, tc.want, trace.RestoreAll(lines))
		})
	}
}
