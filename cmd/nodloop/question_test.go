package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
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
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
)

// A record directory where a curate pass left a direction question about k-wide of repo svc and a shadow decision about k-add of repo acme
type questionBench struct {
	vars    map[string]string
	records string
	// The checkouts of repo svc, repo acme and repo work where no lesson reaches
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

func newQuestionBench(t *testing.T, env map[string]string) questionBench {
	t.Helper()
	getenv, records := curateHome(t, [][3]string{
		{"k-add", "Name the failing test first", "repo=acme"},
		{"k-wide", "Use the staging cluster", "repo=svc"},
	})
	var stderr bytes.Buffer
	require.Equal(t, 0, runKnowledge([]string{"curate"}, getenv, curateClient(t), func() time.Time { return curateNow }, trace.ProofNone, &bytes.Buffer{}, &stderr),
		stderr.String())
	vars := map[string]string{"HOME": getenv("HOME"), envRecordDir: records}
	maps.Copy(vars, env)
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
	code := runHook([]string{action, "--host", host}, b.getenv, start, b.now, strings.NewReader(hookFixture(t, stdin, repo)), &out, &errOut)
	require.Equal(t, 0, code)
	return out.String(), errOut.String()
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

const questionStop = `{"session_id":"s1","cwd":"{repo}","last_assistant_message":"done"}`

// The start of the block of the question about k-wide
const wideQuestion = "nodloop question for you\nOne decision for you: add the lesson \"Use the staging cluster\" in repo svc\n"

// Claude Code asks the one question due for the place at the end of the first answer of a session
// It continues the turn on Stop additionalContext with no decision
func TestRunHookStopQuestion(t *testing.T) {
	b := newQuestionBench(t, nil)

	first, firstErr := b.hook(t, "stop", "claude-code", questionStop, b.svc, noStart)
	second, secondErr := b.hook(t, "stop", "claude-code", questionStop, b.svc, noStart)

	text, decision := stopAnswer(t, first)
	expose, x := b.exposed(t)
	assert.Empty(t, firstErr+secondErr)
	assert.Empty(t, second, "one question per session")
	assert.Empty(t, decision)
	assert.True(t, strings.HasPrefix(text, wideQuestion), text)
	assert.Contains(t, text, `AskUserQuestion call with header "nodloop `+x.Key+`", the two choices keep and drop`)
	assert.LessOrEqual(t, len("nodloop "+x.Key), 12, "the header AskUserQuestion allows")
	assert.Len(t, b.traces(t, trace.NameExpose), 1)
	assert.Equal(t, []any{"s1", curate.QuestionDirection, trace.SourcePush}, []any{expose.SessionID, x.Kind, expose.Source})
	assert.Len(t, b.traces(t, trace.NameRun), 5, "the three runs of the bench and those of the stops")
	assert.NoError(t, schema.Validate(string(trace.NameExpose), expose.Record()))
}

// A writer that reads the exposes of the records when the stop writes its answer
type exposeWitness struct {
	t     *testing.T
	b     questionBench
	out   bytes.Buffer
	found int
}

func (w *exposeWitness) Write(p []byte) (int, error) {
	w.found = len(w.b.traces(w.t, trace.NameExpose))
	return w.out.Write(p)
}

// The expose is on record before the stop prints the question with the run the question followed and the runes of its text
func TestRunHookStopQuestionExpose(t *testing.T) {
	b := newQuestionBench(t, nil)
	w := &exposeWitness{t: t, b: b}
	var stderr bytes.Buffer

	code := runHook([]string{"stop"}, b.getenv, noStart, b.now, strings.NewReader(hookFixture(t, questionStop, b.svc)), w, &stderr)

	require.Equal(t, 0, code, stderr.String())
	assert.Equal(t, 1, w.found)
	expose, x := b.exposed(t)
	runs := b.traces(t, trace.NameRun)
	text, _ := stopAnswer(t, w.out.String())
	assert.Equal(t, []any{runs[0].ID, 1}, []any{x.Run, x.Round})
	assert.Positive(t, x.Runes)
	assert.Less(t, x.Runes, len([]rune(text)), "the runes of the question alone")
	assert.Equal(t, 1.0, *expose.Draw.P)
}

// The stop talks to the person only when everything before it went through and only through the one path of the place and mode
// 1. a stop of a turn a stop hook continued, a place no question reaches and a mode that infers nothing ask nothing curate prepared
// 2. a failed start of the background work asks nothing
// 3. a proposal the session made through the manual path is asked in manual mode too
func TestRunHookStopQuestionRules(t *testing.T) {
	type args struct {
		stop, repo string
		env        map[string]string
		start      starter
		// Records written before the stop
		setup func(t *testing.T, b questionBench)
	}
	type want struct {
		start  string
		stderr string
	}
	propose := func(t *testing.T, b questionBench) {
		var out, stderr bytes.Buffer
		payload := `{"action":"judge","target":"k-add:1","choice":"keep","session":"s1"}`
		code := runIntervene([]string{"--payload", "-"}, b.getenv, b.now, trace.ProofNone, strings.NewReader(payload), &out, &stderr)
		require.Equal(t, 0, code, stderr.String())
	}
	// Session s0 was asked the question and its person answered it, which curate.json still lists until the next pass
	answered := func(t *testing.T, b questionBench) {
		b.hook(t, "stop", "claude-code", strings.Replace(questionStop, "s1", "s0", 1), b.svc, noStart)
		runs := b.traces(t, trace.NameRun)
		iv, err := (app{cfg: config{recordDir: b.records}, now: b.now}).intervener()
		require.NoError(t, err)
		replied, err := iv.Reply(context.Background(), curate.Reply{Session: "s0", Run: runs[0].ID, Word: "keep", Person: unnamedPerson})
		require.NoError(t, err)
		require.NotEmpty(t, replied.Answer)
	}
	corrupt := func(t *testing.T, b questionBench) {
		hookAppend(t, filepath.Join(b.records, "traces.jsonl"), "{\n")
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a turn a stop hook continued asks nothing", args{`{"session_id":"s1","cwd":"{repo}","last_assistant_message":"done","stop_hook_active":true}`, "svc", nil, noStart, nil},
			want{"", ""}},
		{"a place no question reaches asks nothing", args{questionStop, "work", nil, noStart, nil}, want{"", ""}},
		{"manual mode pushes nothing", args{questionStop, "svc", map[string]string{envSession: "manual"}, noStart, nil}, want{"", ""}},
		{"a failed start asks nothing", args{questionStop, "svc", nil, func([]string) error { return errors.New("no process") }, nil}, want{"", "no process"}},
		{"manual mode asks the proposal the session made", args{questionStop, "acme", map[string]string{envSession: "manual"}, noStart, propose},
			want{"nodloop question for you\nOne decision for you: keep the lesson \"Name the failing test first\"\n", ""}},
		{"a question another session answered asks nothing", args{questionStop, "svc", nil, noStart, answered}, want{"", ""}},
		{"exposes that cannot be read ask nothing and say why", args{questionStop, "svc", nil, noStart, corrupt}, want{"", "corrupt"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := newQuestionBench(t, tc.args.env)
			if tc.args.setup != nil {
				tc.args.setup(t, b)
			}
			repo := map[string]string{"svc": b.svc, "acme": b.acme, "work": b.work}[tc.args.repo]

			stdout, stderr := b.hook(t, "stop", "claude-code", tc.args.stop, repo, tc.args.start)

			text, _ := stopAnswer(t, stdout)
			assert.True(t, strings.HasPrefix(text, tc.want.start), text)
			assert.Equal(t, tc.want.start == "", text == "")
			assert.Contains(t, stderr, tc.want.stderr)
		})
	}
}

// The first word of the next prompt that names a choice of the question is never judged and records no answer
// 1. the prompt gets the reply note in place of the reaction note
// 2. any other first word, another session and a prompt after another answer get the notes as before
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
			b := newQuestionBench(t, nil)
			stdout, _ := b.hook(t, "stop", "claude-code", questionStop, b.svc, noStart)
			require.NotEmpty(t, stdout)
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
	b := newQuestionBench(t, map[string]string{})
	b.hook(t, "stop", "claude-code", questionStop, b.svc, noStart)
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
	b := newQuestionBench(t, nil)
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

	first, firstErr := b.hook(t, "stop", "claude-code", questionStop, b.acme, noStart)
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

// A stop whose question cannot be prepared reports why on stderr, asks nothing and still records the run
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
			b := newQuestionBench(t, nil)
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
// So a broken key fails no such stop while it fails the first stop of a session that asks
func TestRunHookStopAsksCurateOnlyWhenDue(t *testing.T) {
	type args struct {
		repo string
		env  map[string]string
		// The session asked its question at a stop before
		asked bool
	}
	tcs := []struct {
		name string
		args args
		want string
	}{
		{"the first stop of a session reaches curate", args{"svc", nil, false}, "nodloop hook: "},
		{"a session that was asked its question does not", args{"svc", nil, true}, ""},
		{"a place no question reaches does not", args{"work", nil, false}, ""},
		{"a mode that pushes nothing does not", args{"svc", map[string]string{envSession: "manual"}, false}, ""},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := newQuestionBench(t, tc.args.env)
			repo := map[string]string{"svc": b.svc, "work": b.work}[tc.args.repo]
			if tc.args.asked {
				stdout, _ := b.hook(t, "stop", "claude-code", questionStop, repo, noStart)
				require.NotEmpty(t, stdout)
			}
			key := filepath.Join(b.records, "hmac.key")
			require.NoError(t, os.RemoveAll(key))
			require.NoError(t, os.Mkdir(key, 0o700))

			stdout, stderr := b.hook(t, "stop", "claude-code", questionStop, repo, noStart)

			assert.Empty(t, stdout)
			assert.Contains(t, stderr, tc.want)
			assert.Equal(t, tc.want == "", stderr == "")
		})
	}
}

// What the exposes of a session tell a hook before it asks curate
// 1. a typed word answers a question the stop asked after the run it followed, case and trailing punctuation ignored
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
	acme := knowledge.RunScope{Producer: sessionProducer, Labels: trace.Labels{"repo": {"acme"}}}
	place := knowledge.RunScope{Producer: sessionProducer, Labels: trace.Labels{"repo": {"acme"}, "dir": {"."}}}
	type want struct {
		proposed, reaching bool
	}
	tcs := []struct {
		name string
		args dueQuestions
		want want
	}{
		{"a proposal of the session", dueQuestions{{Proposal: "i1", Session: "s1"}}, want{true, false}},
		{"a proposal of another session", dueQuestions{{Proposal: "i1", Session: "s2"}}, want{false, false}},
		{"a question of the place", dueQuestions{{Place: &acme}}, want{false, true}},
		{"a question of every place", dueQuestions{{}}, want{false, true}},
		{"a question of another place", dueQuestions{{Place: &knowledge.RunScope{Producer: sessionProducer, Labels: trace.Labels{"repo": {"svc"}}}}}, want{false, false}},
		{"nothing due", nil, want{}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, want{tc.args.proposed("s1"), tc.args.reaching(place)})
		})
	}
}
