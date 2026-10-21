package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

func TestRunSessionCapturePrimaryReplies(t *testing.T) {
	type args struct {
		flag     string
		lineType string
	}
	type want struct {
		answers int
		status  string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"visible main click proves the reply", args{}, want{1, "approved person"}},
		{"meta call proves no reply", args{"isMeta", "assistant"}, want{0, "candidate"}},
		{"sidechain call proves no reply", args{"isSidechain", "assistant"}, want{0, "candidate"}},
		{"summary call proves no reply", args{"isCompactSummary", "assistant"}, want{0, "candidate"}},
		{"hidden call proves no reply", args{"isVisibleInTranscriptOnly", "assistant"}, want{0, "candidate"}},
		{"meta result proves no reply", args{"isMeta", "user"}, want{0, "candidate"}},
		{"sidechain result proves no reply", args{"isSidechain", "user"}, want{0, "candidate"}},
		{"summary result proves no reply", args{"isCompactSummary", "user"}, want{0, "candidate"}},
		{"hidden result proves no reply", args{"isVisibleInTranscriptOnly", "user"}, want{0, "candidate"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := newCaptureBench(t, []captureStop{{after: 10 * time.Second}, {after: 40 * time.Second}}, "")
			b.asked(t, askuserShown)
			path := b.transcript(t, "askuser.jsonl")
			content, err := os.ReadFile(path)
			require.NoError(t, err)
			lines := strings.Split(strings.TrimSpace(string(content)), "\n")
			for i, line := range lines {
				var e map[string]any
				require.NoError(t, json.Unmarshal([]byte(line), &e))
				if e["type"] == tc.args.lineType && tc.args.flag != "" {
					e[tc.args.flag] = true
					encoded, err := json.Marshal(e)
					require.NoError(t, err)
					lines[i] = string(encoded)
				}
			}
			require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600))
			client, calls := captureClient(t, sureNeither, nil)

			code, _, stderr := b.capture(t, client, path, captureDay.Add(80*time.Second))

			require.Equal(t, 0, code, stderr)
			_, out := b.captured(t)
			status, _ := b.decided(t)
			assert.Equal(t, tc.want, want{out.Answers, status})
			assert.Len(t, b.traces(t, trace.NameAnswer), tc.want.answers)
			assert.Empty(t, b.verdicts(t))
			assert.Zero(t, calls.reaction)
		})
	}
}
