package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/trace"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
)

func TestRunSessionCaptureTurnBoundary(t *testing.T) {
	tcs := []struct {
		name string
		turn time.Duration
		want int
	}{
		{"a completed answer receives the reaction", 5 * time.Second, 1},
		{"a missing stop blocks an older answer", 20 * time.Second, 0},
		{"a turn after the message does not block its answer", 65 * time.Second, 1},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := newCaptureBench(t, twoStops, "")
			store, err := tracefile.New(b.records)
			require.NoError(t, err)
			at := captureDay.Add(tc.turn)
			tr := trace.Trace{ID: trace.NewID(at), Name: trace.NameTurn, SessionID: "s1", Time: at, Input: json.RawMessage(`{}`)}
			require.NoError(t, store.Append(context.Background(), tr.Record()))
			client, calls := captureClient(t, sureApprove, nil)
			path := b.transcript(t, "approval.jsonl")

			code, _, stderr := b.capture(t, client, path, captureDay.Add(80*time.Second))
			require.Equal(t, 0, code, stderr)
			assert.Len(t, b.verdicts(t), tc.want)
			assert.Equal(t, tc.want, calls.reaction)
			code, _, stderr = b.capture(t, client, path, captureDay.Add(90*time.Second))
			require.Equal(t, 0, code, stderr)
			assert.Len(t, b.verdicts(t), tc.want)
			assert.Equal(t, tc.want, calls.reaction)
		})
	}
}
