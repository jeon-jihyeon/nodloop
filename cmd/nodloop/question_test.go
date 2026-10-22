package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	knowledgefile "github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/schema"
	"github.com/jeon-jihyeon/nodloop/internal/textkey"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
)

// A record directory where a curate pass left a direction question about k-wide of repo svc and a shadow decision about k-add of repo acme
type questionBench struct {
	vars    map[string]string
	records string
	// Fixture checkouts with work outside every lesson scope
	svc, acme, work string
	now             func() time.Time
}

// A checkout of the repo in a temp directory
func gitRepo(t *testing.T, name string) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0o755))
	return repo
}

func newQuestionBench(t *testing.T, mode string) questionBench {
	t.Helper()
	getenv, records := curateHome(t, [][3]string{
		{"k-add", "Name the failing test first", "repo=acme"},
		{"k-wide", "Use the staging cluster", "repo=svc"},
	})
	var stderr bytes.Buffer
	require.Equal(t, 0, runKnowledge([]string{"curate"}, getenv, curateClient(t), func() time.Time { return curateNow }, trace.ProofNone, &bytes.Buffer{}, &stderr),
		stderr.String())
	saveMode(t, getenv("HOME"), mode)
	vars := map[string]string{"HOME": getenv("HOME"), envRecordDir: records}
	at := curateNow.Add(time.Hour)
	now := func() time.Time {
		at = at.Add(time.Second)
		return at
	}
	return questionBench{vars: vars, records: records, svc: gitRepo(t, "svc"), acme: gitRepo(t, "acme"), work: gitRepo(t, "work"), now: now}
}

func (b questionBench) getenv(k string) string {
	return b.vars[k]
}

// Runs one hook of the host over the input with {repo} for the checkout and the starter given
func (b questionBench) hook(t *testing.T, action, host, stdin, repo string, start starter) (stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := runHook([]string{action, "--host", host}, b.getenv, start, b.now, strings.NewReader(b.input(t, stdin, repo)), &out, &errOut)
	require.Equal(t, 0, code)
	return out.String(), errOut.String()
}

func (b questionBench) input(t *testing.T, input, repo string) string {
	t.Helper()
	input = hookFixture(t, input, repo)
	var in hookInput
	require.NoError(t, json.Unmarshal([]byte(input), &in))
	path := filepath.Join(b.getenv("HOME"), ".claude", "projects", "acme", in.SessionID+".jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		require.NoError(t, os.WriteFile(path, nil, 0o600))
	}
	return strings.ReplaceAll(input, "{transcript}", filepath.ToSlash(path))
}

func proposeQuestion(t *testing.T, b questionBench) {
	t.Helper()
	var out, stderr bytes.Buffer
	payload := `{"action":"judge","target":"k-add:1","choice":"keep","session":"s1"}`
	code := runIntervene([]string{"--payload", "-"}, b.getenv, b.now, trace.ProofNone, strings.NewReader(payload), &out, &stderr)
	require.Equal(t, 0, code, stderr.String())
}

func (b questionBench) legacy(t *testing.T) {
	t.Helper()
	b.hook(t, "stop", "claude-code", questionStop, b.svc, noStart)
	a := app{cfg: config{recordDir: b.records}, now: b.now}
	ledger, err := a.ledger()
	require.NoError(t, err)
	all, err := ledger.All(context.Background())
	require.NoError(t, err)
	mark, err := curate.ReadMark(b.records)
	require.NoError(t, err)
	store, err := a.traces()
	require.NoError(t, err)
	k, err := all.Version("k-wide", 1)
	require.NoError(t, err)
	draw := trace.NewDraw(mark.CycleID, mark.PolicyVersion, mark.Seed, "d-legacy", "direction", 1)
	decision, err := (curate.Event{ID: "d-legacy", Ref: k.Evidence.FeedbackTraceIDs[0], Source: trace.SourcePush, Draw: draw}).Decide(
		curate.Change{Kind: curate.ChangeAdd, ID: k.ID, Version: k.Version},
		curate.Decision{Tier: 2, RuleCode: curate.RuleScopeWider, Action: curate.ActionAsk}, nil, b.now())
	require.NoError(t, err)
	require.NoError(t, store.Append(context.Background(), decision.Record()))
	mark.Due = []curate.Due{{Kind: curate.QuestionDirection, Decide: decision.ID, Change: curate.ChangeAdd, Ref: knowledge.Ref{ID: k.ID, Version: 1}}}
	key, err := textkey.Load(b.records)
	require.NoError(t, err)
	iv, err := a.intervener()
	require.NoError(t, err)
	shown, err := iv.Ask(context.Background(), curate.Asking{
		Session: "s1", Run: b.traces(t, trace.NameRun)[0].ID, Mark: mark, All: all, Pushed: true, Seal: key.Sum,
	})
	require.NoError(t, err)
	require.NotEmpty(t, shown.Expose.ID)
}

// The traces of the name newest first
func (b questionBench) traces(t *testing.T, name trace.Name) trace.Traces {
	t.Helper()
	store, err := tracefile.New(b.records)
	require.NoError(t, err)
	found, err := store.List(context.Background(), record.TraceFilter{Name: string(name)})
	require.NoError(t, err)
	return trace.RestoreAll(found)
}

// The newest expose as the stop recorded it
func (b questionBench) exposed(t *testing.T) (trace.Trace, curate.Exposure) {
	t.Helper()
	exposes := b.traces(t, trace.NameExpose)
	require.NotEmpty(t, exposes)
	var x curate.Exposure
	require.NoError(t, json.Unmarshal(exposes[0].Input, &x))
	return exposes[0], x
}

// The Stop additionalContext a stop answer holds and the decision it names
func stopAnswer(t *testing.T, stdout string) (text, decision string) {
	t.Helper()
	if stdout == "" {
		return "", ""
	}
	var answer struct {
		Decision string `json:"decision"`
		Output   struct {
			Context string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &answer), stdout)
	return answer.Output.Context, answer.Decision
}

const questionStop = `{"session_id":"s1","cwd":"{repo}","last_assistant_message":"done","transcript_path":"{transcript}"}`

// An ordinary candidate never interrupts the session
func TestRunHookStopQuestion(t *testing.T) {
	tcs := []struct{ name, input string }{
		{"a host transcript leaves unrequested candidates pending", questionStop},
		{"a missing transcript shows nothing", `{"session_id":"s1","cwd":"{repo}","last_assistant_message":"done"}`},
		{"a continued turn shows nothing", `{"session_id":"s1","cwd":"{repo}","last_assistant_message":"done","stop_hook_active":true}`},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := newQuestionBench(t, "")
			first, firstErr := b.hook(t, "stop", "claude-code", tc.input, b.svc, noStart)
			second, secondErr := b.hook(t, "stop", "claude-code", tc.input, b.svc, noStart)
			assert.Empty(t, first+second)
			assert.Empty(t, firstErr+secondErr)
			assert.Empty(t, b.traces(t, trace.NameExpose))
			assert.Empty(t, b.traces(t, trace.NameAnswer))
		})
	}
}

// Preparation alone spends no exposure or retry round
func TestRunHookStopQuestionExpose(t *testing.T) {
	b := newQuestionBench(t, "manual")
	proposeQuestion(t, b)
	var out bytes.Buffer
	var stderr bytes.Buffer
	input := b.input(t, questionStop, b.acme)
	code := runHook([]string{"stop"}, b.getenv, noStart, b.now, strings.NewReader(input), &out, &stderr)
	require.Equal(t, 0, code, stderr.String())
	text, decision := stopAnswer(t, out.String())
	assert.Empty(t, decision)
	assert.Contains(t, text, "AskUserQuestion")
	assert.Contains(t, text, "without asking a substitute question")
	assert.Empty(t, b.traces(t, trace.NameExpose))
	assert.Empty(t, b.traces(t, trace.NameAnswer))
}

// A proposal is prepared only where the capture can read the tool call that asks it
func TestRunHookStopQuestionUntranscribed(t *testing.T) {
	b := newQuestionBench(t, "manual")
	proposeQuestion(t, b)

	out, failed := b.hook(t, "stop", "claude-code", `{"session_id":"s1","cwd":"{repo}","last_assistant_message":"done"}`, b.acme, noStart)

	assert.Empty(t, failed)
	assert.NotContains(t, out, "AskUserQuestion")
	assert.Empty(t, b.traces(t, trace.NameExpose))
}

// The stop talks to the person only when everything before it went through and only through the one path of the place and mode
// A requested manual proposal needs a completed turn and a kept transcript
func TestRunHookStopQuestionRules(t *testing.T) {
	type args struct {
		input, mode string
		proposal    bool
	}
	tcs := []struct {
		name string
		args args
		want bool
	}{
		{"manual request prepares one question", args{questionStop, "manual", true}, true},
		{"deferred manual request prepares one question", args{questionStop, "", true}, true},
		{"no request leaves candidates pending", args{questionStop, "", false}, false},
		{"no transcript leaves a manual proposal pending", args{`{"session_id":"s1","cwd":"{repo}","last_assistant_message":"done"}`, "manual", true}, false},
		{"another session leaves a manual proposal pending", args{strings.Replace(questionStop, "s1", "s2", 1), "manual", true}, false},
		{"a continued turn prepares no question", args{`{"session_id":"s1","cwd":"{repo}","last_assistant_message":"done","stop_hook_active":true}`, "manual", true}, false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := newQuestionBench(t, tc.args.mode)
			if tc.args.proposal {
				proposeQuestion(t, b)
			}
			stdout, stderr := b.hook(t, "stop", "claude-code", tc.args.input, b.acme, noStart)
			text, _ := stopAnswer(t, stdout)
			assert.Empty(t, stderr)
			assert.Equal(t, tc.want, text != "")
			assert.Empty(t, b.traces(t, trace.NameExpose))
		})
	}
}

// The first word of the next prompt that names a choice of the question is never judged and records no answer
// 1. the prompt gets the reply note in place of the reaction note
// 2. only a matching reply in the same answer boundary replaces the reaction note
// 3. any process can pipe a prompt into the hook so nothing it reads there decides for the person
func TestRunHookPromptReply(t *testing.T) {
	type args struct {
		prompt string
		// A turn the session took between the question and the prompt
		between bool
	}
	tcs := []struct {
		name string
		args args
		want string
	}{
		{
			"a reply word gets the reply note and decides nothing",
			args{`{"session_id":"s1","cwd":"{repo}","prompt":"Keep. And show the cluster list"}`, false},
			"nodloop: the user's first word keep answers the nodloop question and nodloop records the answer after this turn.",
		},
		{"another first word gets the reaction note", args{`{"session_id":"s1","cwd":"{repo}","prompt":"show the cluster list"}`, false}, "nodloop: your previous answer"},
		{"another session gets no note", args{`{"session_id":"s2","cwd":"{repo}","prompt":"keep"}`, false}, ""},
		{"a reply after another answer gets the reaction note", args{`{"session_id":"s1","cwd":"{repo}","prompt":"keep"}`, true}, "nodloop: your previous answer"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := newQuestionBench(t, "")
			b.legacy(t)
			if tc.args.between {
				b.hook(t, "prompt", "claude-code", `{"session_id":"s1","cwd":"{repo}","prompt":"go on"}`, b.svc, noStart)
				b.hook(t, "stop", "claude-code", questionStop, b.svc, noStart)
			}

			out, stderr := b.hook(t, "prompt", "claude-code", tc.args.prompt, b.svc, noStart)

			_, added := hookOutput(t, []byte(out))
			assert.Empty(t, stderr)
			assert.Empty(t, questionAnswers(t, b))
			assert.Equal(t, []string{"k-add v1 candidate", "k-person v1 approved ann", "k-wide v1 candidate"}, curateStatuses(t, b.records))
			assert.Contains(t, added, tc.want)
			assert.Equal(t, tc.want == "", added == "")
		})
	}
}

// The answers of the records as "choice proof person" oldest first
func questionAnswers(t *testing.T, b questionBench) []string {
	t.Helper()
	out := []string{}
	for _, tr := range b.traces(t, trace.NameAnswer) {
		var a curate.Answer
		require.NoError(t, json.Unmarshal(tr.Input, &a))
		out = append([]string{strings.TrimSpace(string(a.Choice) + " " + string(a.Proof) + " " + a.Person)}, out...)
	}
	return out
}

// The reply word the person typed is recorded by the capture of the turn from the transcript Claude Code keeps
// The answer names the expose it answers and the ledger record names the answer under the person
func TestRunHookReplyCaptured(t *testing.T) {
	b := newQuestionBench(t, "")
	b.legacy(t)
	expose, _ := b.exposed(t)
	b.hook(t, "prompt", "claude-code", `{"session_id":"s1","cwd":"{repo}","prompt":"keep, and show the cluster list"}`, b.svc, noStart)
	dir := filepath.Join(b.getenv("HOME"), ".claude", "projects", "-work-svc")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	path := filepath.Join(dir, "s1.jsonl")
	typed := b.traces(t, trace.NameRun)[0].Time.Add(time.Second).Format(time.RFC3339Nano)
	require.NoError(t, os.WriteFile(path, []byte(`{"type":"user","message":{"content":"keep, and show the cluster list"},"timestamp":"`+typed+`"}`+"\n"), 0o600))
	require.Empty(t, b.traces(t, trace.NameAnswer), "the prompt hook records no answer")
	var stdout, stderr bytes.Buffer

	code := runSession([]string{"capture", "--session", "s1", "--transcript", path}, b.getenv, curateClient(t), b.now, noStart, &stdout, &stderr)

	require.Equal(t, 0, code, stderr.String())
	assert.Equal(t, []string{"keep reply person"}, questionAnswers(t, b))
	answers := b.traces(t, trace.NameAnswer)
	require.Len(t, answers, 1)
	items, err := knowledgefile.New(b.records)
	require.NoError(t, err)
	list, err := items.List(context.Background())
	require.NoError(t, err)
	approved, err := knowledge.RestoreAll(list).Version("k-wide", 1)
	require.NoError(t, err)
	assert.Equal(t, []any{expose.ID, trace.SourcePush, answers[0].ID}, []any{answers[0].Ref, answers[0].Source, approved.Decision})
	assert.NoError(t, schema.Validate(string(trace.NameAnswer), answers[0].Record()))
}

// A notice names what curate carried out in the place through systemMessage alone once per session and never beside a question
func TestRunHookStopNotice(t *testing.T) {
	b := newQuestionBench(t, "")
	store, err := tracefile.New(b.records)
	require.NoError(t, err)
	knobs, err := store.List(context.Background(), record.TraceFilter{Name: string(trace.NameKnob)})
	require.NoError(t, err)
	ev, ok := curate.RestoreKnob(trace.Restore(knobs[0]))
	require.True(t, ok)
	ev.Cause, ev.Knobs.Activated, ev.Knobs.Audit = curate.KnobNarrow, true, curate.Audit{}
	activated, err := ev.Trace("0.10.0", curateNow)
	require.NoError(t, err)
	require.NoError(t, store.Append(context.Background(), activated.Record()))
	var stderr bytes.Buffer
	require.Equal(t, 0, runKnowledge([]string{"curate"}, b.getenv, curateClient(t), func() time.Time { return curateNow.Add(time.Minute) }, trace.ProofNone,
		&bytes.Buffer{}, &stderr), stderr.String())

	untranscribed := `{"session_id":"s1","cwd":"{repo}","last_assistant_message":"done"}`
	first, firstErr := b.hook(t, "stop", "claude-code", untranscribed, b.acme, noStart)
	second, _ := b.hook(t, "stop", "claude-code", questionStop, b.acme, noStart)
	codex, _ := b.hook(t, "stop", "codex", strings.Replace(questionStop, "s1", "s2", 1), b.acme, noStart)

	assert.Empty(t, firstErr)
	assert.JSONEq(t, `{"systemMessage":"From your earlier decisions nodloop applied a lesson here: \"Name the failing test first\". `+
		`/nodloop:nod shows them and takes any back"}`, first)
	assert.Empty(t, second, "one notice per session")
	assert.Empty(t, codex, "a host whose replies code cannot read gets no notice")
	expose, x := b.exposed(t)
	assert.Equal(t, []any{curate.QuestionNotice, curate.SurfaceMessage, "s1"}, []any{x.Kind, x.Surface, expose.SessionID})
}

// A failed question preparation preserves the run and reports its failure without asking
func TestRunHookStopQuestionFails(t *testing.T) {
	type args struct {
		// The record file made a directory before the stop
		file string
	}
	tcs := []struct {
		name string
		args args
		want string
	}{
		{"a key that cannot be read", args{"hmac.key"}, "nodloop hook: "},
		{"verdicts that cannot be read", args{"feedback.jsonl"}, "nodloop hook: "},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := newQuestionBench(t, "")
			proposeQuestion(t, b)
			path := filepath.Join(b.records, tc.args.file)
			require.NoError(t, os.RemoveAll(path))
			require.NoError(t, os.Mkdir(path, 0o700))

			stdout, stderr := b.hook(t, "stop", "claude-code", questionStop, b.svc, noStart)

			assert.Empty(t, stdout)
			assert.Contains(t, stderr, tc.want)
			assert.Empty(t, b.traces(t, trace.NameExpose))
			assert.Len(t, b.traces(t, trace.NameRun), 4, "the run of the stop is recorded")
		})
	}
}

// A stop that curate.json and the exposes of its session leave nothing to show asks curate nothing and makes no key
// A broken key therefore affects a manual proposal and no ordinary candidate
func TestRunHookStopAsksCurateOnlyWhenDue(t *testing.T) {
	tcs := []struct {
		name     string
		proposal bool
		want     string
	}{
		{"unrequested candidates never reach the question reader", false, ""},
		{"a manual proposal reaches the question reader", true, "nodloop hook: "},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := newQuestionBench(t, "manual")
			if tc.proposal {
				proposeQuestion(t, b)
			}
			key := filepath.Join(b.records, "hmac.key")
			require.NoError(t, os.RemoveAll(key))
			require.NoError(t, os.Mkdir(key, 0o700))
			stdout, stderr := b.hook(t, "stop", "claude-code", questionStop, b.acme, noStart)
			assert.Empty(t, stdout)
			assert.Contains(t, stderr, tc.want)
			assert.Equal(t, tc.want == "", stderr == "")
		})
	}
}

// What the exposes of a session tell a hook before it asks curate
// 1. a reply word matches its question after case folding and trailing punctuation removal
// 2. a pushed question spends the one question of the session and a proposal the person asked for does not
// 3. a notice is the one notice of the session
func TestSessionExposes(t *testing.T) {
	expose := func(kind curate.QuestionKind, source trace.Source, run string) trace.Trace {
		in := `{"kind":"` + string(kind) + `","run":"` + run + `","choices":["keep","drop"]}`
		return trace.Trace{Name: trace.NameExpose, Subject: string(kind), Source: source, Input: json.RawMessage(in)}
	}
	type args struct {
		shown sessionExposes
		run   string
		word  string
	}
	type want struct {
		pushed, noticed bool
		named           curate.Choice
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a question pushed after the run", args{sessionExposes{expose(curate.QuestionDirection, trace.SourcePush, "r1")}, "r1", "Keep,"}, want{true, false, "keep"}},
		{"a question after another run", args{sessionExposes{expose(curate.QuestionAudit, trace.SourcePush, "r2")}, "r1", "keep"}, want{true, false, ""}},
		{"a word that names no choice", args{sessionExposes{expose(curate.QuestionDirection, trace.SourcePush, "r1")}, "r1", "maybe"}, want{true, false, ""}},
		{"a proposal the person asked for", args{sessionExposes{expose(curate.QuestionDirection, trace.SourceManual, "r1")}, "r1", "drop"}, want{false, false, "drop"}},
		{"a notice", args{sessionExposes{expose(curate.QuestionNotice, trace.SourcePush, "r1")}, "r1", "keep"}, want{false, true, ""}},
		{"an input that does not decode", args{sessionExposes{{Name: trace.NameExpose, Input: json.RawMessage(`"x"`)}}, "r1", "keep"}, want{false, false, ""}},
		{"no expose", args{nil, "r1", "keep"}, want{}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := want{tc.args.shown.pushed(), tc.args.shown.noticed(), tc.args.shown.named(tc.args.run, tc.args.word)}
			assert.Equal(t, tc.want, got)
		})
	}
}

// What curate.json leaves due for the stop of a session in a place
// 1. a proposal is due only in the session that made it
// 2. a question curate pushes is due where its place admits the place of the stop and everywhere without a place
func TestDueQuestions(t *testing.T) {
	tcs := []struct {
		name string
		args dueQuestions
		want bool
	}{
		{"a proposal of the session", dueQuestions{{Proposal: "i1", Session: "s1"}}, true},
		{"a proposal of another session", dueQuestions{{Proposal: "i1", Session: "s2"}}, false},
		{"an ordinary direction is not a manual request", dueQuestions{{Kind: curate.QuestionDirection}}, false},
		{"nothing due", nil, false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.proposed("s1"))
		})
	}
}
