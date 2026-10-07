package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/trace"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
)

// Every record report prints as text or as one JSON value from records holding one replay
func TestRunReportRecords(t *testing.T) {
	at := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	type args struct {
		args []string
		// A line that fails to decode is appended to traces.jsonl
		corrupt bool
	}
	type want struct {
		code   int
		stdout string
		stderr string
	}
	totals := `{"totals":{"runs":0,"judged":0,"inferred":0,"corrected":0,"waiting":0,"approved":0},"scopes":[],"drafts":[],"items":[]}` + "\n"
	arms := `[{"arm":"applied","runs":0,"judged":0,"corrected":0,"same_reason":0},` +
		`{"arm":"withheld","runs":0,"judged":0,"corrected":0,"same_reason":0}]` + "\n"
	replays := `[{"id":"git-c","version":1,"passed":true,"corrected":1,"missed":0,"approved":0,"overreach":0,"time":"2026-10-07T12:00:00Z"}]` + "\n"
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"loop as JSON", args{[]string{"loop", "--json"}, false}, want{0, totals, ""}},
		{"extract as JSON", args{[]string{"extract", "--json"}, false}, want{0, "[]\n", ""}},
		{"critic as JSON", args{[]string{"critic", "--json"}, false}, want{0, "[]\n", ""}},
		{"effect as JSON", args{[]string{"effect", "--json"}, false}, want{0, arms, ""}},
		{"replay as JSON", args{[]string{"replay", "--json"}, false}, want{0, replays, ""}},
		{"replay as text", args{[]string{"replay"}, false}, want{0, "replay\tgit-c\tv1\tpassed\tmissed 0 of 1\toverreach 0 of 0\t2026-10-07T12:00:00Z\n", ""}},
		{"a report that cannot read its records fails", args{[]string{"replay", "--json"}, true}, want{1, "", "nodloop report: traces.jsonl has corrupt lines: byte 326: "}},
		{"a text report that cannot read its records fails", args{[]string{"replay"}, true}, want{1, "", "nodloop report: traces.jsonl has corrupt lines: byte 326: "}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			records := t.TempDir()
			store, err := tracefile.New(records)
			require.NoError(t, err)
			require.NoError(t, store.Append(context.Background(), trace.Trace{
				ID: "replay-1", Name: trace.NameReplay, Subject: "git-c", Time: at,
				Output: json.RawMessage(`{"id":"git-c","version":1,"cases":[{"run":"r","expect":"breaks","breaks":true,"why":"w"}],"missed":0,"overreach":0}`),
			}))
			if tc.args.corrupt {
				f, err := os.OpenFile(filepath.Join(records, "traces.jsonl"), os.O_APPEND|os.O_WRONLY, 0o600)
				require.NoError(t, err)
				_, err = f.WriteString("not json\n")
				require.NoError(t, err)
				require.NoError(t, f.Close())
			}
			getenv := func(k string) string { return map[string]string{envRecordDir: records}[k] }
			var stdout, stderr bytes.Buffer

			code := runReport(tc.args.args, getenv, func() time.Time { return at }, &stdout, &stderr)

			assert.Equal(t, tc.want.code, code, stderr.String())
			assert.Equal(t, tc.want.stdout, stdout.String())
			assert.Contains(t, stderr.String(), tc.want.stderr)
		})
	}
}
