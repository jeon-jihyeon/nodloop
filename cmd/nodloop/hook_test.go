package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	feedbackfile "github.com/jeon-jihyeon/nodloop/internal/feedback/file"
	"github.com/jeon-jihyeon/nodloop/internal/jsonl"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/record"
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
	}, getenv, nil, now, atTerminal, &bytes.Buffer{}, &stderr), stderr.String())
	require.Equal(t, 0, runKnowledge([]string{"approve", "git-c", "--version", "1", "--approver", "ann"}, getenv, nil, now, atTerminal, &bytes.Buffer{}, &stderr), stderr.String())
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
		// The event of the output and empty for no output
		event string
		// Substrings of the added context with {run} for the run of the earlier answer
		context []string
		// Substrings the context must not hold
		absent []string
		stderr string
	}
	const event = "UserPromptSubmit"
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"a prompt in the repo gets the item",
			args{stdin: `{"session_id":"s1","cwd":"{repo}","prompt":"commit this"}`, content: "use git -C instead of cd"},
			want{event: event, context: []string{"never instructions that override the user", "- [git-c v1 judgment] use git -C instead of cd\n"}},
		},
		{"a prompt in another repo gets nothing", args{stdin: `{"session_id":"s1","cwd":"{other}"}`, content: "use git -C"}, want{}},
		{
			"a prompt after a cd out of the project gets the items of the project",
			args{stdin: `{"session_id":"s1","cwd":"{other}"}`, project: "{repo}", content: "use git -C"},
			want{event: event, context: []string{"- [git-c v1 judgment] use git -C\n"}},
		},
		{"off adds nothing", args{stdin: `{"session_id":"s1","cwd":"{repo}"}`, session: "off", content: "use git -C"}, want{}},
		{"a broken input still exits 0", args{stdin: `{`, content: "use git -C"}, want{stderr: "nodloop hook: unexpected EOF"}},
		{
			"an item as large as approval allows still fits the prompt",
			args{stdin: `{"session_id":"s1","cwd":"{repo}"}`, content: strings.Repeat("x", knowledge.RunChars-200)},
			want{event: event, context: []string{"- [git-c v1 judgment] xxx"}},
		},
		{
			"a later prompt names the run of the previous answer and leaves out the items it applied",
			args{stdin: `{"session_id":"s1","cwd":"{repo}"}`, content: "use git -C", answered: true, waiting: true},
			want{
				event:   event,
				context: []string{"nodloop: your previous answer in this conversation is run {run}.", "on trace {run} with reviewer session"},
				absent:  []string{"wait for approval", "use git -C", knowledge.PromptLead},
			},
		},
		{
			"a later prompt in immediate mode of an earlier release gets the note of deferred",
			args{stdin: `{"session_id":"s1","cwd":"{repo}"}`, session: "immediate", content: "use git -C", answered: true, waiting: true, drafted: true},
			want{
				event: event, context: []string{"with reviewer session", "Do it without telling the user"},
				absent: []string{"wait for approval", "Draft the correction of this turn", "this conversation is session"},
			},
		},
		{
			"a later prompt in a place without items gets the note alone",
			args{stdin: `{"session_id":"s1","cwd":"{other}"}`, content: "use git -C", answered: true},
			want{event: event, context: []string{"is run {run}."}, absent: []string{knowledge.PromptLead}},
		},
		{
			"the first prompt counts no waiting lesson",
			args{stdin: `{"session_id":"s1","cwd":"{repo}"}`, content: "use git -C", waiting: true},
			want{event: event, context: []string{"use git -C\n"}, absent: []string{"wait for approval"}},
		},
		{
			"manual adds the items and no note",
			args{stdin: `{"session_id":"s1","cwd":"{repo}"}`, session: "manual", content: "use git -C", waiting: true, drafted: true},
			want{event: event, context: []string{"use git -C\n"}, absent: []string{"previous answer", "wait for approval"}},
		},
		{
			"manual adds nothing later since the items are in the context",
			args{stdin: `{"session_id":"s1","cwd":"{repo}"}`, session: "manual", content: "use git -C", answered: true, waiting: true, drafted: true},
			want{},
		},
		{
			"a prompt without a session names no run",
			args{stdin: `{"cwd":"{repo}"}`, content: "use git -C", answered: true},
			want{event: event, context: []string{"use git -C\n"}, absent: []string{"previous answer"}},
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
			now := hookClock()
			run := hookPromptSetup(t, getenv, now, repo, tc.args.answered, tc.args.waiting, tc.args.drafted)
			stdin := place.Replace(tc.args.stdin)
			var stdout, stderr bytes.Buffer

			code := runHook([]string{"prompt"}, getenv, nil, now, strings.NewReader(stdin), &stdout, &stderr)

			event, added := hookOutput(t, stdout.Bytes())
			assert.Equal(t, 0, code)
			assert.Contains(t, stderr.String(), tc.want.stderr)
			assert.Equal(t, tc.want.event, event)
			assert.Equal(t, tc.want.context, hookFound(added, run, slices.Concat(tc.want.context, tc.want.absent)), added)
		})
	}
}

// The prompts of one session add each item version once and add again only what the context may lack
func TestRunHookPromptDelta(t *testing.T) {
	type step string
	const (
		prompt  step = "prompt"  // a prompt of session s1 whose context is kept
		stop    step = "stop"    // an answer of session s1
		cont    step = "cont"    // the stop of a turn of session s1 a stop hook continued
		approve step = "approve" // an approval of item pr-c
		revise  step = "revise"  // an approval of version 2 of git-c
		compact step = "compact" // a session start after a compaction
	)
	type want struct {
		// Substrings of the context of the last prompt and ones it must not hold
		context []string
		absent  []string
	}
	gitC, gitC2, prC := "- [git-c v1 judgment] use git -C\n", "- [git-c v2 judgment] use git -C always\n", "- [pr-c v1 judgment] write the motivation first\n"
	tcs := []struct {
		name string
		args []step
		want want
	}{
		{"the first prompt adds every item under the prompt lead", []step{prompt}, want{[]string{knowledge.PromptLead + gitC}, []string{knowledge.DeltaLead}}},
		{"a prompt after an answer adds no item", []step{prompt, stop, prompt}, want{nil, []string{"[git-c", knowledge.PromptLead, knowledge.DeltaLead}}},
		{"an item approved after an answer comes under the delta lead", []step{prompt, stop, approve, prompt},
			want{[]string{knowledge.DeltaLead + prC}, []string{"[git-c", knowledge.PromptLead}}},
		{"a new version comes under the delta lead", []step{prompt, stop, revise, prompt}, want{[]string{knowledge.DeltaLead + gitC2}, []string{gitC}}},
		{"an item approved during a turn is added at the next prompt", []step{prompt, stop, prompt, approve, stop, prompt},
			want{[]string{knowledge.DeltaLead + prC}, []string{"[git-c"}}},
		{"an item approved between turns is added again once and then never", []step{prompt, stop, approve, prompt, stop, prompt, stop, prompt},
			want{nil, []string{"[pr-c", "[git-c"}}},
		{"a prompt after a stop a stop hook continued adds no item", []step{prompt, stop, cont, prompt},
			want{nil, []string{"[git-c", knowledge.PromptLead, knowledge.DeltaLead}}},
		{"an item approved before the answer of a continued turn is added at the next prompt", []step{prompt, stop, prompt, approve, stop, cont, prompt},
			want{[]string{knowledge.DeltaLead + prC}, []string{"[git-c"}}},
		{"an item approved while a stop hook continued the turn is added at the next prompt", []step{prompt, stop, prompt, stop, approve, cont, prompt},
			want{[]string{knowledge.DeltaLead + prC}, []string{"[git-c"}}},
		{"a prompt after a compaction adds no item since the start added them", []step{prompt, stop, compact, prompt},
			want{nil, []string{"[git-c", knowledge.PromptLead}}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, records, repo := hookHome(t, "use git -C")
			getenv := func(k string) string { return map[string]string{"HOME": home, envRecordDir: records}[k] }
			now := hookClock()
			store, err := tracefile.New(records)
			require.NoError(t, err)
			knowledgeOf := func(args ...string) {
				var stderr bytes.Buffer
				require.Equal(t, 0, runKnowledge(args, getenv, nil, now, atTerminal, &bytes.Buffer{}, &stderr), stderr.String())
			}
			propose := func(id, content string, version string) func() string {
				return func() string {
					runs, err := store.List(context.Background(), record.TraceFilter{Name: string(trace.NameRun), Limit: 1})
					require.NoError(t, err)
					knowledgeOf("propose", "--id", id, "--kind", "judgment", "--content", content,
						"--trace", runs[0].ID, "--producer", sessionProducer, "--label", "repo=nodloop")
					knowledgeOf("approve", id, "--version", version, "--approver", "ann")
					return ""
				}
			}
			hook := func(action string, continued bool) func() string {
				return func() string {
					var stdout, stderr bytes.Buffer
					stdin := fmt.Sprintf(`{"session_id":"s1","cwd":%q,"source":"compact","last_assistant_message":"done","stop_hook_active":%t}`, repo, continued)
					require.Equal(t, 0, runHook([]string{action}, getenv, nil, now, strings.NewReader(stdin), &stdout, &stderr))
					require.Empty(t, stderr.String())
					return stdout.String()
				}
			}
			steps := map[step]func() string{
				prompt: hook("prompt", false), stop: hook("stop", false), cont: hook("stop", true), compact: hook("start", false),
				approve: propose("pr-c", "write the motivation first", "1"), revise: propose("git-c", "use git -C always", "2"),
			}
			// The output of the last run of each step
			outputs := map[step]string{}
			for _, s := range tc.args {
				outputs[s] = steps[s]()
			}

			event, added := hookOutput(t, []byte(outputs[prompt]))
			assert.Equal(t, "UserPromptSubmit", event)
			assert.Equal(t, tc.want.context, hookFound(added, "", slices.Concat(tc.want.context, tc.want.absent)), added)
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
		// The event of the output and empty for no output
		event string
		// Substrings of the added context and ones it must not hold
		context []string
		absent  []string
	}
	reaction, draft, waiting := "call the nodloop feedback tool on trace {run}", "Draft the correction of this turn", "wait for approval: "
	silent, rejected := "Do it without telling the user", "was recorded as a reject of your previous answer, run {run}"
	session := "this conversation is session s1"
	const event = "UserPromptSubmit"
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"deferred adds items alone on the first prompt", args{"deferred", first}, want{event, []string{"use git -C\n"}, []string{waiting, reaction}}},
		{"deferred records silently later and asks nothing", args{"deferred", later}, want{event, []string{reaction, silent}, []string{draft, waiting, session}}},
		{"deferred drafts nothing in the turn of a recorded reject", args{"deferred", recorded},
			want{event, []string{rejected, "drafted after the turn"}, []string{draft, reaction}}},
		{"immediate of an earlier release adds items alone on the first prompt", args{"immediate", first},
			want{event, []string{"use git -C\n"}, []string{waiting, reaction}}},
		{"immediate of an earlier release records silently later like deferred", args{"immediate", later},
			want{event, []string{reaction, silent}, []string{draft, waiting, session}}},
		{"immediate of an earlier release drafts nothing in the turn of a recorded reject", args{"immediate", recorded},
			want{event, []string{rejected, "drafted after the turn"}, []string{draft, reaction, session}}},
		{"manual adds items alone on the first prompt", args{"manual", first}, want{event, []string{"use git -C\n"}, []string{waiting}}},
		{"manual adds nothing later since the items are in the context", args{"manual", later}, want{}},
		{"manual asks no reaction point", args{"manual", recorded}, want{}},
		{"off adds nothing on the first prompt", args{"off", first}, want{}},
		{"off adds nothing later", args{"off", later}, want{}},
		{"off adds nothing after a correction", args{"off", recorded}, want{}},
	}
	// Only the recorded turn has a reaction point and its endpoint is sure of a correction
	points := map[turn]func(t *testing.T, getenv func(string) string, now func() time.Time){
		first: func(*testing.T, func(string) string, func() time.Time) {},
		later: func(*testing.T, func(string) string, func() time.Time) {},
		recorded: func(t *testing.T, getenv func(string) string, now func() time.Time) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"answers":{"corrects":{"noul":0.95},"approves":{"noul":0.05}}}`))
			}))
			t.Cleanup(srv.Close)
			require.Equal(t, 0, runClassifier([]string{"set", "reaction", "--url", srv.URL}, getenv, now, &bytes.Buffer{}, &bytes.Buffer{}))
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, records, repo := hookHome(t, "use git -C")
			getenv := func(k string) string { return map[string]string{"HOME": home, envRecordDir: records}[k] }
			now := hookClock()
			points[tc.args.turn](t, getenv, now)
			run := hookPromptSetup(t, getenv, now, repo, tc.args.turn != first, true, tc.args.turn == later)
			// The env names the mode since config session_mode saves no mode of an earlier release
			inMode := envOf(map[string]string{"HOME": home, envRecordDir: records, envSession: tc.args.mode})
			stdin := `{"session_id":"s1","cwd":"` + repo + `","prompt":"that was wrong"}`
			var stdout, stderr bytes.Buffer

			code := runHook([]string{"prompt"}, inMode, nil, now, strings.NewReader(stdin), &stdout, &stderr)

			event, added := hookOutput(t, stdout.Bytes())
			assert.Equal(t, 0, code)
			assert.Empty(t, stderr.String())
			assert.Equal(t, tc.want.event, event)
			assert.Equal(t, tc.want.context, hookFound(added, run, slices.Concat(tc.want.context, tc.want.absent)), added)
		})
	}
}

// The mode comes from NODLOOP_SESSION, then session_mode in config.json, then deferred, and an unknown one records nothing
func TestRunHookSessionMode(t *testing.T) {
	type args struct {
		env string
		// config.json and none when empty
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
		{"the env wins over the config", args{"deferred", `{"session_mode":"manual"}`}, want{note: true}},
		{"immediate of an earlier release in the env runs as deferred", args{"immediate", `{"session_mode":"manual"}`}, want{note: true}},
		{"immediate of an earlier release in the config runs as deferred", args{"", `{"session_mode":"immediate"}`}, want{note: true}},
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
			now := hookClock()
			hookPromptSetup(t, func(k string) string { return map[string]string{"HOME": home, envRecordDir: records}[k] }, now, repo, true, false, false)
			hookSaveConfig(t, home, tc.args.saved)
			stdin := `{"session_id":"s1","cwd":"` + repo + `"}`
			var stdout, stderr bytes.Buffer

			code := runHook([]string{"prompt"}, getenv, nil, now, strings.NewReader(stdin), &stdout, &stderr)

			assert.Equal(t, 0, code)
			assert.True(t, strings.HasPrefix(stderr.String(), tc.want.stderr), stderr.String())
			assert.Equal(t, tc.want.note, strings.Contains(stdout.String(), "your previous answer"), stdout.String())
		})
	}
}

// A compaction or a clear adds every fitting item of the place again and a subagent start adds them while no start names a waiting draft
func TestRunHookStart(t *testing.T) {
	type args struct {
		// The hook action and start when empty
		action string
		source string
		mode   string
		// Another repo than the one of the item
		other bool
		// A broken line follows the knowledge records
		corrupt bool
		// The holdout withholds session s1
		withheld bool
	}
	type want struct {
		// The event and context of the output, both empty for none
		event, context string
		stderr         string
	}
	items := want{event: "SessionStart", context: knowledge.PromptLead + "- [git-c v1 judgment] use git -C\n"}
	subagent := want{event: "SubagentStart", context: items.context}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a compact adds the items", args{source: "compact"}, items},
		{"a clear adds the items", args{source: "clear"}, items},
		{"manual adds the items", args{source: "compact", mode: "manual"}, items},
		{"a corrupt line leaves the other records added", args{source: "clear", corrupt: true},
			want{event: items.event, context: items.context, stderr: "knowledge.jsonl has corrupt lines"}},
		{"a startup adds nothing since the first prompt does", args{source: "startup"}, want{}},
		{"a resume adds nothing", args{source: "resume"}, want{}},
		{"a fork adds nothing", args{source: "fork"}, want{}},
		{"another repo has no item", args{source: "compact", other: true}, want{}},
		{"a withheld session gets no item", args{source: "compact", withheld: true}, want{}},
		{"off adds nothing", args{source: "compact", mode: "off"}, want{}},
		{"a subagent gets the items", args{action: "subagent"}, subagent},
		{"a subagent in manual gets the items", args{action: "subagent", mode: "manual"}, subagent},
		{"a subagent gets the items whatever the source", args{action: "subagent", source: "startup"}, subagent},
		{"a subagent with a corrupt line gets the other records", args{action: "subagent", corrupt: true},
			want{event: subagent.event, context: subagent.context, stderr: "knowledge.jsonl has corrupt lines"}},
		{"a subagent in another repo gets nothing", args{action: "subagent", other: true}, want{}},
		{"a subagent of a withheld session gets no item", args{action: "subagent", withheld: true}, want{}},
		{"a subagent in off gets nothing", args{action: "subagent", mode: "off"}, want{}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, records, repo := hookHome(t, "use git -C")
			getenv := func(k string) string {
				return map[string]string{"HOME": home, envRecordDir: records, envSession: tc.args.mode}[k]
			}
			now := hookClock()
			hookPromptSetup(t, getenv, now, repo, false, true, false)
			// A share of 0 withholds no session like no holdout at all
			share := map[bool]string{false: "0", true: "0.999"}[tc.args.withheld]
			require.Equal(t, 0, runConfig([]string{"holdout", share}, getenv, atTerminal, &bytes.Buffer{}, &bytes.Buffer{}))
			hookAppend(t, filepath.Join(records, "knowledge.jsonl"), map[bool]string{true: "not json\n"}[tc.args.corrupt])
			cwd := map[bool]string{false: repo, true: filepath.Join(t.TempDir(), "other")}[tc.args.other]
			require.NoError(t, os.MkdirAll(filepath.Join(cwd, ".git"), 0o755))
			stdin := `{"session_id":"s1","cwd":"` + cwd + `","source":"` + tc.args.source + `"}`
			var stdout, stderr bytes.Buffer

			code := runHook([]string{cmp.Or(tc.args.action, "start")}, getenv, nil, now, strings.NewReader(stdin), &stdout, &stderr)

			event, added := hookOutput(t, stdout.Bytes())
			assert.Equal(t, 0, code)
			assert.Equal(t, tc.want.event, event)
			assert.Equal(t, tc.want.context, added)
			assert.Contains(t, stderr.String(), tc.want.stderr)
		})
	}
}

// The first prompt after a start counts the items the start added as in the context
// A clear gives the conversation a new session id with no run so only the refill tells the prompt what it holds
func TestRunHookPromptAfterStart(t *testing.T) {
	tcs := []struct {
		name string
		args string
		// The context the prompt adds
		want string
	}{
		{"a clear leaves nothing to add", "clear", ""},
		{"a compact leaves nothing to add", "compact", ""},
		{"a startup leaves every item to the prompt", "startup", knowledge.PromptLead + "- [git-c v1 judgment] use git -C\n"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, records, repo := hookHome(t, "use git -C")
			getenv := envOf(map[string]string{"HOME": home, envRecordDir: records, envSession: "manual"})
			now := hookClock()
			start := `{"session_id":"s2","cwd":"` + repo + `","source":"` + tc.args + `"}`
			require.Equal(t, 0, runHook([]string{"start"}, getenv, nil, now, strings.NewReader(start), &bytes.Buffer{}, &bytes.Buffer{}))
			var stdout, stderr bytes.Buffer

			code := runHook([]string{"prompt"}, getenv, nil, now, strings.NewReader(`{"session_id":"s2","cwd":"`+repo+`"}`), &stdout, &stderr)

			_, added := hookOutput(t, stdout.Bytes())
			assert.Equal(t, 0, code)
			assert.Empty(t, stderr.String())
			assert.Equal(t, tc.want, added)
		})
	}
}

// The run of an answer of session s1 in the repo when answered, with a candidate for the repo before it when waiting and after it when drafted
func hookPromptSetup(t *testing.T, getenv func(string) string, now func() time.Time, repo string, answered, waiting, drafted bool) string {
	t.Helper()
	store, err := tracefile.New(getenv(envRecordDir))
	require.NoError(t, err)
	propose := func(id, content string) {
		runs, err := store.List(context.Background(), record.TraceFilter{Name: string(trace.NameRun), Limit: 1})
		require.NoError(t, err)
		var stderr bytes.Buffer
		require.Equal(t, 0, runKnowledge([]string{
			"propose", "--id", id, "--kind", "judgment", "--content", content,
			"--trace", runs[0].ID, "--producer", sessionProducer, "--label", "repo=nodloop",
		}, getenv, nil, now, atTerminal, &bytes.Buffer{}, &stderr), stderr.String())
	}
	if waiting {
		propose("short-msg", "keep commit messages to one line")
	}
	if !answered {
		return ""
	}
	stdin := `{"session_id":"s1","cwd":"` + repo + `","last_assistant_message":"done"}`
	require.Equal(t, 0, runHook([]string{"stop"}, getenv, nil, now, strings.NewReader(stdin), &bytes.Buffer{}, &bytes.Buffer{}))
	runs, err := store.List(context.Background(), record.TraceFilter{Name: string(trace.NameRun), SessionID: "s1"})
	require.NoError(t, err)
	require.Len(t, runs, 1)
	if drafted {
		propose("pr-body", "write the motivation of a pull request first")
	}
	return runs[0].ID
}

// A fake clock an hour after the records of hookHome
// It steps a second on every read so the records keep the order of the calls
func hookClock() func() time.Time {
	at := time.Date(2026, 10, 2, 1, 0, 0, 0, time.UTC)
	return func() time.Time {
		at = at.Add(time.Second)
		return at
	}
}

// The event and the added context of a hook output and both empty for no output
func hookOutput(t *testing.T, stdout []byte) (event, added string) {
	t.Helper()
	if len(stdout) == 0 {
		return "", ""
	}
	var got struct {
		Out struct {
			Event   string `json:"hookEventName"`
			Context string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	require.NoError(t, json.Unmarshal(stdout, &got), string(stdout))
	return got.Out.Event, got.Out.Context
}

// The substrings the context holds in their order with {run} standing for the run
func hookFound(added, run string, subs []string) []string {
	var found []string
	for _, s := range subs {
		if strings.Contains(added, strings.ReplaceAll(s, "{run}", run)) {
			found = append(found, s)
		}
	}
	return found
}

// Writes config.json of the home and nothing when saved is empty
func hookSaveConfig(t *testing.T, home, saved string) {
	t.Helper()
	if saved == "" {
		return
	}
	require.NoError(t, os.MkdirAll(homeDir(home).dir(), 0o755))
	require.NoError(t, os.WriteFile(homeDir(home).config().ConfigPath(), []byte(saved), 0o600))
}

// Appends the line to a record file and an empty line leaves it as it is
func hookAppend(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = f.WriteString(line)
	require.NoError(t, err)
	require.NoError(t, f.Close())
}

func TestRunHookStop(t *testing.T) {
	type args struct {
		// The hook input with {repo} for the repository of the item
		stdin   string
		session string
		// The hook arguments after stop
		flags []string
	}
	// A recorded run of session s1
	type run struct {
		labels        trace.Labels
		input, output string
	}
	type want struct {
		runs   []run
		stderr string
	}
	recorded := func(host, output string) []run {
		return []run{{trace.Labels{"repo": {"nodloop"}, "dir": {"."}, "host": {host}}, `{"applied":[{"id":"git-c","version":1}],"plugin":"0.7.0"}`, output}}
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"an answer is recorded with its place and the items it got",
			args{stdin: `{"session_id":"s1","cwd":"{repo}","last_assistant_message":"run git -C repo status"}`},
			want{runs: recorded("claude-code", `"run git -C repo status"`)},
		},
		{
			"a secret in the answer is redacted",
			args{stdin: `{"session_id":"s1","cwd":"{repo}","last_assistant_message":"token=abc123secret"}`},
			want{runs: recorded("claude-code", `"token=[redacted]"`)},
		},
		{
			"a Codex answer is recorded with its host",
			args{stdin: "codex_stop.json", flags: []string{"--host", "codex"}},
			want{runs: recorded("codex", `"Ran git -C repo status."`)},
		},
		{
			"the stop of a turn a stop hook continued records its run as continued",
			args{stdin: `{"session_id":"s1","cwd":"{repo}","last_assistant_message":"Reply keep or drop","stop_hook_active":true}`},
			want{runs: []run{{
				trace.Labels{"repo": {"nodloop"}, "dir": {"."}, "host": {"claude-code"}},
				`{"applied":[{"id":"git-c","version":1}],"plugin":"0.7.0","continued":true}`, `"Reply keep or drop"`,
			}}},
		},
		{"a Cursor stop carries no answer and records nothing", args{stdin: "cursor_stop.json", flags: []string{"--host", "cursor"}}, want{}},
		{"a Copilot stop carries no answer and records nothing", args{stdin: "copilot_stop.json", flags: []string{"--host", "copilot"}}, want{}},
		{"off records nothing", args{stdin: `{"session_id":"s1","cwd":"{repo}","last_assistant_message":"x"}`, session: "off"}, want{}},
		{"a broken input records nothing", args{stdin: `not json`}, want{stderr: "nodloop hook: invalid character"}},
		{
			"an unknown host records nothing",
			args{stdin: `{"session_id":"s1","cwd":"{repo}","last_assistant_message":"x"}`, flags: []string{"--host", "zed"}},
			want{stderr: `unknown host "zed". Use claude-code, codex, cursor or copilot`},
		},
		{
			"an unknown flag records nothing",
			args{stdin: `{"session_id":"s1","cwd":"{repo}","last_assistant_message":"x"}`, flags: []string{"--hots", "codex"}},
			want{stderr: "flag provided but not defined: -hots"},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, records, repo := hookHome(t, "use git -C")
			getenv := func(k string) string {
				return map[string]string{"HOME": home, envRecordDir: records, envSession: tc.args.session, envPluginVersion: "0.7.0"}[k]
			}
			stdin := hookFixture(t, tc.args.stdin, repo)
			var stdout, stderr bytes.Buffer

			code := runHook(append([]string{"stop"}, tc.args.flags...), getenv, nil, hookClock(), strings.NewReader(stdin), &stdout, &stderr)

			store, err := tracefile.New(records)
			require.NoError(t, err)
			found, err := store.List(context.Background(), record.TraceFilter{Name: string(trace.NameRun), SessionID: "s1"})
			require.NoError(t, err)
			var runs []run
			for _, r := range trace.RestoreAll(found) {
				runs = append(runs, run{r.Labels, string(r.Input), string(r.Output)})
			}
			assert.Equal(t, 0, code)
			assert.Empty(t, stdout.String())
			assert.Contains(t, stderr.String(), tc.want.stderr)
			assert.Equal(t, tc.want.runs, runs)
		})
	}
}

// The hook input as written or the fixture of that name under testdata/hooks
// {repo} in either stands for the place
func hookFixture(t *testing.T, stdin, repo string) string {
	t.Helper()
	if strings.HasSuffix(stdin, ".json") {
		b, err := os.ReadFile(filepath.Join("testdata", "hooks", stdin))
		require.NoError(t, err)
		stdin = string(b)
	}
	return strings.ReplaceAll(stdin, "{repo}", repo)
}

// The stop hook starts the extraction of the previous run only when the conversation inferred a correction of it
func TestRunHookStopExtract(t *testing.T) {
	type args struct {
		// Stops of the first turn a stop hook continued after its answer and before its verdicts
		questions int
		// The feedback flags recorded in order on the first answer
		verdicts [][]string
		session  string
		// Answers after the first one
		stops int
		// The answers after the first one end turns a stop hook continued
		continued bool
		// The error of the start and none when nil
		startErr error
		// The conversation drafted the lesson of the reject in its turn
		drafted bool
		// The mode of the trace records before the later answers and unchanged when 0
		mode os.FileMode
	}
	type want struct {
		// Runs of session s1 recorded
		runs     int
		extracts bool
		stderr   string
	}
	reject := func(reviewer string) []string {
		return []string{"--verdict", "reject", "--reason-code", "form", "--reason", "the message was too long", "--reviewer", reviewer}
	}
	inferred := [][]string{reject("session")}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"an inferred correction of the previous answer is extracted", args{verdicts: inferred, stops: 1}, want{runs: 2, extracts: true}},
		{"a later answer does not extract it again", args{verdicts: inferred, stops: 2}, want{runs: 3, extracts: true}},
		{"a person's verdict is left to the nod skill", args{verdicts: [][]string{reject("ann")}, stops: 1}, want{runs: 2}},
		{
			"a person's approve wins over a later inferred reject",
			args{verdicts: [][]string{{"--verdict", "approve", "--reviewer", "ann"}, reject("session")}, stops: 1},
			want{runs: 2},
		},
		{"an answer without a verdict extracts nothing", args{stops: 1}, want{runs: 2}},
		{"manual extracts nothing", args{verdicts: inferred, session: "manual", stops: 1}, want{runs: 2}},
		{"immediate of an earlier release extracts like deferred", args{verdicts: inferred, session: "immediate", stops: 1}, want{runs: 2, extracts: true}},
		{
			"a reject a person withdrew is not extracted",
			args{verdicts: [][]string{reject("session"), {"--verdict", "withdraw", "--reviewer", "ann"}}, stops: 1},
			want{runs: 2},
		},
		{"a correction the turn already drafted is not extracted again", args{verdicts: inferred, stops: 1, drafted: true}, want{runs: 2}},
		{
			"the answer before a question a stop hook continued is extracted at the next answer",
			args{questions: 1, verdicts: inferred, stops: 1},
			want{runs: 3, extracts: true},
		},
		{"a stop of a continued turn leaves the extraction to a later answer", args{verdicts: inferred, stops: 1, continued: true}, want{runs: 2}},
		{
			"a failed start still records the answer",
			args{verdicts: inferred, stops: 1, startErr: errors.New("no process")},
			want{runs: 2, extracts: true, stderr: "nodloop hook: no process"},
		},
		{
			"an answer that fails to record still starts the extraction",
			args{verdicts: inferred, stops: 1, mode: 0o400},
			want{runs: 1, extracts: true, stderr: "permission denied"},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.args.mode != 0 && (runtime.GOOS == "windows" || os.Geteuid() == 0) {
				t.Skip("a read only file stops no write on windows or as root")
			}
			home, records, repo := hookHome(t, "use git -C")
			getenv := func(k string) string {
				return map[string]string{"HOME": home, envRecordDir: records, envSession: tc.args.session}[k]
			}
			now := hookClock()
			var started [][]string
			start := func(args []string) error {
				started = append(started, args)
				return tc.args.startErr
			}
			stop := func(answer string, continued bool) string {
				var stderr bytes.Buffer
				stdin := fmt.Sprintf(`{"session_id":"s1","cwd":%q,"last_assistant_message":%q,"stop_hook_active":%t}`, repo, answer, continued)
				require.Equal(t, 0, runHook([]string{"stop"}, getenv, start, now, strings.NewReader(stdin), &bytes.Buffer{}, &stderr))
				return stderr.String()
			}
			stop("committed with a long message", false)
			store, err := tracefile.New(records)
			require.NoError(t, err)
			first, err := store.List(context.Background(), record.TraceFilter{Name: string(trace.NameRun), SessionID: "s1"})
			require.NoError(t, err)
			require.Len(t, first, 1)
			for range tc.args.questions {
				require.Empty(t, stop("Reply keep or drop", true))
			}
			for _, flags := range tc.args.verdicts {
				var stderr bytes.Buffer
				require.Equal(t, 0, runFeedback(slices.Concat([]string{"add", "--trace", first[0].ID}, flags), getenv, now, trace.ProofNone, &bytes.Buffer{}, &stderr), stderr.String())
			}
			drafts := map[bool][][]string{true: {{
				"propose", "--id", "short-msg", "--kind", "judgment", "--content", "keep commit messages to one line",
				"--trace", first[0].ID, "--producer", sessionProducer, "--label", "repo=nodloop",
			}}}[tc.args.drafted]
			for _, draft := range drafts {
				var stderr bytes.Buffer
				require.Equal(t, 0, runKnowledge(draft, getenv, nil, now, atTerminal, &bytes.Buffer{}, &stderr), stderr.String())
			}
			require.NoError(t, os.Chmod(filepath.Join(records, "traces.jsonl"), cmp.Or(tc.args.mode, 0o600)))
			var stderr string
			for i := range tc.args.stops {
				stderr += stop(fmt.Sprintf("answer %d", i), tc.args.continued)
			}

			runs, err := store.List(context.Background(), record.TraceFilter{Name: string(trace.NameRun), SessionID: "s1"})
			require.NoError(t, err)
			extracted := map[bool][][]string{true: {{"knowledge", "extract", "--from", first[0].ID}}}[tc.want.extracts]
			assert.Contains(t, stderr, tc.want.stderr)
			assert.Len(t, runs, tc.want.runs)
			assert.Equal(t, extracted, started)
		})
	}
}

// The stop hook looks for the previous run of the session within sessionWindow and reads its verdicts from the run on
func TestRunHookStopWindow(t *testing.T) {
	first := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	type args struct {
		// From the first answer to the inferred reject of it
		verdict time.Duration
		// From the first answer to the next one
		next time.Duration
	}
	type want struct {
		extracts bool
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a previous run within the window is extracted", args{time.Minute, 24 * time.Hour}, want{true}},
		{"a previous run older than the window is no previous run", args{time.Minute, 8 * 24 * time.Hour}, want{false}},
		{"a verdict stamped before the run is not read", args{-time.Minute, 24 * time.Hour}, want{false}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, records, repo := hookHome(t, "use git -C")
			getenv := func(k string) string { return map[string]string{"HOME": home, envRecordDir: records}[k] }
			at := first
			now := func() time.Time { return at }
			var started [][]string
			start := func(args []string) error {
				started = append(started, args)
				return nil
			}
			stdin := `{"session_id":"s1","cwd":"` + repo + `","last_assistant_message":"done"}`
			require.Equal(t, 0, runHook([]string{"stop"}, getenv, start, now, strings.NewReader(stdin), &bytes.Buffer{}, &bytes.Buffer{}))
			store, err := tracefile.New(records)
			require.NoError(t, err)
			runs, err := store.List(context.Background(), record.TraceFilter{Name: string(trace.NameRun), SessionID: "s1"})
			require.NoError(t, err)
			require.Len(t, runs, 1)
			at = first.Add(tc.args.verdict)
			var stderr bytes.Buffer
			require.Equal(t, 0, runFeedback([]string{"add", "--trace", runs[0].ID, "--verdict", "reject", "--reason", "too long", "--reviewer", "session"},
				getenv, now, trace.ProofNone, &bytes.Buffer{}, &stderr), stderr.String())
			at = first.Add(tc.args.next)

			code := runHook([]string{"stop"}, getenv, start, now, strings.NewReader(stdin), &bytes.Buffer{}, &stderr)

			assert.Equal(t, 0, code)
			assert.Empty(t, stderr.String())
			assert.Equal(t, tc.want.extracts, len(started) == 1, "started %v", started)
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
	type want struct {
		code   int
		stdout string
		stderr string
	}
	tcs := []struct {
		name string
		args []string
		want want
	}{
		{
			"the text report counts the run and the item it followed",
			[]string{"loop"},
			want{stdout: "window\tall\nloop\truns 2\tjudged 1\tnamed 0\tinferred 0\tcorrected 0\twaiting 0\tapproved 1\n" +
				"scope\tunknown\titems 1\tsingle session 0\tnever applied 0\n" +
				"git-c\tv1\tapplied 1\tfollowed 1 of 1\trepeat 0\tnamed followed 0 of 0\tnamed repeat 0\tinferred followed 0 of 0\tinferred repeat 0\tsettle -\n" +
				"misapplied\tnot measured: no label says which runs an item should have reached\n"},
		},
		{
			"the JSON report holds the same totals and item",
			[]string{"loop", "--json"},
			want{stdout: `{"totals":{"runs":2,"judged":1,"named":0,"inferred":0,"corrected":0,"waiting":0,"approved":1},` +
				`"scopes":[{"version":"unknown","items":1,"single_session":0,"never_applied":0}],"drafts":[],` +
				`"items":[{"id":"git-c","version":1,"applied":1,"judged":1,"followed":1,"repeat":0,` +
				`"named_judged":0,"named_followed":0,"named_repeat":0,` +
				`"inferred_judged":0,"inferred_followed":0,"inferred_repeat":0,"settle_ns":0}]}` + "\n"},
		},
		{"an unknown flag fails", []string{"loop", "--nope"}, want{code: 1, stderr: "flag provided but not defined: -nope"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, records, repo := hookHome(t, "use git -C")
			getenv := func(k string) string { return map[string]string{"HOME": home, envRecordDir: records}[k] }
			now := hookClock()
			stdin := `{"session_id":"s2","cwd":"` + repo + `","last_assistant_message":"git -C repo status"}`
			require.Equal(t, 0, runHook([]string{"stop"}, getenv, nil, now, strings.NewReader(stdin), &bytes.Buffer{}, &bytes.Buffer{}))
			store, err := tracefile.New(records)
			require.NoError(t, err)
			runs, err := store.List(context.Background(), record.TraceFilter{Name: string(trace.NameRun), SessionID: "s2"})
			require.NoError(t, err)
			require.Len(t, runs, 1)
			var stdout, stderr bytes.Buffer
			require.Equal(t, 0, runFeedback([]string{"add", "--trace", runs[0].ID, "--verdict", "approve", "--reviewer", "ann"}, getenv, now, atTerminal, &bytes.Buffer{}, &stderr), stderr.String())

			code := runReport(tc.args, getenv, now, &stdout, &stderr)

			assert.Equal(t, tc.want.code, code, stderr.String())
			assert.Equal(t, tc.want.stdout, stdout.String())
			assert.Contains(t, stderr.String(), tc.want.stderr)
		})
	}
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
		require.Equal(t, 0, runFeedback([]string{"outcome", "--trace", id, "--result", result, "--reviewer", "ann"}, getenv, time.Now, trace.ProofNone, &bytes.Buffer{}, &stderr), stderr.String())
	}
	knowledgeRun := func(args ...string) (int, string, string) {
		var stdout, stderr bytes.Buffer
		code := runKnowledge(args, getenv, nil, time.Now, atTerminal, &stdout, &stderr)
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
	type want struct {
		// The ids of the items shown and recorded
		shown []string
		// The count line of the items left out and empty for none
		leftOut string
	}
	counted := regexp.MustCompile(`- \d+ more items left out over the size cap\n`)
	tcs := []struct {
		name string
		args hookItems
		want want
	}{
		{"items past the cap are left out and counted", hookItems{item("a", 6000), item("b", 6000), item("c", 10)},
			want{[]string{"a"}, "- 2 more items left out over the size cap\n"}},
		{"items under the cap are all shown and none counted", hookItems{item("a", 10), item("b", 10)}, want{[]string{"a", "b"}, ""}},
		{"no items show nothing", nil, want{}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			shown := tc.args.fitting()
			text := tc.args.context(nil, nil, sessionNote{})

			var ids []string
			for _, k := range shown {
				ids = append(ids, k.ID)
			}
			assert.Equal(t, tc.want.shown, ids)
			assert.Equal(t, tc.want.leftOut, counted.FindString(text))
			assert.Less(t, len([]rune(text)), 10_000)
		})
	}
}

// A prompt after versions in the context adds only the fitting versions not among them
func TestHookItemsContextDelta(t *testing.T) {
	item := func(id string, version, size int) knowledge.Knowledge {
		return knowledge.Knowledge{ID: id, Version: version, Kind: knowledge.KindJudgment, Content: strings.Repeat("x", size)}
	}
	type args struct {
		items     hookItems
		inContext []knowledge.Ref
	}
	note := sessionNote{run: "run-1"}
	tcs := []struct {
		name string
		args args
		// The whole context with {note} for the note text
		want string
	}{
		{
			"none in the context adds every item under the prompt lead",
			args{hookItems{item("a", 1, 3), item("b", 1, 3)}, nil},
			knowledge.PromptLead + "- [a v1 judgment] xxx\n- [b v1 judgment] xxx\n{note}",
		},
		{
			"every version in the context adds the note alone",
			args{hookItems{item("a", 1, 3)}, []knowledge.Ref{{ID: "a", Version: 1}}},
			"{note}",
		},
		{
			"a new item comes under the delta lead",
			args{hookItems{item("a", 1, 3), item("b", 1, 3)}, []knowledge.Ref{{ID: "a", Version: 1}}},
			knowledge.DeltaLead + "- [b v1 judgment] xxx\n{note}",
		},
		{
			"a new version comes under the delta lead to replace the earlier line",
			args{hookItems{item("a", 2, 3)}, []knowledge.Ref{{ID: "a", Version: 1}}},
			knowledge.DeltaLead + "- [a v2 judgment] xxx\n{note}",
		},
		{
			"an item past the cap is never added and never counted again",
			args{hookItems{item("a", 1, 6000), item("b", 1, 6000)}, []knowledge.Ref{{ID: "a", Version: 1}}},
			"{note}",
		},
		{
			"versions of items no longer in the place add nothing",
			args{nil, []knowledge.Ref{{ID: "a", Version: 1}}},
			"{note}",
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.args.items.context(tc.args.inContext, nil, note)

			assert.Equal(t, strings.ReplaceAll(tc.want, "{note}", note.text()), got)
		})
	}
}

// The versions in the context are what the previous run applied less the ones approved during its turn or what a later refill added
func TestSessionTurnsInContext(t *testing.T) {
	before := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	run := func(input string, at time.Time) trace.Trace {
		return trace.Trace{ID: "r", Name: trace.NameRun, Input: json.RawMessage(input), Time: at}
	}
	refill := func(input string, at time.Time) trace.Trace {
		return trace.Trace{ID: "f", Name: trace.NameRefill, Input: json.RawMessage(input), Time: at}
	}
	applied := `{"applied":[{"id":"a","version":1},{"id":"b","version":1}]}`
	refilled := `{"applied":[{"id":"c","version":2}]}`
	items := knowledge.Set{
		{ID: "a", Version: 1, Status: knowledge.StatusApproved, ApprovedAt: before.Add(-time.Hour)},
		{ID: "b", Version: 1, Status: knowledge.StatusApproved, ApprovedAt: before.Add(time.Minute)},
	}
	type args struct {
		runs   sessionTurns
		refill trace.Trace
	}
	tcs := []struct {
		name string
		args args
		want []knowledge.Ref
	}{
		{"no run has nothing in the context", args{nil, trace.Trace{}}, nil},
		{
			"a first run counts all it applied",
			args{sessionTurns{run(applied, before.Add(2*time.Minute))}, trace.Trace{}},
			[]knowledge.Ref{{ID: "a", Version: 1}, {ID: "b", Version: 1}},
		},
		{
			"a version approved after the run before is added again",
			args{sessionTurns{run(applied, before.Add(2*time.Minute)), run(applied, before)}, trace.Trace{}},
			[]knowledge.Ref{{ID: "a", Version: 1}},
		},
		{
			"a version no longer in the place stays in the context",
			args{sessionTurns{run(`{"applied":[{"id":"c","version":1}]}`, before.Add(time.Minute)), run(applied, before)}, trace.Trace{}},
			[]knowledge.Ref{{ID: "c", Version: 1}},
		},
		{"an unreadable input names nothing", args{sessionTurns{run(`{"applied":7}`, before)}, trace.Trace{}}, nil},
		{"a refill of a cleared session with no run is the context", args{nil, refill(refilled, before)}, []knowledge.Ref{{ID: "c", Version: 2}}},
		{
			"a refill after the newest run is the context",
			args{sessionTurns{run(applied, before)}, refill(refilled, before.Add(time.Minute))},
			[]knowledge.Ref{{ID: "c", Version: 2}},
		},
		{
			"a run after the refill applied what the context holds",
			args{sessionTurns{run(applied, before.Add(2*time.Minute))}, refill(refilled, before.Add(time.Minute))},
			[]knowledge.Ref{{ID: "a", Version: 1}, {ID: "b", Version: 1}},
		},
		{"an unreadable refill names nothing", args{nil, refill(`{"applied":7}`, before)}, nil},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.runs.inContext(items, tc.args.refill))
		})
	}
}

// The note follows the items only in the room left under the context limit
func TestHookItemsContextNote(t *testing.T) {
	item := func(size int) hookItems {
		return hookItems{{ID: "a", Version: 1, Kind: knowledge.KindJudgment, Content: strings.Repeat("x", size)}}
	}
	note := sessionNote{run: "run-1"}
	type want struct {
		// The context holds the note
		note bool
		// The context opens with the prompt lead
		lead bool
	}
	tcs := []struct {
		name string
		args hookItems
		want want
	}{
		{"no items leaves the note alone", nil, want{note: true}},
		{"a small item leaves room", item(100), want{note: true, lead: true}},
		{"an item at the prompt cap leaves none", item(promptRunes - 250), want{lead: true}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.args.context(nil, nil, note)

			assert.Equal(t, tc.want.note, strings.Contains(got, note.text()))
			assert.LessOrEqual(t, len([]rune(got)), contextRunes)
			assert.Equal(t, tc.want.lead, strings.HasPrefix(got, knowledge.PromptLead))
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
	run := hookPromptSetup(t, getenv, hookClock(), repo, false, true, false)
	require.Empty(t, run)
	store, err := tracefile.New(records)
	require.NoError(t, err)
	runs, err := store.List(context.Background(), record.TraceFilter{Name: string(trace.NameRun)})
	require.NoError(t, err)
	type want struct {
		code   int
		stdout string
		stderr string
	}
	tcs := []struct {
		name string
		args []string
		want want
	}{
		{"the repo waits for the candidate", []string{"waiting", "--producer", "session", "--label", "repo=nodloop"},
			want{stdout: "short-msg\tv1\tjudgment\tkeep commit messages to one line\tscope runs of session. repo=nodloop\tfrom " + runs[0].ID +
				"\treplay none\n"}},
		{"another repo waits for nothing", []string{"waiting", "--producer", "session", "--label", "repo=other"}, want{}},
		{"no producer is refused", []string{"waiting", "--label", "repo=nodloop"}, want{code: 1, stderr: "waiting: --producer"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			// The cases only read the records they share
			t.Parallel()
			var stdout, stderr bytes.Buffer

			code := runKnowledge(tc.args, getenv, nil, hookClock(), atTerminal, &stdout, &stderr)

			assert.Equal(t, tc.want.code, code, stderr.String())
			assert.Equal(t, tc.want.stdout, stdout.String())
			assert.Contains(t, stderr.String(), tc.want.stderr)
		})
	}
}

// approve takes the scope the person picked over the proposed one
func TestRunKnowledgeApproveIn(t *testing.T) {
	type args struct {
		flags []string
	}
	type want struct {
		code   int
		stderr string
		// Whether a prompt in another repo receives the item afterwards
		elsewhere bool
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"as proposed it stays in the repo", args{}, want{}},
		{"everywhere reaches another repo", args{[]string{"--everywhere"}}, want{elsewhere: true}},
		{"a recorded label approves it there", args{[]string{"--label", "repo=nodloop"}}, want{}},
		{"a label no run carries is refused", args{[]string{"--label", "repo=other"}}, want{code: 1, stderr: "no run of session carries repo=other"}},
		{"both flags are refused", args{[]string{"--everywhere", "--label", "repo=nodloop"}}, want{code: 1, stderr: "--label and --everywhere exclude each other"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, records, repo := hookHome(t, "use git -C")
			getenv := func(k string) string { return map[string]string{"HOME": home, envRecordDir: records}[k] }
			hookPromptSetup(t, getenv, time.Now, repo, false, true, false)
			var stdout, stderr bytes.Buffer

			code := runKnowledge(slices.Concat([]string{"approve", "short-msg", "--version", "1", "--approver", "ann"}, tc.args.flags),
				getenv, nil, time.Now, atTerminal, &stdout, &stderr)

			assert.Equal(t, tc.want.code, code, stderr.String())
			assert.Contains(t, stderr.String(), tc.want.stderr)
			var items bytes.Buffer
			require.Equal(t, 0, runKnowledge([]string{"for", "--producer", "session", "--label", "repo=other"}, getenv, nil, time.Now, atTerminal, &items, &stderr))
			assert.Equal(t, tc.want.elsewhere, strings.Contains(items.String(), "short-msg"), items.String())
		})
	}
}

// A session the holdout draws gets no item in any prompt and every run of it records them as withheld with its draw
func TestRunHookHoldout(t *testing.T) {
	require.True(t, knowledge.Holdout(0.999).WithholdsRun(sessionProducer, "s1"))
	gitC := []knowledge.Ref{{ID: "git-c", Version: 1}}
	type want struct {
		// The inputs of the three turns newest first without the plugin version
		inputs []knowledge.RunInput
		// The prompts that got the item
		items int
	}
	tcs := []struct {
		name string
		// The holdout share
		args string
		want want
	}{
		{
			"both turns of a drawn session are withheld in one unit and a turn without a session is never drawn",
			"0.999",
			want{[]knowledge.RunInput{
				{Applied: gitC},
				{Applied: []knowledge.Ref{}, Withheld: gitC, Drawn: true, Share: 0.999, Scheme: knowledge.DrawScheme, Unit: "d698df9d2c5b9655"},
				{Applied: []knowledge.Ref{}, Withheld: gitC, Drawn: true, Share: 0.999, Scheme: knowledge.DrawScheme, Unit: "d698df9d2c5b9655"},
			}, 1},
		},
		{
			"no holdout applies the item in every turn and draws none",
			"0",
			want{[]knowledge.RunInput{{Applied: gitC}, {Applied: gitC}, {Applied: gitC}}, 2},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, records, repo := hookHome(t, "use git -C")
			require.Equal(t, 0, runConfig([]string{"holdout", tc.args}, func(k string) string { return map[string]string{"HOME": home}[k] }, atTerminal, &bytes.Buffer{}, &bytes.Buffer{}))
			getenv := func(k string) string { return map[string]string{"HOME": home, envRecordDir: records}[k] }
			now := hookClock()
			var prompts bytes.Buffer
			for _, session := range []string{"s1", "s1", ""} {
				stdin := `{"session_id":"` + session + `","cwd":"` + repo + `","last_assistant_message":"done"}`
				require.Equal(t, 0, runHook([]string{"prompt"}, getenv, nil, now, strings.NewReader(stdin), &prompts, &bytes.Buffer{}))
				require.Equal(t, 0, runHook([]string{"stop"}, getenv, nil, now, strings.NewReader(stdin), &bytes.Buffer{}, &bytes.Buffer{}))
			}

			store, err := tracefile.New(records)
			require.NoError(t, err)
			runs, err := store.List(context.Background(), record.TraceFilter{Name: string(trace.NameRun)})
			require.NoError(t, err)
			require.Len(t, runs, 4, "the three turns after the run of the home")
			var got []knowledge.RunInput
			for _, run := range runs[:3] {
				var in knowledge.RunInput
				require.NoError(t, json.Unmarshal(run.Input, &in))
				in.Plugin = ""
				got = append(got, in)
			}
			assert.Equal(t, tc.want.inputs, got)
			assert.Equal(t, tc.want.items, strings.Count(prompts.String(), "use git -C"))
		})
	}
}

// A version nodloop approved that the item hold draws for a session is left out of every hook of it
// Until curate.json turns the hold on every hook writes the bytes it wrote before the hold
func TestRunHookItemHold(t *testing.T) {
	prC := knowledge.Ref{ID: "pr-c", Version: 1}
	require.True(t, knowledge.ItemHold(0.1).Holds(sessionProducer, "s34", prC), "pr-c draws 0.054 in s34")
	require.False(t, knowledge.ItemHold(0.1).Holds(sessionProducer, "s1", prC), "pr-c draws 0.932 in s1")
	// pr-c approved by nodloop after git-c so it comes first in the For order
	const nodloopItem = `{"id":"pr-c","version":1,"kind":"judgment","content":"write the motivation first",` +
		`"run":{"producer":"session","labels":{"repo":["nodloop"]}},"evidence":{"feedback_trace_ids":["t1"]},"basis":"stated",` +
		`"status":"approved","approver":"nodloop","approved_at":"2026-10-02T00:30:00Z","time":"2026-10-02T00:30:00Z","author":"nodloop"}` + "\n"
	type args struct {
		// The content of curate.json, none when empty and a directory for dir
		mark    string
		session string
		// The holdout share
		holdout string
	}
	type want struct {
		// The output of the prompt, stop, second prompt, subagent and compact start hooks in that order
		outputs []string
		// The input of the run the stop recorded as stored
		input string
		// Lines on stderr, each one the unreadable mark
		errors int
	}
	both := knowledge.PromptLead + "- [pr-c v1 judgment] write the motivation first\n- [git-c v1 judgment] use git -C\n"
	person := knowledge.PromptLead + "- [git-c v1 judgment] use git -C\n"
	outputs := func(text string) []string {
		return []string{hookAdded("UserPromptSubmit", text), "", "", hookAdded("SubagentStart", text), hookAdded("SessionStart", text)}
	}
	asBefore := want{outputs(both), `{"applied":[{"id":"pr-c","version":1},{"id":"git-c","version":1}],"plugin":"0.7.0"}`, 0}
	const active = `{"knobs":{"activated":true,"item_hold":0.1}}`
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"no curate.json leaves every hook as before", args{session: "s34", holdout: "0"}, asBefore},
		{"tier 1 in shadow leaves every hook as before", args{`{"knobs":{"activated":false,"item_hold":0.1}}`, "s34", "0"}, asBefore},
		{"a share of 0 leaves every hook as before", args{`{"knobs":{"activated":true,"item_hold":0}}`, "s34", "0"}, asBefore},
		{"an unreadable mark is reported and leaves every hook as before", args{"dir", "s34", "0"}, want{asBefore.outputs, asBefore.input, 5}},
		{
			"a turn without a session is never held",
			args{active, "", "0"},
			want{
				[]string{asBefore.outputs[0], "", asBefore.outputs[0], asBefore.outputs[3], asBefore.outputs[4]},
				asBefore.input, 0,
			},
		},
		{
			"a drawn session misses the version in every hook and its run records the chances",
			args{active, "s34", "0"},
			want{outputs(person), `{"applied":[{"id":"git-c","version":1}],"plugin":"0.7.0",` +
				`"eligible":[{"id":"pr-c","version":1,"p_include":0.9},{"id":"git-c","version":1,"p_include":1}],` +
				`"held":[{"id":"pr-c","version":1}]}`, 0},
		},
		{
			"a session the draw passes gets every version and its run records the chances",
			args{active, "s1", "0"},
			want{outputs(both), `{"applied":[{"id":"pr-c","version":1},{"id":"git-c","version":1}],"plugin":"0.7.0",` +
				`"eligible":[{"id":"pr-c","version":1,"p_include":0.9},{"id":"git-c","version":1,"p_include":1}]}`, 0},
		},
		{
			"a withheld session gets no item and includes none",
			args{active, "s34", "0.999"},
			want{[]string{"", "", "", "", ""}, `{"applied":[],"plugin":"0.7.0",` +
				`"withheld":[{"id":"pr-c","version":1},{"id":"git-c","version":1}],"drawn":true,"share":0.999,"scheme":1,"unit":"7b1eb3ad69b36412",` +
				`"eligible":[{"id":"pr-c","version":1,"p_include":0},{"id":"git-c","version":1,"p_include":0}]}`, 0},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, records, repo := hookHome(t, "use git -C")
			hookAppend(t, filepath.Join(records, "knowledge.jsonl"), nodloopItem)
			hookMark(t, records, tc.args.mark)
			getenv := envOf(map[string]string{"HOME": home, envRecordDir: records, envSession: "manual", envPluginVersion: "0.7.0"})
			require.Equal(t, 0, runConfig([]string{"holdout", tc.args.holdout}, getenv, atTerminal, &bytes.Buffer{}, &bytes.Buffer{}))
			now := hookClock()
			in := `{"session_id":"` + tc.args.session + `","cwd":"` + repo + `","source":"compact","last_assistant_message":"done"}`
			var got []string
			var stderr bytes.Buffer

			for _, action := range []string{"prompt", "stop", "prompt", "subagent", "start"} {
				var stdout bytes.Buffer
				require.Equal(t, 0, runHook([]string{action}, getenv, nil, now, strings.NewReader(in), &stdout, &stderr))
				got = append(got, stdout.String())
			}

			store, err := tracefile.New(records)
			require.NoError(t, err)
			runs, err := store.List(context.Background(), record.TraceFilter{Name: string(trace.NameRun), Limit: 1})
			require.NoError(t, err)
			require.Len(t, runs, 1)
			assert.Equal(t, tc.want.outputs, got)
			assert.Equal(t, tc.want.input, string(runs[0].Input))
			assert.Equal(t, tc.want.errors, strings.Count(stderr.String(), "curate: mark unreadable"))
			assert.Equal(t, tc.want.errors, strings.Count(stderr.String(), "\n"), stderr.String())
		})
	}
}

// Writes curate.json of the record directory, nothing when mark is empty and a directory in its place for dir
func hookMark(t *testing.T, records, mark string) {
	t.Helper()
	path := filepath.Join(records, curate.MarkFile)
	switch mark {
	case "":
	case "dir":
		require.NoError(t, os.Mkdir(path, 0o755))
	default:
		require.NoError(t, os.WriteFile(path, []byte(mark), 0o600))
	}
}

// The output of a Claude Code hook that adds the text on the event
func hookAdded(event, text string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": event, "additionalContext": text}})
	return b.String()
}

// A process with no record directory reads no mark and holds nothing
func TestAppItemHold(t *testing.T) {
	hold, err := app{}.itemHold()

	assert.NoError(t, err)
	assert.Zero(t, hold)
}

// The reaction point judges the user's message on the previous answer before the conversation does
func TestRunHookReaction(t *testing.T) {
	type args struct {
		// The answer of the endpoint and empty for one that fails
		answer string
	}
	// The latest verdict on the previous answer
	type verdict struct {
		verdict feedback.Verdict
		reason  string
	}
	type want struct {
		// The latest verdicts and none when nil
		verdicts []verdict
		// The event of the output and empty for no output
		event string
		// A substring of the added context and one it must not hold
		context string
		absent  string
		stderr  string
	}
	message := "that was wrong, use git -C"
	const event = "UserPromptSubmit"
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a sure correction is recorded as a reject with the message", args{`{"answers":{"corrects":{"noul":0.9},"approves":{"noul":0.1}}}`},
			want{[]verdict{{feedback.VerdictReject, message}}, event, "was recorded as a reject of your previous answer", "call the nodloop feedback tool", ""}},
		{"a sure approval is recorded and adds nothing", args{`{"answers":{"corrects":{"noul":0.1},"approves":{"noul":0.9}}}`},
			want{[]verdict{{feedback.VerdictApprove, ""}}, "", "", "previous answer", ""}},
		{"an answer that says both leaves the conversation to judge", args{`{"answers":{"corrects":{"noul":0.9},"approves":{"noul":0.9}}}`},
			want{nil, event, "call the nodloop feedback tool", "recorded as a reject", ""}},
		{"an unsure answer defers to the conversation", args{`{"answers":{"corrects":{"noul":0.6},"approves":{"noul":0.3}}}`},
			want{nil, event, "call the nodloop feedback tool", "recorded as a reject", ""}},
		{"a failing endpoint defers to the conversation without a warning", args{""},
			want{nil, event, "call the nodloop feedback tool", "recorded as a reject", ""}},
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
			now := hookClock()
			var setStderr bytes.Buffer
			require.Equal(t, 0, runClassifier([]string{"set", "reaction", "--url", srv.URL}, getenv, now, &bytes.Buffer{}, &setStderr), setStderr.String())
			run := hookPromptSetup(t, getenv, now, repo, true, false, false)
			stdin := `{"session_id":"s1","cwd":"` + repo + `","prompt":"` + message + `"}`
			var stdout, stderr bytes.Buffer

			code := runHook([]string{"prompt"}, getenv, nil, now, strings.NewReader(stdin), &stdout, &stderr)

			event, added := hookOutput(t, stdout.Bytes())
			store, err := feedbackfile.New(records)
			require.NoError(t, err)
			recorded, err := store.List(context.Background(), record.FeedbackFilter{TraceID: run})
			require.NoError(t, err)
			var latest []verdict
			for _, f := range feedback.RestoreAll(recorded).Latest() {
				latest = append(latest, verdict{f.Verdict, f.Reason})
			}
			assert.Equal(t, 0, code)
			assert.True(t, strings.HasPrefix(<-states, "## User message\n\n"+message), "a truncating endpoint still reads the message")
			assert.Equal(t, tc.want.event, event)
			assert.Contains(t, added, tc.want.context)
			assert.NotContains(t, added, tc.want.absent)
			assert.Equal(t, tc.want.stderr, stderr.String())
			assert.Equal(t, tc.want.verdicts, latest)
		})
	}
}

// A prompt after stops a stop hook continued names and judges the newest answer before them
// A continued stop answers what the stop asked so a correction in the next prompt is about the answer before it
func TestRunHookContinued(t *testing.T) {
	type want struct {
		// The run oldest first the note names and the reaction point rejects and -1 for none
		judged int
	}
	tcs := []struct {
		name string
		// Whether each stop before the prompt ends a turn a stop hook continued
		args []bool
		want want
	}{
		{"a prompt after an answer names it", []bool{false}, want{0}},
		{"a prompt after a question a stop hook continued names the answer before it", []bool{false, true}, want{0}},
		{"an answer before several continued stops is still named", []bool{false, true, true, true}, want{0}},
		{"the answer of a later turn is named over the earlier one", []bool{false, true, false}, want{2}},
		{"continued stops alone name no run", []bool{true}, want{-1}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"answers":{"corrects":{"noul":0.9},"approves":{"noul":0.1}}}`))
			}))
			t.Cleanup(srv.Close)
			home, records, repo := hookHome(t, "use git -C")
			getenv := func(k string) string { return map[string]string{"HOME": home, envRecordDir: records}[k] }
			now := hookClock()
			var setStderr bytes.Buffer
			require.Equal(t, 0, runClassifier([]string{"set", "reaction", "--url", srv.URL}, getenv, now, &bytes.Buffer{}, &setStderr), setStderr.String())
			for i, continued := range tc.args {
				stdin := fmt.Sprintf(`{"session_id":"s1","cwd":%q,"last_assistant_message":"answer %d","stop_hook_active":%t}`, repo, i, continued)
				require.Equal(t, 0, runHook([]string{"stop"}, getenv, nil, now, strings.NewReader(stdin), &bytes.Buffer{}, &bytes.Buffer{}))
			}
			traces, err := tracefile.New(records)
			require.NoError(t, err)
			found, err := traces.List(context.Background(), record.TraceFilter{Name: string(trace.NameRun), SessionID: "s1"})
			require.NoError(t, err)
			require.Len(t, found, len(tc.args))
			// The run ids oldest first after the empty id of none
			ids := []string{""}
			for _, r := range slices.Backward(found) {
				ids = append(ids, r.ID)
			}
			judged := ids[tc.want.judged+1]
			stdin := fmt.Sprintf(`{"session_id":"s1","cwd":%q,"prompt":"that was wrong, use git -C"}`, repo)
			var stdout, stderr bytes.Buffer

			code := runHook([]string{"prompt"}, getenv, nil, now, strings.NewReader(stdin), &stdout, &stderr)

			_, added := hookOutput(t, stdout.Bytes())
			verdicts, err := feedbackfile.New(records)
			require.NoError(t, err)
			recorded, err := verdicts.List(context.Background(), record.FeedbackFilter{})
			require.NoError(t, err)
			var rejected []string
			for _, f := range feedback.RestoreAll(recorded) {
				rejected = append(rejected, f.TraceID)
			}
			// The run the note names and empty when it names none
			named := regexp.MustCompile(`run ([0-9a-f]*)\.`).FindStringSubmatch(added + "run .")[1]
			assert.Equal(t, 0, code)
			assert.Empty(t, stderr.String())
			assert.Equal(t, judged, named, added)
			assert.Equal(t, map[bool][]string{true: {judged}}[judged != ""], rejected)
		})
	}
}

// The runs from the newest one no stop hook continued back
func TestSessionTurnsAnswered(t *testing.T) {
	run := func(id, input string) trace.Trace {
		return trace.Trace{ID: id, Name: trace.NameRun, Input: json.RawMessage(input)}
	}
	answer := func(id string) trace.Trace { return run(id, `{"applied":[]}`) }
	question := func(id string) trace.Trace { return run(id, `{"applied":[],"continued":true}`) }
	tcs := []struct {
		name string
		args sessionTurns
		// The ids of the runs newest first
		want []string
	}{
		{"no run has no answer", nil, nil},
		{"the newest answer and every run before it", sessionTurns{answer("a2"), question("q1"), answer("a1")}, []string{"a2", "q1", "a1"}},
		{"continued runs before the answer are left out", sessionTurns{question("q2"), question("q1"), answer("a1"), answer("a0")}, []string{"a1", "a0"}},
		{"continued runs alone have no answer", sessionTurns{question("q1")}, nil},
		{"an input that does not decode reads as an answer", sessionTurns{question("q1"), run("r0", `{"applied":7}`)}, []string{"r0"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var got []string
			for _, r := range tc.args.answered() {
				got = append(got, r.ID)
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

// A corrupt line in the records leaves out its record and the hooks go on with the rest
func TestRunHookCorruptLine(t *testing.T) {
	type args struct {
		// The lines appended to knowledge.jsonl and traces.jsonl
		knowledge, traces string
	}
	type want struct {
		// Substrings of stderr of the prompt and the stop hook
		promptErr, stopErr string
		listErr            error
	}
	const corrupt = "not json\n"
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"corrupt lines in both files are named and the hooks go on",
			args{corrupt, corrupt},
			want{"knowledge.jsonl has corrupt lines", "traces.jsonl has corrupt lines", jsonl.ErrCorrupt},
		},
		{"a corrupt knowledge line leaves the traces whole", args{knowledge: corrupt}, want{"knowledge.jsonl has corrupt lines", "knowledge.jsonl has corrupt lines", nil}},
		{"a corrupt trace line leaves the items whole", args{traces: corrupt}, want{"traces.jsonl has corrupt lines", "traces.jsonl has corrupt lines", jsonl.ErrCorrupt}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, records, repo := hookHome(t, "use git -C")
			hookAppend(t, filepath.Join(records, "knowledge.jsonl"), tc.args.knowledge)
			hookAppend(t, filepath.Join(records, "traces.jsonl"), tc.args.traces)
			getenv := func(k string) string { return map[string]string{"HOME": home, envRecordDir: records}[k] }
			now := hookClock()
			stdin := `{"session_id":"s1","cwd":"` + repo + `","last_assistant_message":"done"}`
			var prompt, promptErr, stopErr bytes.Buffer

			promptCode := runHook([]string{"prompt"}, getenv, nil, now, strings.NewReader(stdin), &prompt, &promptErr)
			stopCode := runHook([]string{"stop"}, getenv, nil, now, strings.NewReader(stdin), &bytes.Buffer{}, &stopErr)

			store, err := tracefile.New(records)
			require.NoError(t, err)
			runs, err := store.List(context.Background(), record.TraceFilter{Name: string(trace.NameRun), SessionID: "s1"})
			assert.Equal(t, 0, promptCode)
			assert.Equal(t, 0, stopCode)
			assert.Contains(t, prompt.String(), "use git -C")
			assert.Contains(t, promptErr.String(), tc.want.promptErr)
			assert.Contains(t, stopErr.String(), tc.want.stopErr)
			assert.ErrorIs(t, err, tc.want.listErr)
			assert.Len(t, runs, 1, "the stop hook still records the answer")
		})
	}
}

// The run of each stop records how long its turn took
func TestRunHookTurnDuration(t *testing.T) {
	// One hook call after the clock moved by wait
	type step struct {
		action    string
		wait      time.Duration
		continued bool
	}
	tcs := []struct {
		name string
		args []step
		// The duration of each run oldest first
		want []int64
	}{
		{"a turn lasts from its prompt to its stop", []step{{"prompt", 0, false}, {"stop", 90 * time.Second, false}}, []int64{90_000}},
		{"a stop without a prompt records no duration", []step{{"stop", time.Second, false}}, []int64{0}},
		{
			"a turn a stop hook continued lasts from the stop before it",
			[]step{{"prompt", 0, false}, {"stop", 10 * time.Second, false}, {"stop", 5 * time.Second, true}},
			[]int64{10_000, 5_000},
		},
		{
			"a prompt before the previous stop starts no later turn",
			[]step{{"prompt", 0, false}, {"stop", 10 * time.Second, false}, {"stop", 5 * time.Second, false}},
			[]int64{10_000, 0},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, records, repo := hookHome(t, "use git -C")
			getenv := func(k string) string { return map[string]string{"HOME": home, envRecordDir: records}[k] }
			at := time.Date(2026, 10, 2, 1, 0, 0, 0, time.UTC)
			now := func() time.Time { return at }

			for _, s := range tc.args {
				at = at.Add(s.wait)
				stdin := fmt.Sprintf(`{"session_id":"s1","cwd":%q,"prompt":"go on","last_assistant_message":"done","stop_hook_active":%t}`, repo, s.continued)
				var stdout, stderr bytes.Buffer
				require.Equal(t, 0, runHook([]string{s.action}, getenv, nil, now, strings.NewReader(stdin), &stdout, &stderr))
				require.Empty(t, stderr.String())
			}

			store, err := tracefile.New(records)
			require.NoError(t, err)
			found, err := store.List(context.Background(), record.TraceFilter{Name: string(trace.NameRun), SessionID: "s1"})
			require.NoError(t, err)
			var got []int64
			for _, r := range slices.Backward(found) {
				got = append(got, r.DurationMS)
			}
			assert.Equal(t, tc.want, got)
		})
	}
}
