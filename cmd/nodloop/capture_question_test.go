package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/textkey"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

func preparedQuestion(t *testing.T, b questionBench) curate.Question {
	t.Helper()
	a := app{cfg: config{recordDir: b.records}, now: b.now}
	ledger, err := a.ledger()
	require.NoError(t, err)
	all, err := ledger.All(context.Background())
	require.NoError(t, err)
	mark, err := a.mark()
	require.NoError(t, err)
	key, err := textkey.Load(b.records)
	require.NoError(t, err)
	iv, err := a.intervener()
	require.NoError(t, err)
	shown, err := iv.Prepare(context.Background(), curate.Asking{Session: "s1", All: all, Mark: mark, Seal: key.Sum})
	require.NoError(t, err)
	require.NotEmpty(t, shown.Question.Key)
	return shown.Question
}

func TestRunSessionCaptureQuestion(t *testing.T) {
	type args struct {
		altered, description, key, partial, answer, early bool
	}
	type want struct {
		exposes, answers int
		status           string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"an observed unanswered call is exposed once", args{}, want{1, 0, "candidate"}},
		{"a matching click confirms the requested proposal", args{answer: true}, want{1, 1, "approved"}},
		{"changed question wording proves no exposure", args{altered: true}, want{0, 0, "candidate"}},
		{"changed option descriptions prove no exposure", args{description: true}, want{0, 0, "candidate"}},
		{"an unknown key proves no exposure", args{key: true}, want{0, 0, "candidate"}},
		{"a partial tool call waits for its whole line", args{partial: true}, want{0, 0, "candidate"}},
		{"a call before any run of the session proves no exposure", args{early: true}, want{0, 0, "candidate"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := newQuestionBench(t, "manual")
			proposeQuestion(t, b)
			out, failed := b.hook(t, "stop", "claude-code", questionStop, b.acme, noStart)
			require.Empty(t, failed)
			require.NotEmpty(t, out)
			q := preparedQuestion(t, b)
			call := askQuestion{Header: curate.AskHeader + q.Key,
				Question: strings.TrimSuffix(q.Text(), "\nReply yes or notyet"),
				Options:  []askOption{{"yes", "Go ahead"}, {"notyet", "Leave everything as it is"}}}
			if tc.args.altered {
				call.Question = "Apply a different change"
			}
			if tc.args.description {
				call.Options[0].Description = "Choose a different effect"
			}
			if tc.args.key {
				call.Header = curate.AskHeader + "0000"
			}
			at := b.now()
			if tc.args.early {
				at = at.Add(-24 * time.Hour)
			}
			body := questionTranscript(t, call, at, tc.args.answer)
			if tc.args.partial {
				body = strings.TrimSuffix(body, "\n")
			}
			path := filepath.Join(b.getenv("HOME"), ".claude", "projects", "acme", "s1.jsonl")
			require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
			var stdout, stderr bytes.Buffer
			for range 2 {
				code := runSession([]string{"capture", "--session", "s1", "--transcript", path},
					b.getenv, curateClient(t), b.now, noStart, &stdout, &stderr)
				require.Equal(t, 0, code, stderr.String())
			}
			a := app{cfg: config{recordDir: b.records}, now: b.now}
			ledger, err := a.ledger()
			require.NoError(t, err)
			all, err := ledger.All(context.Background())
			require.NoError(t, err)
			item, err := all.Version("k-add", 1)
			require.NoError(t, err)
			assert.Equal(t, tc.want, want{len(b.traces(t, trace.NameExpose)), len(b.traces(t, trace.NameAnswer)), string(item.Status)})
			for _, exposure := range b.traces(t, trace.NameExpose) {
				assert.NotContains(t, string(exposure.Input), call.Question)
				assert.NotContains(t, string(exposure.Input), "Name the failing test first")
			}
		})
	}
}

func questionTranscript(t *testing.T, q askQuestion, at time.Time, answer bool) string {
	t.Helper()
	call := map[string]any{"type": "assistant", "timestamp": at,
		"message": map[string]any{"content": []any{map[string]any{"type": "tool_use", "id": "call1", "name": "AskUserQuestion",
			"input": map[string]any{"questions": []askQuestion{q}}}}}}
	line, err := json.Marshal(call)
	require.NoError(t, err)
	out := string(line) + "\n"
	if !answer {
		return out
	}
	result := map[string]any{"type": "user", "timestamp": at.Add(time.Second),
		"message":       map[string]any{"content": []any{map[string]any{"type": "tool_result", "tool_use_id": "call1"}}},
		"toolUseResult": map[string]any{"answers": map[string]string{q.Question: "yes"}}}
	line, err = json.Marshal(result)
	require.NoError(t, err)
	return out + string(line) + "\n"
}
