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

// The text a stop answer holds under the field of the host and the decision it names
func stopAnswer(t *testing.T, stdout, field string) (text, decision string) {
	t.Helper()
	if stdout == "" {
		return "", ""
	}
	var answer map[string]any
	require.NoError(t, json.Unmarshal([]byte(stdout), &answer), stdout)
	decision, _ = answer["decision"].(string)
	if field == "additionalContext" {
		out, _ := answer["hookSpecificOutput"].(map[string]any)
		text, _ = out[field].(string)
		return text, decision
	}
	text, _ = answer[field].(string)
	return text, decision
}

const questionStop = `{"session_id":"s1","cwd":"{repo}","last_assistant_message":"done"}`

// The start of the block of the question about k-wide
const wideQuestion = "nodloop question for you\nOne decision for you: add the lesson \"Use the staging cluster\" in repo svc\n"

// Every host asks the one question due for the place at the end of the first answer of a session in its own answer shape
// 1. Claude Code continues the turn on Stop additionalContext with no decision
// 2. Codex and Copilot block the stop with the question as the reason and Cursor follows up with it
// 3. Cursor and Copilot record no run so the expose of the session alone keeps a later stop from asking again
func TestRunHookStopQuestion(t *testing.T) {
	type args struct {
		host string
		// The input of both stops, a fixture under testdata/hooks or a JSON text
		stop string
	}
	type want struct {
		field, decision string
		runs            int
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"Claude Code asks through additionalContext with no decision", args{"claude-code", questionStop}, want{"additionalContext", "", 2}},
		{"Codex blocks its stop with the question", args{"codex", "codex_stop.json"}, want{"reason", "block", 2}},
		{"Cursor follows up once", args{"cursor", "cursor_stop.json"}, want{"followup_message", "", 0}},
		{"Copilot blocks its stop once", args{"copilot", "copilot_stop.json"}, want{"reason", "block", 0}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := newQuestionBench(t, nil)

			first, firstErr := b.hook(t, "stop", tc.args.host, tc.args.stop, b.svc, noStart)
			second, secondErr := b.hook(t, "stop", tc.args.host, tc.args.stop, b.svc, noStart)

			text, decision := stopAnswer(t, first, tc.want.field)
			expose, x := b.exposed(t)
			assert.Empty(t, firstErr+secondErr)
			assert.Empty(t, second, "one question per session")
			assert.Equal(t, tc.want.decision, decision)
			assert.True(t, strings.HasPrefix(text, wideQuestion), text)
			assert.Contains(t, text, `AskUserQuestion call with header "nodloop `+x.Key+`", the two choices keep and drop`)
			assert.LessOrEqual(t, len("nodloop "+x.Key), 12, "the header AskUserQuestion allows")
			assert.Len(t, b.traces(t, trace.NameExpose), 1)
			assert.Equal(t, []any{"s1", curate.QuestionDirection, trace.SourcePush}, []any{expose.SessionID, x.Kind, expose.Source})
			assert.Len(t, b.traces(t, trace.NameRun), tc.want.runs+3, "the three runs of the bench and those of the stops")
			assert.NoError(t, schema.Validate(string(trace.NameExpose), expose.Record()))
		})
	}
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
	text, _ := stopAnswer(t, w.out.String(), "additionalContext")
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

			text, _ := stopAnswer(t, stdout, "additionalContext")
			assert.True(t, strings.HasPrefix(text, tc.want.start), text)
			assert.Equal(t, tc.want.start == "", text == "")
			assert.Contains(t, stderr, tc.want.stderr)
		})
	}
}

// The first word of the next prompt that names a choice of the question is recorded as the person's answer and never judged
// 1. the answer carries proof reply and the ledger record names it under approver of config.json or else person
// 2. the prompt gets the reply note in place of the reaction note
// 3. any other first word, another session and a prompt after another answer record nothing
func TestRunHookPromptReply(t *testing.T) {
	type args struct {
		prompt string
		// A turn the session took between the question and the prompt
		between bool
	}
	type want struct {
		answers  []string
		statuses []string
		note     string
	}
	waiting := []string{"k-add v1 candidate", "k-person v1 approved ann", "k-wide v1 candidate"}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"a reply word keeps the change under the person",
			args{`{"session_id":"s1","cwd":"{repo}","prompt":"Keep. And show the cluster list"}`, false},
			want{[]string{"keep reply person"}, []string{"k-add v1 candidate", "k-person v1 approved ann", "k-wide v1 approved person"},
				"nodloop: the user's first word keep answered the nodloop question and nodloop recorded it."},
		},
		{"another first word answers nothing", args{`{"session_id":"s1","cwd":"{repo}","prompt":"show the cluster list"}`, false}, want{[]string{}, waiting, "nodloop: your previous answer"}},
		{"another session answers nothing", args{`{"session_id":"s2","cwd":"{repo}","prompt":"keep"}`, false}, want{[]string{}, waiting, ""}},
		{"a reply after another answer answers nothing", args{`{"session_id":"s1","cwd":"{repo}","prompt":"keep"}`, true}, want{[]string{}, waiting, "nodloop: your previous answer"}},
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
			assert.Equal(t, tc.want.answers, questionAnswers(t, b))
			assert.Equal(t, tc.want.statuses, curateStatuses(t, b.records))
			assert.Contains(t, added, tc.want.note)
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

// The answer of a reply word names the expose it answers and the ledger record names the answer
func TestRunHookPromptReplyRecords(t *testing.T) {
	b := newQuestionBench(t, map[string]string{})
	b.hook(t, "stop", "claude-code", questionStop, b.svc, noStart)
	expose, _ := b.exposed(t)

	b.hook(t, "prompt", "claude-code", `{"session_id":"s1","cwd":"{repo}","prompt":"keep"}`, b.svc, noStart)

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
	assert.Empty(t, codex, "a host that shows no stop line gets no notice")
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

// A prompt whose reply cannot be read reports why on stderr and records no answer
// A corrupt line of the knowledge records leaves the items to the prompt and fails the read of the reply
func TestRunHookPromptReplyFails(t *testing.T) {
	b := newQuestionBench(t, nil)
	stdout, _ := b.hook(t, "stop", "claude-code", questionStop, b.svc, noStart)
	require.NotEmpty(t, stdout)
	f, err := os.OpenFile(filepath.Join(b.records, "knowledge.jsonl"), os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = f.WriteString("not json\n")
	require.NoError(t, errors.Join(err, f.Close()))

	_, stderr := b.hook(t, "prompt", "claude-code", `{"session_id":"s1","cwd":"{repo}","prompt":"keep"}`, b.svc, noStart)

	assert.Contains(t, stderr, "nodloop hook: ")
	assert.Empty(t, questionAnswers(t, b))
}
