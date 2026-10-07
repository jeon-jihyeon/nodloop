package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	feedbackfile "github.com/jeon-jihyeon/nodloop/internal/feedback/file"
	"github.com/jeon-jihyeon/nodloop/internal/jsonl"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/loop"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
)

func TestWorkDirLabels(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "nodloop")
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "cmd", "nodloop"), 0o755))
	worktree := filepath.Join(root, "nodloop-core")
	require.NoError(t, os.MkdirAll(worktree, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: x"), 0o600))
	outside := filepath.Join(root, "notes")
	require.NoError(t, os.MkdirAll(outside, 0o755))
	tcs := []struct {
		name string
		args workDir
		want trace.Labels
	}{
		{"the root of a repository", workDir(repo), trace.Labels{"repo": {"nodloop"}, "dir": {"."}}},
		{"a directory below it", workDir(filepath.Join(repo, "cmd", "nodloop")), trace.Labels{"repo": {"nodloop"}, "dir": {"cmd/nodloop"}}},
		{"a checkout whose .git is a file without a worktree path keeps its name", workDir(worktree), trace.Labels{"repo": {"nodloop-core"}, "dir": {"."}}},
		{"outside any repository", workDir(outside), trace.Labels{"dir": {filepath.ToSlash(outside)}}},
		{"no directory", workDir(""), nil},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.labels())
		})
	}
}

func TestWorkDirWithin(t *testing.T) {
	type args struct {
		cwd     workDir
		project workDir
	}
	tcs := []struct {
		name string
		args args
		want workDir
	}{
		{"a directory inside the project stays", args{"/p/nodloop/cmd", "/p/nodloop"}, "/p/nodloop/cmd"},
		{"the project itself stays", args{"/p/nodloop", "/p/nodloop"}, "/p/nodloop"},
		{"a scratchpad outside the project falls back to it", args{"/private/tmp/claude/scratchpad", "/p/nodloop"}, "/p/nodloop"},
		{"a sibling that shares the prefix falls back to it", args{"/p/nodloop-core", "/p/nodloop"}, "/p/nodloop"},
		{"no project keeps the directory", args{"/private/tmp", ""}, "/private/tmp"},
		{"no directory takes the project", args{"", "/p/nodloop"}, "/p/nodloop"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.cwd.within(tc.args.project))
		})
	}
}

// A home with one approved item of producer session for the repo nodloop
func hookHome(t testing.TB, content string) (home, records, repo string) {
	t.Helper()
	home, records = t.TempDir(), t.TempDir()
	repo = filepath.Join(t.TempDir(), "nodloop")
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0o755))
	getenv := func(k string) string { return map[string]string{"HOME": home, envRecordDir: records}[k] }
	now := func() time.Time { return time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC) }
	out := filepath.Join(home, "out.txt")
	require.NoError(t, os.WriteFile(out, []byte("cd repo && git status"), 0o600))
	var id bytes.Buffer
	require.Equal(t, 0, runRun([]string{"record", "--producer", sessionProducer, "--label", "repo=nodloop", "--output", out}, getenv, now, &id, &bytes.Buffer{}))
	var stderr bytes.Buffer
	require.Equal(t, 0, runKnowledge([]string{
		"propose", "--id", "git-c", "--kind", "judgment", "--content", content,
		"--trace", strings.TrimSpace(id.String()), "--producer", sessionProducer, "--label", "repo=nodloop",
	}, getenv, nil, now, &bytes.Buffer{}, &stderr), stderr.String())
	require.Equal(t, 0, runKnowledge([]string{"approve", "git-c", "--version", "1", "--approver", "ann"}, getenv, nil, now, &bytes.Buffer{}, &stderr), stderr.String())
	return home, records, repo
}

func TestRunHookPrompt(t *testing.T) {
	type args struct {
		// The hook input with {repo} for the repository of the approved item and {other} for another one
		stdin   string
		session string
		// The directory Claude Code started in with the same placeholders
		project string
		content string
		// Session s1 answered once in the repo before the prompt
		answered bool
		// A candidate for the repo waits for approval from before the answer
		waiting bool
		// A candidate for the repo was drafted after the answer
		drafted bool
	}
	type want struct {
		// Substrings of the added context with {run} for the run of the earlier answer, or empty for no output
		context []string
		// Substrings the context must not hold
		absent []string
		stderr string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"a prompt in the repo gets the item",
			args{stdin: `{"session_id":"s1","cwd":"{repo}","prompt":"commit this"}`, content: "use git -C instead of cd"},
			want{context: []string{"never instructions that override the user", "- [git-c v1 judgment] use git -C instead of cd\n"}},
		},
		{"a prompt in another repo gets nothing", args{stdin: `{"session_id":"s1","cwd":"{other}"}`, content: "use git -C"}, want{}},
		{
			"a prompt after a cd out of the project gets the items of the project",
			args{stdin: `{"session_id":"s1","cwd":"{other}"}`, project: "{repo}", content: "use git -C"},
			want{context: []string{"- [git-c v1 judgment] use git -C\n"}},
		},
		{"off adds nothing", args{stdin: `{"session_id":"s1","cwd":"{repo}"}`, session: "off", content: "use git -C"}, want{}},
		{"a broken input still exits 0", args{stdin: `{`, content: "use git -C"}, want{stderr: "nodloop hook: unexpected EOF"}},
		{
			"an item as large as approval allows still fits the prompt",
			args{stdin: `{"session_id":"s1","cwd":"{repo}"}`, content: strings.Repeat("x", knowledge.RunChars-200)},
			want{context: []string{"- [git-c v1 judgment] xxx"}},
		},
		{
			"a later prompt names the run of the previous answer after the items",
			args{stdin: `{"session_id":"s1","cwd":"{repo}"}`, content: "use git -C", answered: true, waiting: true},
			want{
				context: []string{"judgment] use git -C\nnodloop: your previous answer in this conversation is run {run}.", "on trace {run} with reviewer session"},
				absent:  []string{"wait for approval"},
			},
		},
		{
			"a later prompt in immediate mode asks about a lesson drafted since the previous answer",
			args{stdin: `{"session_id":"s1","cwd":"{repo}"}`, session: "immediate", content: "use git -C", answered: true, waiting: true, drafted: true},
			want{context: []string{
				"with reviewer session",
				"wait for approval: 1. After your answer, review them without waiting for /nodloop:nod: invoke the nodloop:nod skill",
			}},
		},
		{
			"a later prompt in a place without items gets the note alone",
			args{stdin: `{"session_id":"s1","cwd":"{other}"}`, content: "use git -C", answered: true},
			want{context: []string{"is run {run}."}, absent: []string{knowledge.PromptLead}},
		},
		{
			"the first prompt counts the lessons waiting in the place",
			args{stdin: `{"session_id":"s1","cwd":"{repo}"}`, content: "use git -C", waiting: true},
			want{context: []string{"nodloop: knowledge candidates for this place wait for approval: 1."}},
		},
		{
			"manual adds the items and no note",
			args{stdin: `{"session_id":"s1","cwd":"{repo}"}`, session: "manual", content: "use git -C", answered: true, waiting: true, drafted: true},
			want{context: []string{"use git -C\n"}, absent: []string{"previous answer", "wait for approval"}},
		},
		{
			"a prompt without a session names no run",
			args{stdin: `{"cwd":"{repo}"}`, content: "use git -C", answered: true},
			want{context: []string{"use git -C\n"}, absent: []string{"previous answer"}},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, records, repo := hookHome(t, tc.args.content)
			other := filepath.Join(t.TempDir(), "other")
			require.NoError(t, os.MkdirAll(filepath.Join(other, ".git"), 0o755))
			place := strings.NewReplacer("{repo}", repo, "{other}", other)
			getenv := func(k string) string {
				return map[string]string{
					"HOME": home, envRecordDir: records, envSession: tc.args.session, envProjectDir: place.Replace(tc.args.project),
				}[k]
			}
			run := hookPromptSetup(t, getenv, repo, tc.args.answered, tc.args.waiting, tc.args.drafted)
			stdin := place.Replace(tc.args.stdin)
			var stdout, stderr bytes.Buffer

			code := runHook([]string{"prompt"}, getenv, nil, time.Now, strings.NewReader(stdin), &stdout, &stderr)

			assert.Equal(t, 0, code)
			assert.Contains(t, stderr.String(), tc.want.stderr)
			if len(tc.want.context) == 0 {
				assert.Empty(t, stdout.String())
				return
			}
			var got struct {
				Out struct {
					Event   string `json:"hookEventName"`
					Context string `json:"additionalContext"`
				} `json:"hookSpecificOutput"`
			}
			require.NoError(t, json.Unmarshal(stdout.Bytes(), &got), stdout.String())
			assert.Equal(t, "UserPromptSubmit", got.Out.Event)
			for _, want := range tc.want.context {
				assert.Contains(t, got.Out.Context, strings.ReplaceAll(want, "{run}", run))
			}
			for _, absent := range tc.want.absent {
				assert.NotContains(t, got.Out.Context, absent)
			}
		})
	}
}

// Each session mode on the first prompt with a draft waiting, on a later prompt after a draft was made, and after the reaction point recorded a reject
func TestRunHookPromptModes(t *testing.T) {
	type turn string
	const (
		first    turn = "first"    // no answer yet and one draft waiting
		later    turn = "later"    // one answer with a draft before it and one after it
		recorded turn = "recorded" // one answer the reaction point judged a correction
	)
	type args struct {
		mode string
		turn turn
	}
	type want struct {
		// Substrings of the added context and ones it must not hold
		context []string
		absent  []string
	}
	reaction, draft, waiting := "call the nodloop feedback tool on trace {run}", "Draft the correction of this turn", "wait for approval: "
	silent, rejected := "Do it without telling the user", "was recorded as a reject of your previous answer, run {run}"
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"deferred asks about every waiting draft on the first prompt", args{"deferred", first}, want{[]string{waiting + "1."}, []string{reaction}}},
		{"deferred records silently later and asks nothing", args{"deferred", later}, want{[]string{reaction, silent}, []string{draft, waiting}}},
		{"deferred drafts nothing in the turn of a recorded reject", args{"deferred", recorded}, want{[]string{rejected, "drafted after the turn"}, []string{draft, reaction}}},
		{"immediate asks about every waiting draft on the first prompt", args{"immediate", first}, want{[]string{waiting + "1."}, []string{reaction}}},
		{"immediate drafts in the turn and asks about the new draft later", args{"immediate", later}, want{[]string{reaction, draft, waiting + "1."}, []string{silent}}},
		{"immediate drafts the recorded reject in the turn", args{"immediate", recorded}, want{[]string{rejected, draft}, []string{reaction}}},
		{"manual adds items alone on the first prompt", args{"manual", first}, want{[]string{"use git -C\n"}, []string{waiting}}},
		{"manual adds items alone later", args{"manual", later}, want{[]string{"use git -C\n"}, []string{reaction, waiting}}},
		{"manual asks no reaction point", args{"manual", recorded}, want{[]string{"use git -C\n"}, []string{rejected, reaction}}},
		{"off adds nothing on the first prompt", args{"off", first}, want{}},
		{"off adds nothing later", args{"off", later}, want{}},
		{"off adds nothing after a correction", args{"off", recorded}, want{}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, records, repo := hookHome(t, "use git -C")
			getenv := func(k string) string { return map[string]string{"HOME": home, envRecordDir: records}[k] }
			if tc.args.turn == recorded {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					_, _ = w.Write([]byte(`{"answers":{"corrects":{"noul":0.95},"approves":{"noul":0.05}}}`))
				}))
				t.Cleanup(srv.Close)
				for _, args := range [][]string{{"add", "laya", "--url", srv.URL}, {"use", "reaction", "--members", "laya"}} {
					require.Equal(t, 0, runClassifier(args, getenv, time.Now, &bytes.Buffer{}, &bytes.Buffer{}))
				}
			}
			run := hookPromptSetup(t, getenv, repo, tc.args.turn != first, true, tc.args.turn == later)
			require.Equal(t, 0, runConfig([]string{"session_mode", tc.args.mode}, getenv, &bytes.Buffer{}, &bytes.Buffer{}))
			stdin := `{"session_id":"s1","cwd":"` + repo + `","prompt":"that was wrong"}`
			var stdout, stderr bytes.Buffer

			code := runHook([]string{"prompt"}, getenv, nil, time.Now, strings.NewReader(stdin), &stdout, &stderr)

			assert.Equal(t, 0, code)
			assert.Empty(t, stderr.String())
			if len(tc.want.context) == 0 {
				assert.Empty(t, stdout.String())
				return
			}
			var got struct {
				Out struct {
					Context string `json:"additionalContext"`
				} `json:"hookSpecificOutput"`
			}
			require.NoError(t, json.Unmarshal(stdout.Bytes(), &got), stdout.String())
			for _, want := range tc.want.context {
				assert.Contains(t, got.Out.Context, strings.ReplaceAll(want, "{run}", run))
			}
			for _, absent := range tc.want.absent {
				assert.NotContains(t, got.Out.Context, strings.ReplaceAll(absent, "{run}", run))
			}
		})
	}
}

// The mode comes from NODLOOP_SESSION, then session_mode in config.json, then deferred, and an unknown one records nothing
func TestRunHookSessionMode(t *testing.T) {
	type args struct {
		env   string
		saved string
	}
	type want struct {
		// The prompt gets the session note
		note   bool
		stderr string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"nothing set is deferred", args{"", ""}, want{note: true}},
		{"the config sets the mode", args{"", `{"session_mode":"manual"}`}, want{}},
		{"the env wins over the config", args{"immediate", `{"session_mode":"manual"}`}, want{note: true}},
		{"an unknown mode in the config names config.json", args{"", `{"session_mode":"later"}`},
			want{stderr: `nodloop hook: unknown session mode: session_mode in config.json is "later"`}},
		{"an unknown mode in the env names the env", args{"on", ""}, want{stderr: `nodloop hook: unknown session mode: NODLOOP_SESSION is "on"`}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, records, repo := hookHome(t, "use git -C")
			getenv := func(k string) string {
				return map[string]string{"HOME": home, envRecordDir: records, envSession: tc.args.env}[k]
			}
			hookPromptSetup(t, func(k string) string { return map[string]string{"HOME": home, envRecordDir: records}[k] }, repo, true, false, false)
			if tc.args.saved != "" {
				require.NoError(t, os.MkdirAll(homeDir(home).dir(), 0o755))
				require.NoError(t, os.WriteFile(homeDir(home).configPath(), []byte(tc.args.saved), 0o600))
			}
			stdin := `{"session_id":"s1","cwd":"` + repo + `"}`
			var stdout, stderr bytes.Buffer

			code := runHook([]string{"prompt"}, getenv, nil, time.Now, strings.NewReader(stdin), &stdout, &stderr)

			assert.Equal(t, 0, code)
			assert.True(t, strings.HasPrefix(stderr.String(), tc.want.stderr), stderr.String())
			assert.Equal(t, tc.want.note, strings.Contains(stdout.String(), "your previous answer"), stdout.String())
		})
	}
}

// The run of an answer of session s1 in the repo when answered, with a candidate for the repo before it when waiting and after it when drafted
func hookPromptSetup(t *testing.T, getenv func(string) string, repo string, answered, waiting, drafted bool) string {
	t.Helper()
	store, err := tracefile.New(getenv(envRecordDir))
	require.NoError(t, err)
	propose := func(id, content string) {
		runs, err := store.List(context.Background(), trace.Filter{Name: trace.NameRun, Limit: 1})
		require.NoError(t, err)
		var stderr bytes.Buffer
		require.Equal(t, 0, runKnowledge([]string{
			"propose", "--id", id, "--kind", "judgment", "--content", content,
			"--trace", runs[0].ID, "--producer", sessionProducer, "--label", "repo=nodloop",
		}, getenv, nil, time.Now, &bytes.Buffer{}, &stderr), stderr.String())
	}
	if waiting {
		propose("short-msg", "keep commit messages to one line")
	}
	if !answered {
		return ""
	}
	stdin := `{"session_id":"s1","cwd":"` + repo + `","last_assistant_message":"done"}`
	require.Equal(t, 0, runHook([]string{"stop"}, getenv, nil, time.Now, strings.NewReader(stdin), &bytes.Buffer{}, &bytes.Buffer{}))
	runs, err := store.List(context.Background(), trace.Filter{Name: trace.NameRun, SessionID: "s1"})
	require.NoError(t, err)
	require.Len(t, runs, 1)
	if drafted {
		propose("pr-body", "write the motivation of a pull request first")
	}
	return runs[0].ID
}

func TestRunHookStop(t *testing.T) {
	type want struct {
		// Runs recorded after the one hookHome made
		runs   int
		output string
	}
	tcs := []struct {
		name    string
		args    string
		session string
		want    want
	}{
		{
			"an answer is recorded with its place and the items it got",
			`{"session_id":"s1","cwd":"{repo}","last_assistant_message":"run git -C repo status"}`, "",
			want{1, `"run git -C repo status"`},
		},
		{
			"a secret in the answer is redacted",
			`{"session_id":"s1","cwd":"{repo}","last_assistant_message":"token=abc123secret"}`, "",
			want{1, `"token=[redacted]"`},
		},
		{"off records nothing", `{"session_id":"s1","cwd":"{repo}","last_assistant_message":"x"}`, "off", want{}},
		{"a broken input records nothing", `not json`, "", want{}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, records, repo := hookHome(t, "use git -C")
			getenv := func(k string) string {
				return map[string]string{"HOME": home, envRecordDir: records, envSession: tc.session, envPluginVersion: "0.7.0"}[k]
			}
			stdin := strings.ReplaceAll(tc.args, "{repo}", repo)
			var stdout bytes.Buffer

			code := runHook([]string{"stop"}, getenv, nil, time.Now, strings.NewReader(stdin), &stdout, &bytes.Buffer{})

			assert.Equal(t, 0, code)
			assert.Empty(t, stdout.String())
			store, err := tracefile.New(records)
			require.NoError(t, err)
			runs, err := store.List(context.Background(), trace.Filter{Name: trace.NameRun, SessionID: "s1"})
			require.NoError(t, err)
			require.Len(t, runs, tc.want.runs)
			if tc.want.runs == 0 {
				return
			}
			assert.Equal(t, trace.Labels{"repo": {"nodloop"}, "dir": {"."}}, runs[0].Labels)
			assert.JSONEq(t, `{"applied":[{"id":"git-c","version":1}],"plugin":"0.7.0"}`, string(runs[0].Input))
			assert.JSONEq(t, tc.want.output, string(runs[0].Output))
		})
	}
}

// The stop hook starts the extraction of the previous run only when the conversation inferred a correction of it
func TestRunHookStopExtract(t *testing.T) {
	type args struct {
		// The reviewer of a reject on the first answer
		// None when empty
		reviewer string
		// The verdict of a person recorded on the first answer before that reject
		person  string
		session string
		// Answers after the first one
		stops int
		// The start fails
		fails bool
		// A person withdraws the reject
		withdrawn bool
		// The conversation drafted the lesson of the reject in its turn
		drafted bool
	}
	type want struct {
		extracts bool
		stderr   string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"an inferred correction of the previous answer is extracted", args{reviewer: "session", stops: 1}, want{extracts: true}},
		{"a later answer does not extract it again", args{reviewer: "session", stops: 2}, want{extracts: true}},
		{"a person's verdict is left to the nod skill", args{reviewer: "ann", stops: 1}, want{}},
		{"a person's approve wins over a later inferred reject", args{reviewer: "session", person: "approve", stops: 1}, want{}},
		{"an answer without a verdict extracts nothing", args{stops: 1}, want{}},
		{"manual extracts nothing", args{reviewer: "session", session: "manual", stops: 1}, want{}},
		{"immediate extracts like deferred", args{reviewer: "session", session: "immediate", stops: 1}, want{extracts: true}},
		{"a reject a person withdrew is not extracted", args{reviewer: "session", stops: 1, withdrawn: true}, want{}},
		{"a correction the turn already drafted is not extracted again", args{reviewer: "session", stops: 1, drafted: true}, want{}},
		{"a failed start still records the answer", args{reviewer: "session", stops: 1, fails: true}, want{extracts: true, stderr: "nodloop hook: no process"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, records, repo := hookHome(t, "use git -C")
			getenv := func(k string) string {
				return map[string]string{"HOME": home, envRecordDir: records, envSession: tc.args.session}[k]
			}
			var started [][]string
			start := func(args []string) error {
				started = append(started, args)
				if tc.args.fails {
					return errors.New("no process")
				}
				return nil
			}
			stop := func(answer string) string {
				var stderr bytes.Buffer
				stdin := `{"session_id":"s1","cwd":"` + repo + `","last_assistant_message":"` + answer + `"}`
				require.Equal(t, 0, runHook([]string{"stop"}, getenv, start, time.Now, strings.NewReader(stdin), &bytes.Buffer{}, &stderr))
				return stderr.String()
			}
			stop("committed with a long message")
			store, err := tracefile.New(records)
			require.NoError(t, err)
			first, err := store.List(context.Background(), trace.Filter{Name: trace.NameRun, SessionID: "s1"})
			require.NoError(t, err)
			require.Len(t, first, 1)
			if tc.args.person != "" {
				var stderr bytes.Buffer
				require.Equal(t, 0, runFeedback([]string{"add", "--trace", first[0].ID, "--verdict", tc.args.person, "--reviewer", "ann"},
					getenv, time.Now, &bytes.Buffer{}, &stderr), stderr.String())
			}
			if tc.args.reviewer != "" {
				var stderr bytes.Buffer
				require.Equal(t, 0, runFeedback([]string{"add", "--trace", first[0].ID, "--verdict", "reject", "--reason-code", "form",
					"--reason", "the message was too long", "--reviewer", tc.args.reviewer}, getenv, time.Now, &bytes.Buffer{}, &stderr), stderr.String())
			}
			if tc.args.withdrawn {
				var stderr bytes.Buffer
				require.Equal(t, 0, runFeedback([]string{"add", "--trace", first[0].ID, "--verdict", "withdraw", "--reviewer", "ann"},
					getenv, time.Now, &bytes.Buffer{}, &stderr), stderr.String())
			}
			if tc.args.drafted {
				var stderr bytes.Buffer
				require.Equal(t, 0, runKnowledge([]string{
					"propose", "--id", "short-msg", "--kind", "judgment", "--content", "keep commit messages to one line",
					"--trace", first[0].ID, "--producer", sessionProducer, "--label", "repo=nodloop",
				}, getenv, nil, time.Now, &bytes.Buffer{}, &stderr), stderr.String())
			}
			var stderr string
			for i := range tc.args.stops {
				stderr += stop(fmt.Sprintf("answer %d", i))
			}

			assert.Contains(t, stderr, tc.want.stderr)
			runs, err := store.List(context.Background(), trace.Filter{Name: trace.NameRun, SessionID: "s1"})
			require.NoError(t, err)
			assert.Len(t, runs, 1+tc.args.stops)
			if !tc.want.extracts {
				assert.Empty(t, started)
				return
			}
			assert.Equal(t, [][]string{{"knowledge", "extract", "--from", first[0].ID}}, started)
		})
	}
}

// A detached start runs the binary in a process of its own and appends its output to the log
func TestDetached(t *testing.T) {
	log := filepath.Join(t.TempDir(), "nodloop", "hook.log")

	require.NoError(t, detached(log)([]string{"-test.run=^$"}))

	assert.Eventually(t, func() bool {
		b, err := os.ReadFile(log)
		return err == nil && strings.Contains(string(b), "PASS")
	}, 10*time.Second, 50*time.Millisecond)
}

func TestReplyText(t *testing.T) {
	long := reply(strings.Repeat("a", answerRunes+5))
	assert.Equal(t, strings.Repeat("a", answerRunes)+"\n[cut by nodloop]", long.text())
	assert.Equal(t, "short", reply("short").text())
}

// A run the stop hook recorded with the item and a person's approve on it show in the loop report
func TestRunReportLoop(t *testing.T) {
	home, records, repo := hookHome(t, "use git -C")
	getenv := func(k string) string { return map[string]string{"HOME": home, envRecordDir: records}[k] }
	stdin := `{"session_id":"s2","cwd":"` + repo + `","last_assistant_message":"git -C repo status"}`
	require.Equal(t, 0, runHook([]string{"stop"}, getenv, nil, time.Now, strings.NewReader(stdin), &bytes.Buffer{}, &bytes.Buffer{}))
	store, err := tracefile.New(records)
	require.NoError(t, err)
	runs, err := store.List(context.Background(), trace.Filter{Name: trace.NameRun, SessionID: "s2"})
	require.NoError(t, err)
	require.Len(t, runs, 1)
	var stderr bytes.Buffer
	require.Equal(t, 0, runFeedback([]string{"add", "--trace", runs[0].ID, "--verdict", "approve"}, getenv, time.Now, &bytes.Buffer{}, &stderr), stderr.String())
	var stdout bytes.Buffer

	code := runReport([]string{"loop"}, getenv, time.Now, &stdout, &stderr)

	require.Equal(t, 0, code, stderr.String())
	assert.Equal(t, "loop\truns 2\tjudged 1\tinferred 0\tcorrected 0\twaiting 0\tapproved 1\n"+
		"scope\tunknown\titems 1\tsingle session 0\tnever applied 0\n"+
		"git-c\tv1\tapplied 1\tfollowed 1 of 1\trepeat 0\tinferred followed 0 of 0\tinferred repeat 0\tsettle -\n"+
		"misapplied\tnot measured: no label says which runs an item should have reached\n", stdout.String())
	var asJSON bytes.Buffer
	require.Equal(t, 0, runReport([]string{"loop", "--json"}, getenv, time.Now, &asJSON, &stderr), stderr.String())
	var report loop.LoopReport
	require.NoError(t, json.Unmarshal(asJSON.Bytes(), &report))
	assert.Equal(t, loop.Totals{Runs: 2, Judged: 1, Approved: 1}, report.Totals)
	assert.Equal(t, []loop.RunItem{{ID: "git-c", Version: 1, Applied: 1, Judged: 1, Followed: 1}}, report.Items)
}

// A run item narrows by the dir of its refuted run and promotes on its confirmed run, all on the records alone
func TestRunKnowledgeLifecycle(t *testing.T) {
	home, records, _ := hookHome(t, "use git -C")
	getenv := func(k string) string { return map[string]string{"HOME": home, envRecordDir: records}[k] }
	out := filepath.Join(home, "answer.txt")
	require.NoError(t, os.WriteFile(out, []byte("answer"), 0o600))
	record := func(dir string) string {
		var id bytes.Buffer
		require.Equal(t, 0, runRun([]string{"record", "--producer", sessionProducer, "--label", "repo=nodloop", "--label", "dir=" + dir,
			"--applied", "git-c:1", "--output", out}, getenv, time.Now, &id, &bytes.Buffer{}))
		return strings.TrimSpace(id.String())
	}
	refuted, confirmed := record("docs"), record(".")
	for id, result := range map[string]string{refuted: "refuted", confirmed: "confirmed"} {
		var stderr bytes.Buffer
		require.Equal(t, 0, runFeedback([]string{"outcome", "--trace", id, "--result", result}, getenv, time.Now, &bytes.Buffer{}, &stderr), stderr.String())
	}
	knowledgeRun := func(args ...string) (int, string, string) {
		var stdout, stderr bytes.Buffer
		code := runKnowledge(args, getenv, nil, time.Now, &stdout, &stderr)
		return code, stdout.String(), stderr.String()
	}

	code, _, stderr := knowledgeRun("narrow", "git-c", "--version", "1")
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "narrow: --key")
	code, stdout, stderr := knowledgeRun("narrow", "git-c", "--version", "1", "--key", "dir")
	require.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, "git-c\tv2\tcandidate\tscope runs of session. repo=nodloop. except dir=docs\n")
	code, _, stderr = knowledgeRun("retire", "git-c", "--version", "2", "--approver", "ann")
	require.Equal(t, 0, code, stderr)
	code, stdout, stderr = knowledgeRun("promote", "git-c", "--version", "1")
	require.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, "git-c\tv3\tcandidate\tbasis verified\toutcomes ["+confirmed+"]\n")
}

// Items past the prompt cap are left out and counted, and the stop hook records only the items that fit
func TestHookItemsFitting(t *testing.T) {
	item := func(id string, size int) knowledge.Knowledge {
		return knowledge.Knowledge{ID: id, Version: 1, Kind: knowledge.KindJudgment, Content: strings.Repeat("x", size)}
	}
	items := hookItems{item("a", 6000), item("b", 6000), item("c", 10)}

	shown, _ := items.fitting()

	require.Len(t, shown, 1)
	assert.Equal(t, "a", shown[0].ID)
	assert.Contains(t, items.context(sessionNote{}), "- 2 more items left out over the size cap\n")
	assert.Less(t, len([]rune(items.context(sessionNote{}))), 10_000)
}

// The note follows the items only in the room left under the context limit
func TestHookItemsContextNote(t *testing.T) {
	item := func(size int) hookItems {
		return hookItems{{ID: "a", Version: 1, Kind: knowledge.KindJudgment, Content: strings.Repeat("x", size)}}
	}
	note := sessionNote{run: "run-1"}
	tcs := []struct {
		name  string
		items hookItems
		want  bool
	}{
		{"no items leaves the note alone", nil, true},
		{"a small item leaves room", item(100), true},
		{"an item at the prompt cap leaves none", item(promptRunes - 250), false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.items.context(note)

			assert.Equal(t, tc.want, strings.Contains(got, note.text()))
			assert.LessOrEqual(t, len([]rune(got)), contextRunes)
			assert.Equal(t, len(tc.items) > 0, strings.HasPrefix(got, knowledge.PromptLead))
		})
	}
}

func TestRepoName(t *testing.T) {
	root := t.TempDir()
	main := filepath.Join(root, "nodloop")
	require.NoError(t, os.MkdirAll(filepath.Join(main, ".git", "worktrees", "core"), 0o755))
	worktree := filepath.Join(root, "nodloop-core")
	require.NoError(t, os.MkdirAll(worktree, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(main, ".git", "worktrees", "core")+"\n"), 0o600))
	submodule := filepath.Join(root, "vendor-lib")
	require.NoError(t, os.MkdirAll(submodule, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(submodule, ".git"), []byte("gitdir: ../nodloop/.git/modules/vendor-lib\n"), 0o600))

	assert.Equal(t, "nodloop", repoName(main))
	assert.Equal(t, "nodloop", repoName(worktree), "a worktree takes the name of its main checkout")
	assert.Equal(t, "vendor-lib", repoName(submodule), "a submodule keeps its own name")
}

// waiting lists the candidates a place would receive with the runs that taught them, and none of another place
func TestRunKnowledgeWaiting(t *testing.T) {
	home, records, repo := hookHome(t, "use git -C")
	getenv := func(k string) string { return map[string]string{"HOME": home, envRecordDir: records}[k] }
	run := hookPromptSetup(t, getenv, repo, false, true, false)
	require.Empty(t, run)
	store, err := tracefile.New(records)
	require.NoError(t, err)
	runs, err := store.List(context.Background(), trace.Filter{Name: trace.NameRun})
	require.NoError(t, err)
	tcs := []struct {
		name string
		args []string
		want string
	}{
		{"the repo waits for the candidate", []string{"waiting", "--producer", "session", "--label", "repo=nodloop"},
			"short-msg\tv1\tjudgment\tkeep commit messages to one line\tscope runs of session. repo=nodloop\tfrom " + runs[0].ID + "\n"},
		{"another repo waits for nothing", []string{"waiting", "--producer", "session", "--label", "repo=other"}, ""},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer

			code := runKnowledge(tc.args, getenv, nil, time.Now, &stdout, &stderr)

			require.Equal(t, 0, code, stderr.String())
			assert.Equal(t, tc.want, stdout.String())
		})
	}
}

func TestHoldoutWithholds(t *testing.T) {
	tcs := []struct {
		name    string
		share   holdout
		session string
		want    bool
	}{
		{"no share withholds nothing", 0, "s1", false},
		{"a turn without a session is never drawn", 0.999, "", false},
		{"a share near 1 withholds the turn", 0.999, "s1", true},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.share.withholds(tc.session, ""))
		})
	}
}

// A share draws about that share of turns and the same turn always draws the same
func TestHoldoutShare(t *testing.T) {
	drawn := 0
	for turn := range 1000 {
		previous := trace.NewID(time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC).Add(time.Duration(turn) * time.Minute))
		if holdout(0.2).withholds("s1", previous) {
			drawn++
		}
		assert.Equal(t, holdout(0.2).withholds("s1", previous), holdout(0.2).withholds("s1", previous))
	}
	assert.InDelta(t, 200, drawn, 50)
}

// A turn the holdout draws gets no item in its prompt and its run records them as withheld
func TestRunHookHoldout(t *testing.T) {
	require.True(t, holdout(0.999).withholds("s1", ""))
	home, records, repo := hookHome(t, "use git -C")
	require.Equal(t, 0, runConfig([]string{"holdout", "0.999"}, func(k string) string { return map[string]string{"HOME": home}[k] }, &bytes.Buffer{}, &bytes.Buffer{}))
	getenv := func(k string) string { return map[string]string{"HOME": home, envRecordDir: records}[k] }
	stdin := `{"session_id":"s1","cwd":"` + repo + `","last_assistant_message":"done"}`
	var prompt bytes.Buffer

	require.Equal(t, 0, runHook([]string{"prompt"}, getenv, nil, time.Now, strings.NewReader(stdin), &prompt, &bytes.Buffer{}))
	require.Equal(t, 0, runHook([]string{"stop"}, getenv, nil, time.Now, strings.NewReader(stdin), &bytes.Buffer{}, &bytes.Buffer{}))

	assert.NotContains(t, prompt.String(), "use git -C")
	store, err := tracefile.New(records)
	require.NoError(t, err)
	runs, err := store.List(context.Background(), trace.Filter{Name: trace.NameRun, SessionID: "s1"})
	require.NoError(t, err)
	require.Len(t, runs, 1)
	var in struct {
		Applied  []knowledge.Ref `json:"applied"`
		Withheld []knowledge.Ref `json:"withheld"`
	}
	require.NoError(t, json.Unmarshal(runs[0].Input, &in))
	assert.Empty(t, in.Applied)
	assert.Equal(t, []knowledge.Ref{{ID: "git-c", Version: 1}}, in.Withheld)
}

// The reaction point judges the user's message on the previous answer before the conversation does
func TestRunHookReaction(t *testing.T) {
	type args struct {
		// The answer of the endpoint and empty for one that fails
		answer string
		// Members of the reaction setup
		members string
	}
	type want struct {
		verdict feedback.Verdict
		reason  string
		// A substring of the added context and one it must not hold
		context string
		absent  string
		stderr  string
	}
	message := "that was wrong, use git -C"
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a sure correction is recorded as a reject with the message", args{`{"answers":{"corrects":{"noul":0.9},"approves":{"noul":0.1}}}`, "laya"},
			want{feedback.VerdictReject, message, "was recorded as a reject of your previous answer", "call the nodloop feedback tool", ""}},
		{"a sure approval is recorded and asks nothing", args{`{"answers":{"corrects":{"noul":0.1},"approves":{"noul":0.9}}}`, "laya"},
			want{feedback.VerdictApprove, "", "use git -C", "previous answer", ""}},
		{"an unsure answer leaves the conversation to judge", args{`{"answers":{"corrects":{"noul":0.3},"approves":{"noul":0.3}}}`, "laya"},
			want{"", "", "call the nodloop feedback tool", "recorded as a reject", ""}},
		{"a cascade that ends at claude defers to the conversation", args{`{"answers":{"corrects":{"noul":0.6},"approves":{"noul":0.3}}}`, "laya,claude"},
			want{"", "", "call the nodloop feedback tool", "recorded as a reject", ""}},
		{"a failing endpoint leaves the conversation to judge", args{"", "laya"},
			want{"", "", "call the nodloop feedback tool", "recorded as a reject", "nodloop hook: laya: classify: endpoint answered an error status"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			states := make(chan string, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					State string `json:"state"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				states <- req.State
				if tc.args.answer == "" {
					http.Error(w, "down", http.StatusServiceUnavailable)
					return
				}
				_, _ = w.Write([]byte(tc.args.answer))
			}))
			t.Cleanup(srv.Close)
			home, records, repo := hookHome(t, "use git -C")
			getenv := func(k string) string { return map[string]string{"HOME": home, envRecordDir: records}[k] }
			for _, args := range [][]string{{"add", "laya", "--url", srv.URL}, {"use", "reaction", "--members", tc.args.members}} {
				var stderr bytes.Buffer
				require.Equal(t, 0, runClassifier(args, getenv, time.Now, &bytes.Buffer{}, &stderr), stderr.String())
			}
			run := hookPromptSetup(t, getenv, repo, true, false, false)
			stdin := `{"session_id":"s1","cwd":"` + repo + `","prompt":"` + message + `"}`
			var stdout, stderr bytes.Buffer

			code := runHook([]string{"prompt"}, getenv, nil, time.Now, strings.NewReader(stdin), &stdout, &stderr)

			assert.Equal(t, 0, code)
			assert.True(t, strings.HasPrefix(<-states, "## User message\n\n"+message), "a truncating endpoint still reads the message")
			var got struct {
				Out struct {
					Context string `json:"additionalContext"`
				} `json:"hookSpecificOutput"`
			}
			require.NoError(t, json.Unmarshal(stdout.Bytes(), &got), stdout.String())
			assert.Contains(t, got.Out.Context, tc.want.context)
			assert.NotContains(t, got.Out.Context, tc.want.absent)
			assert.Contains(t, stderr.String(), tc.want.stderr)
			verdicts, err := feedbackfile.New(records)
			require.NoError(t, err)
			recorded, err := verdicts.List(context.Background(), feedback.Filter{TraceID: run})
			require.NoError(t, err)
			var latest struct {
				verdict feedback.Verdict
				reason  string
			}
			if newest := feedback.Records(recorded).Latest(); len(newest) > 0 {
				latest.verdict, latest.reason = newest[0].Verdict, newest[0].Reason
			}
			assert.Equal(t, tc.want.verdict, latest.verdict)
			assert.Equal(t, tc.want.reason, latest.reason)
		})
	}
}

// A corrupt line in the records leaves out its record and the hooks go on with the rest
func TestRunHookCorruptLine(t *testing.T) {
	home, records, repo := hookHome(t, "use git -C")
	for _, name := range []string{"knowledge.jsonl", "traces.jsonl"} {
		f, err := os.OpenFile(filepath.Join(records, name), os.O_APPEND|os.O_WRONLY, 0o600)
		require.NoError(t, err)
		_, err = f.WriteString("not json\n")
		require.NoError(t, err)
		require.NoError(t, f.Close())
	}
	getenv := func(k string) string { return map[string]string{"HOME": home, envRecordDir: records}[k] }
	stdin := `{"session_id":"s1","cwd":"` + repo + `","last_assistant_message":"done"}`
	var prompt, promptErr, stopErr bytes.Buffer

	require.Equal(t, 0, runHook([]string{"prompt"}, getenv, nil, time.Now, strings.NewReader(stdin), &prompt, &promptErr))
	require.Equal(t, 0, runHook([]string{"stop"}, getenv, nil, time.Now, strings.NewReader(stdin), &bytes.Buffer{}, &stopErr))

	assert.Contains(t, prompt.String(), "use git -C")
	assert.Contains(t, promptErr.String(), "knowledge.jsonl has corrupt lines")
	assert.Contains(t, stopErr.String(), "traces.jsonl has corrupt lines")
	store, err := tracefile.New(records)
	require.NoError(t, err)
	runs, err := store.List(context.Background(), trace.Filter{Name: trace.NameRun, SessionID: "s1"})
	assert.ErrorIs(t, err, jsonl.ErrCorrupt)
	assert.Len(t, runs, 1, "the stop hook still records the answer")
}
