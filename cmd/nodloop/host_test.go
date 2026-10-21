package main

import (
	"bytes"
	"encoding/json"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// The host joins the labels of the place without changing them and a place outside any directory still carries it
func TestHostLabels(t *testing.T) {
	type args struct {
		host  host
		place trace.Labels
	}
	tcs := []struct {
		name string
		args args
		want trace.Labels
	}{
		{"a repository", args{hostCodex, trace.Labels{"repo": {"o"}, "dir": {"."}}}, trace.Labels{"repo": {"o"}, "dir": {"."}, "host": {"codex"}}},
		{"no place", args{hostCursor, nil}, trace.Labels{"host": {"cursor"}}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			before := maps.Clone(tc.args.place)

			got := tc.args.host.labels(tc.args.place)

			assert.Equal(t, tc.want, got)
			assert.Equal(t, before, tc.args.place)
		})
	}
}

// Each host input is read into the fields the hooks read and an input of the wrong shape reads as none
func TestHostInput(t *testing.T) {
	type args struct {
		host  host
		stdin string
	}
	type want struct {
		input hookInput
		fails bool
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"Codex reads as Claude Code", args{hostCodex, "codex_start.json"}, want{input: hookInput{SessionID: "s1", Cwd: "/r", Source: "compact"}}},
		{"Cursor takes the conversation and the first root", args{hostCursor, "cursor_start.json"}, want{input: hookInput{SessionID: "s1", Cwd: "/r"}}},
		{"Cursor without a root has no place", args{hostCursor, `{"conversation_id":"s1","workspace_roots":[]}`}, want{input: hookInput{SessionID: "s1"}}},
		{
			"a Cursor followup is continued",
			args{hostCursor, "cursor_stop_followup.json"},
			want{input: hookInput{SessionID: "s1", Cwd: "/r", StopHookActive: true}},
		},
		{
			"an aborted Cursor loop is continued",
			args{hostCursor, "cursor_stop_aborted.json"},
			want{input: hookInput{SessionID: "s1", Cwd: "/r", StopHookActive: true}},
		},
		{"Copilot reads camel case", args{hostCopilot, "copilot_start.json"}, want{input: hookInput{SessionID: "s1", Cwd: "/r", Source: "new"}}},
		{"a continued Copilot stop", args{hostCopilot, "copilot_stop_active.json"}, want{input: hookInput{SessionID: "s1", Cwd: "/r", StopHookActive: true}}},
		{"a broken Codex input reads as none", args{hostCodex, `{"session_id":1}`}, want{fails: true}},
		{"a broken Cursor input reads as none", args{hostCursor, `{"workspace_roots":"x"}`}, want{fails: true}},
		{"a broken Copilot input reads as none", args{hostCopilot, `{"sessionId":1}`}, want{fails: true}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := tc.args.host.input(strings.NewReader(hookFixture(t, tc.args.stdin, "/r")))

			assert.Equal(t, tc.want.fails, err != nil, err)
			assert.Equal(t, tc.want.input, got)
		})
	}
}

// The hooks that add items answer in the shape of each host and refuse a hook the host never runs
func TestRunHookHosts(t *testing.T) {
	type args struct {
		hook []string
		// A hook fixture name or the input as written
		stdin string
	}
	type want struct {
		// The answer with {items} for the items as a JSON string and empty for none
		answer string
		stderr string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"a Codex compact adds the items",
			args{[]string{"start", "--host", "codex"}, "codex_start.json"},
			want{answer: `{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":{items}}}`},
		},
		{"a Codex startup adds nothing since the first prompt does", args{[]string{"start", "--host", "codex"}, "codex_startup.json"}, want{}},
		{
			"a Codex prompt adds the items",
			args{[]string{"prompt", "--host", "codex"}, "codex_prompt.json"},
			want{answer: `{"hookSpecificOutput":{"hookEventName":"UserPromptSubmit","additionalContext":{items}}}`},
		},
		{
			"a Codex subagent gets the items",
			args{[]string{"subagent", "--host", "codex"}, "codex_subagent.json"},
			want{answer: `{"hookSpecificOutput":{"hookEventName":"SubagentStart","additionalContext":{items}}}`},
		},
		{"the wake of an earlier release is no hook", args{[]string{"wake"}, "codex_stop.json"}, want{stderr: "unknown action: claude-code runs no hook wake"}},
		{
			"a Cursor session start adds the items to the conversation",
			args{[]string{"start", "--host", "cursor"}, "cursor_start.json"},
			want{answer: `{"additional_context":{items}}`},
		},
		{"Cursor runs no prompt hook", args{[]string{"prompt", "--host", "cursor"}, "cursor_start.json"}, want{stderr: "cursor runs no hook prompt"}},
		{
			"a Copilot session start adds the items",
			args{[]string{"start", "--host", "copilot"}, "copilot_start.json"},
			want{answer: `{"additionalContext":{items}}`},
		},
		{"Copilot runs no subagent hook", args{[]string{"subagent", "--host", "copilot"}, "copilot_start.json"}, want{stderr: "copilot runs no hook subagent"}},
		{"a hook without an action does nothing", args{nil, "{}"}, want{stderr: "an action is required"}},
		{"a broken Cursor input does nothing", args{[]string{"start", "--host", "cursor"}, `{"workspace_roots":"x"}`}, want{stderr: "cannot unmarshal"}},
		{"a broken Copilot input does nothing", args{[]string{"start", "--host", "copilot"}, `{"sessionId":1}`}, want{stderr: "cannot unmarshal"}},
	}
	items, err := json.Marshal(knowledge.PromptLead + "- [git-c v1 judgment] use git -C\n")
	require.NoError(t, err)
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, records, repo := hookHome(t, "use git -C")
			getenv := envOf(map[string]string{"HOME": home, envRecordDir: records})
			stdin := strings.NewReader(hookFixture(t, tc.args.stdin, repo))
			var stdout, stderr bytes.Buffer

			code := runHook(tc.args.hook, getenv, noStart, func() time.Time { return hostNow }, stdin, &stdout, &stderr)
			want := strings.ReplaceAll(tc.want.answer, "{items}", string(items))

			assert.Equal(t, 0, code)
			assert.Contains(t, stderr.String(), tc.want.stderr)
			assert.Equal(t, jsonValue(t, want), jsonValue(t, stdout.String()))
		})
	}
}

// A host whose replies code cannot read is shown no question and the question stays due for the next Claude Code session
// 1. no expose is recorded so the stop spends no round of the question
// 2. Codex records its run and Cursor and Copilot record none
func TestRunHookStopQuestionHosts(t *testing.T) {
	type args struct {
		host string
		// A hook fixture of session s1
		stop string
	}
	tcs := []struct {
		name string
		args args
		// The runs of the stop beside the three of the bench and the one of the Claude Code stop
		want int
	}{
		{"Codex records its run and asks nothing", args{"codex", "codex_stop.json"}, 1},
		{"Cursor asks nothing", args{"cursor", "cursor_stop.json"}, 0},
		{"Copilot asks nothing", args{"copilot", "copilot_stop.json"}, 0},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := newQuestionBench(t, "")

			shown, shownErr := b.hook(t, "stop", tc.args.host, tc.args.stop, b.svc, noStart)
			exposes := len(b.traces(t, trace.NameExpose))
			later, laterErr := b.hook(t, "stop", "claude-code", strings.Replace(questionStop, "s1", "s2", 1), b.svc, noStart)

			text, _ := stopAnswer(t, later)
			expose, _ := b.exposed(t)
			assert.Empty(t, shownErr+laterErr)
			assert.Empty(t, shown)
			assert.Zero(t, exposes)
			assert.True(t, strings.HasPrefix(text, wideQuestion), text)
			assert.Equal(t, "s2", expose.SessionID)
			assert.Len(t, b.traces(t, trace.NameRun), tc.want+4)
		})
	}
}

// A time after the records hookHome writes
var hostNow = time.Date(2026, 10, 2, 1, 0, 0, 0, time.UTC)

// The decoded JSON value and nil for no text
func jsonValue(t *testing.T, text string) any {
	t.Helper()
	if text == "" {
		return nil
	}
	var v any
	require.NoError(t, json.Unmarshal([]byte(text), &v), text)
	return v
}
