package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
)

// The lesson the cued item of a cue bench teaches
const cueLesson = "Keep a commit title to one line"

// A record directory where ann approved git-c for repo nodloop and the cued item title for its git commit calls
type cueBench struct {
	getenv  func(string) string
	records string
	repo    string
	at      time.Time
}

func newCueBench(t testing.TB) *cueBench {
	t.Helper()
	home, records, repo := hookHome(t, "Use git -C")
	b := &cueBench{
		getenv:  func(k string) string { return map[string]string{"HOME": home, envRecordDir: records}[k] },
		records: records,
		repo:    repo,
		at:      curateNow,
	}
	now := func() time.Time { return time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC) }
	out := filepath.Join(home, "title.txt")
	require.NoError(t, os.WriteFile(out, []byte("Fix the retry loop on C1 and stop after three tries"), 0o600))
	var id, stderr bytes.Buffer
	require.Equal(
		t,
		0,
		runRun(
			[]string{"record", "--producer", sessionProducer, "--label", "repo=nodloop", "--output", out},
			b.getenv,
			now,
			&id,
			&stderr,
		),
	)
	require.Equal(t, 0, runKnowledge([]string{
		"propose", "--id", "title", "--kind", "judgment", "--content", cueLesson,
		"--cue-tool", "Bash", "--cue-command", "git commit",
		"--trace", strings.TrimSpace(id.String()), "--producer", sessionProducer, "--label", "repo=nodloop",
	}, b.getenv, nil, now, atTerminal, &bytes.Buffer{}, &stderr), stderr.String())
	require.Equal(
		t,
		0,
		runKnowledge(
			[]string{"approve", "title", "--version", "1", "--approver", "ann"},
			b.getenv,
			nil,
			now,
			atTerminal,
			&bytes.Buffer{},
			&stderr,
		),
		stderr.String(),
	)
	return b
}

// Runs the hook with the fields given beside the session and the place and returns its stdout
func (b *cueBench) hook(t *testing.T, args []string, fields map[string]any) string {
	t.Helper()
	stdout, stderr := b.hookOut(t, args, fields)
	require.Empty(t, stderr)
	return stdout
}

// Runs the hook as hook does and returns its stdout and stderr
func (b *cueBench) hookOut(t *testing.T, args []string, fields map[string]any) (stdout, stderr string) {
	t.Helper()
	in := map[string]any{"session_id": "s1", "cwd": b.repo, "last_assistant_message": "done"}
	for k, v := range fields {
		in[k] = v
	}
	input, err := json.Marshal(in)
	require.NoError(t, err)
	var out, errOut bytes.Buffer
	now := func() time.Time { b.at = b.at.Add(time.Second); return b.at }
	require.Equal(t, 0, runHook(args, b.getenv, noStart, now, bytes.NewReader(input), &out, &errOut))
	return out.String(), errOut.String()
}

// The call of the tool hook with the command given
func (b *cueBench) bash(t *testing.T, command string, fields map[string]any) string {
	t.Helper()
	call := map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": command}}
	for k, v := range fields {
		call[k] = v
	}
	return b.hook(t, []string{"tool", "--cues"}, call)
}

// The traces of the name newest first
func (b *cueBench) traces(t *testing.T, name trace.Name) trace.Traces {
	t.Helper()
	store, err := tracefile.New(b.records)
	require.NoError(t, err)
	list, err := store.List(context.Background(), record.TraceFilter{Name: string(name)})
	require.NoError(t, err)
	return trace.RestoreAll(list)
}

// The input of the newest run
func (b *cueBench) run(t *testing.T) knowledge.RunInput {
	t.Helper()
	runs := b.traces(t, trace.NameRun)
	require.NotEmpty(t, runs)
	var in knowledge.RunInput
	require.NoError(t, json.Unmarshal(runs[0].Input, &in))
	return in
}

// The refusal reason of a tool hook output and empty when it let the call through
func refusal(t *testing.T, out string) string {
	t.Helper()
	if out == "" {
		return ""
	}
	var answer struct {
		Output struct {
			Event    string `json:"hookEventName"`
			Decision string `json:"permissionDecision"`
			Reason   string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &answer))
	require.Equal(t, "PreToolUse", answer.Output.Event)
	require.Equal(t, "deny", answer.Output.Decision)
	return answer.Output.Reason
}

var title = knowledge.Ref{ID: "title", Version: 1}

// A commit is refused once with the cued lesson and then passes
// The prompt leaves the lesson for the call
// and the stop applies it since it is in the context of the answer
func TestRunHookToolRefusesOnce(t *testing.T) {
	b := newCueBench(t)
	prompt := b.hook(t, []string{"prompt", "--cues"}, nil)
	assert.Contains(t, prompt, "Use git -C")
	assert.NotContains(t, prompt, cueLesson, "a cued item waits for its call")
	assert.Empty(t, b.bash(t, "git status", nil), "a call no cue fits passes with nothing")
	assert.Empty(t, b.traces(t, trace.NameCue), "and records nothing")
	reason := refusal(t, b.bash(t, `git -C /work/o/svc commit -m "Fix it"`, nil))
	assert.True(t, strings.HasPrefix(reason, knowledge.CueLead))
	assert.Contains(t, reason, cueLesson)
	assert.Contains(t, reason, "Cue: before each Bash call running git commit")
	assert.True(t, strings.HasSuffix(reason, knowledge.CueClose))
	assert.NotContains(t, reason, "Use git -C", "an uncued item never comes at a call")
	assert.Empty(t, b.bash(t, `git commit -m "Fix it"`, nil), "the same call passes once the lesson is in the context")
	b.hook(t, []string{"stop", "--cues"}, nil)
	assert.Contains(t, b.run(t).Applied, title)
	again := b.hook(t, []string{"prompt", "--cues"}, nil)
	assert.NotContains(t, again, cueLesson, "a later prompt never adds what a call received")
	assert.Empty(t, b.bash(t, `git commit --amend`, nil), "nor does a later call")
}

// A plugin without the tool hook adds the cued item with the prompt and the tool hook does nothing without cues
func TestRunHookToolWithoutCues(t *testing.T) {
	b := newCueBench(t)
	assert.Contains(t, b.hook(t, []string{"prompt"}, nil), cueLesson)
	assert.Empty(
		t,
		b.hook(
			t,
			[]string{"tool"},
			map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "git commit"}},
		),
	)
	assert.Empty(t, b.traces(t, trace.NameCue))
}

// A subagent holds only what its own calls received
func TestRunHookToolSubagent(t *testing.T) {
	b := newCueBench(t)
	b.hook(t, []string{"prompt", "--cues"}, nil)
	sub := map[string]any{"agent_id": "a1"}
	assert.Contains(t, refusal(t, b.bash(t, "git commit", sub)), cueLesson)
	assert.Empty(t, b.bash(t, "git commit", sub))
	assert.Contains(t, refusal(t, b.bash(t, "git commit", nil)), cueLesson, "the conversation still lacks it")
	assert.Empty(t, b.bash(t, "git commit", map[string]any{"agent_id": "a1"}))
	cues := b.traces(t, trace.NameCue)
	require.Len(t, cues, 2)
	assert.Equal(t, []string{"", "a1"}, []string{cues[0].Subject, cues[1].Subject})
	for range 4 {
		b.hook(t, []string{"stop", "--cues"}, nil)
		b.hook(t, []string{"prompt", "--cues"}, nil)
	}
	assert.Empty(
		t,
		b.bash(t, "git commit", sub),
		"a subagent keeps what it received however many turns its conversation answered since",
	)
}

// A compaction empties the context so the next fitting call delivers the lesson again
func TestRunHookToolAfterRefill(t *testing.T) {
	b := newCueBench(t)
	b.hook(t, []string{"prompt", "--cues"}, nil)
	require.NotEmpty(t, refusal(t, b.bash(t, "git commit", nil)))
	b.hook(t, []string{"stop", "--cues"}, nil)
	start := b.hook(t, []string{"start", "--cues"}, map[string]any{"source": "compact"})
	assert.NotContains(t, start, cueLesson, "a refill adds only the uncued items")
	b.hook(t, []string{"prompt", "--cues"}, nil)
	assert.Contains(t, refusal(t, b.bash(t, "git commit", nil)), cueLesson)
}

// A retired cued item the context received is invalidated by the next prompt
func TestRunHookToolRetired(t *testing.T) {
	b := newCueBench(t)
	b.hook(t, []string{"prompt", "--cues"}, nil)
	require.NotEmpty(t, refusal(t, b.bash(t, "git commit", nil)))
	b.hook(t, []string{"stop", "--cues"}, nil)
	var stderr bytes.Buffer
	require.Equal(t, 0, runKnowledge(
		[]string{"retire", "title", "--version", "1", "--approver", "ann"},
		b.getenv,
		nil,
		func() time.Time { b.at = b.at.Add(time.Second); return b.at },
		atTerminal,
		&bytes.Buffer{},
		&stderr,
	), stderr.String())
	assert.Contains(t, b.hook(t, []string{"prompt", "--cues"}, nil), "stop following inactive items title v1")
	assert.Empty(t, b.bash(t, "git commit", nil))
}

// A withheld session refuses nothing and records what its calls fitted so the stop counts it withheld
func TestRunHookToolWithheld(t *testing.T) {
	b := newCueBench(t)
	config := filepath.Join(b.getenv("HOME"), ".nodloop", "config.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(config), 0o755))
	require.NoError(t, os.WriteFile(config, []byte(`{"holdout":0.5}`), 0o600))
	session := ""
	for i := range 100 {
		if id := fmt.Sprintf("w%d", i); knowledge.Holdout(0.5).WithholdsRun(sessionProducer, id) {
			session = id
			break
		}
	}
	require.NotEmpty(t, session)
	withheld := map[string]any{"session_id": session}
	b.hook(t, []string{"prompt", "--cues"}, withheld)
	assert.Empty(t, b.bash(t, "git commit", withheld))
	assert.Empty(t, b.bash(t, "git commit", withheld), "a second call records nothing more")
	require.Len(t, b.traces(t, trace.NameCue), 1)
	b.hook(t, []string{"stop", "--cues"}, withheld)
	in := b.run(t)
	assert.Empty(t, in.Applied)
	assert.Contains(t, in.Withheld, title)
}

// A failed record lets the call through and refuses nothing
func TestRunHookToolAppendFails(t *testing.T) {
	b := newCueBench(t)
	b.hook(t, []string{"prompt", "--cues"}, nil)
	traces := filepath.Join(b.records, "traces.jsonl")
	require.NoError(t, os.Chmod(traces, 0o400))
	t.Cleanup(func() { _ = os.Chmod(traces, 0o600) })
	input, err := json.Marshal(
		map[string]any{
			"session_id": "s1",
			"cwd":        b.repo,
			"tool_name":  "Bash",
			"tool_input": map[string]any{"command": "git commit"},
		},
	)
	require.NoError(t, err)
	var stdout, stderr bytes.Buffer
	require.Equal(
		t,
		0,
		runHook([]string{"tool", "--cues"}, b.getenv, noStart, func() time.Time { return b.at.Add(time.Minute) },
			bytes.NewReader(input), &stdout, &stderr),
	)
	assert.Empty(t, stdout.String())
	assert.NotEmpty(t, stderr.String())
}

// The refusal holds what fits contextRunes and leaves the rest for the next one
func TestCueText(t *testing.T) {
	item := func(id string, runes int) knowledge.Knowledge {
		return knowledge.Knowledge{
			ID: id, Version: 1, Kind: knowledge.KindJudgment, Content: strings.Repeat("a", runes),
			Run: &knowledge.RunScope{Producer: sessionProducer}, Cue: &knowledge.Cue{Tool: "Bash"},
		}
	}
	type want struct {
		shown []string
		// Substrings of the text
		holds []string
	}
	tcs := []struct {
		name string
		args knowledge.Set
		want want
	}{
		{
			"items past the room wait for the next refusal",
			knowledge.Set{item("a", 4000), item("b", 4000), item("c", 4000)},
			want{[]string{"a", "b"}, []string{"- 1 more items come at the next call\n", knowledge.CueClose}},
		},
		{"one item past the room is cut and marked", knowledge.Set{item("a", 12000), item("b", 10)},
			want{[]string{"a"}, []string{cutMark, "- 1 more items come at the next call\n"}}},
		{
			"items that fit come whole with no count",
			knowledge.Set{item("a", 10), item("b", 10)},
			want{[]string{"a", "b"}, []string{knowledge.CueLead}},
		},
		{"nothing due shows nothing", nil, want{shown: []string{}}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			shown, text := cueText(tc.args)
			ids := []string{}
			for _, k := range shown {
				ids = append(ids, k.ID)
			}
			assert.Equal(t, tc.want.shown, ids)
			for _, part := range tc.want.holds {
				assert.Contains(t, text, part)
			}
			assert.LessOrEqual(t, len([]rune(text)), contextRunes)
			if len(tc.args) > 0 && len(shown) == len(tc.args) {
				assert.NotContains(t, text, "more items")
			}
		})
	}
}

// A cued version the item hold draws for a session is eligible at its call and never delivered
// The stop records it held
func TestRunHookToolItemHold(t *testing.T) {
	prC := knowledge.Ref{ID: "pr-c", Version: 1}
	require.True(t, knowledge.ItemHold(0.1).Holds(sessionProducer, "s34", prC), "pr-c draws 0.054 in s34")
	require.False(t, knowledge.ItemHold(0.1).Holds(sessionProducer, "s1", prC), "pr-c draws 0.932 in s1")
	const cuedItem = `{"id":"pr-c","version":1,"kind":"judgment","content":"write the motivation first",` +
		`"run":{"producer":"session","labels":{"repo":["nodloop"]}},` +
		`"evidence":{"feedback_trace_ids":["t1"]},"basis":"stated",` +
		`"status":"approved","approver":"nodloop","approved_at":"2026-10-02T00:30:00Z",` +
		`"time":"2026-10-02T00:30:00Z","author":"nodloop",` +
		`"cue":{"tool":"Bash","command":"gh pr create"}}` + "\n"
	tcs := []struct {
		name    string
		session string
		want    knowledge.RunInput
	}{
		{"a drawn session gets no refusal and its run holds the version", "s34", knowledge.RunInput{
			Applied: []knowledge.Ref{{ID: "git-c", Version: 1}}, Held: []knowledge.Ref{prC},
		}},
		{"a session the draw passes is refused once and its run applies the version", "s1", knowledge.RunInput{
			Applied: []knowledge.Ref{{ID: "git-c", Version: 1}, prC},
		}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, records, repo := hookHome(t, "use git -C")
			hookAppend(t, filepath.Join(records, "knowledge.jsonl"), cuedItem)
			hookMark(t, records, `{"knobs":{"activated":true,"item_hold":0.1}}`)
			b := &cueBench{
				getenv:  envOf(map[string]string{"HOME": home, envRecordDir: records}),
				records: records,
				repo:    repo,
				at:      curateNow,
			}
			session := map[string]any{"session_id": tc.session}
			b.hook(t, []string{"prompt", "--cues"}, session)
			reason := refusal(t, b.bash(t, "gh pr create --fill", session))
			assert.Equal(t, tc.session == "s1", strings.Contains(reason, "write the motivation first"))
			b.hook(t, []string{"stop", "--cues"}, session)
			got := b.run(t)
			assert.Equal(t, tc.want.Applied, got.Applied)
			assert.Equal(t, tc.want.Held, got.Held)
		})
	}
}

// A version approved during the turn that a call delivered counts as applied though the turn began before it
func TestRunHookToolApprovedDuringTurn(t *testing.T) {
	b := newCueBench(t)
	b.hook(t, []string{"prompt", "--cues"}, nil)
	clock := func() time.Time { b.at = b.at.Add(time.Second); return b.at }
	runs := b.traces(t, trace.NameRun)
	require.NotEmpty(t, runs)
	var stderr bytes.Buffer
	require.Equal(t, 0, runKnowledge([]string{
		"propose", "--id", "title", "--kind", "judgment", "--content", "Keep a commit title under fifty characters",
		"--cue-tool", "Bash", "--cue-command", "git commit", "--trace", runs[len(runs)-1].ID,
		"--producer", sessionProducer, "--label", "repo=nodloop",
	}, b.getenv, nil, clock, atTerminal, &bytes.Buffer{}, &stderr), stderr.String())
	require.Equal(
		t,
		0,
		runKnowledge(
			[]string{"approve", "title", "--version", "2", "--approver", "ann"},
			b.getenv,
			nil,
			clock,
			atTerminal,
			&bytes.Buffer{},
			&stderr,
		),
		stderr.String(),
	)
	assert.Contains(t, refusal(t, b.bash(t, "git commit", nil)), "under fifty characters")
	b.hook(t, []string{"stop", "--cues"}, nil)
	assert.Contains(t, b.run(t).Applied, knowledge.Ref{ID: "title", Version: 2})
}

// A cued version the cut left due was never delivered so the stop leaves it out of applied
func TestRunHookToolCutNotApplied(t *testing.T) {
	home, records, repo := hookHome(t, "use git -C")
	long := strings.Repeat("a", 6000)
	for _, id := range []string{"cue-a", "cue-b", "cue-c"} {
		hookAppend(
			t,
			filepath.Join(records, "knowledge.jsonl"),
			`{"id":"`+id+`","version":1,"kind":"judgment","content":"`+long+`",`+
				`"run":{"producer":"session","labels":{"repo":["nodloop"]}},`+
				`"evidence":{"feedback_trace_ids":["t1"]},"basis":"stated",`+
				`"status":"approved","approver":"ann","approved_at":"2026-10-02T00:30:00Z",`+
				`"time":"2026-10-02T00:30:00Z","author":"author",`+
				`"cue":{"tool":"Bash","command":"git commit"}}`+"\n",
		)
	}
	b := &cueBench{
		getenv:  envOf(map[string]string{"HOME": home, envRecordDir: records}),
		records: records,
		repo:    repo,
		at:      curateNow,
	}
	b.hook(t, []string{"prompt", "--cues"}, nil)
	require.NotEmpty(t, refusal(t, b.bash(t, "git commit", nil)))
	b.hook(t, []string{"stop", "--cues"}, nil)
	applied := b.run(t).Applied
	delivered := 0
	for _, id := range []string{"cue-a", "cue-b", "cue-c"} {
		if slices.Contains(applied, knowledge.Ref{ID: id, Version: 1}) {
			delivered++
		}
	}
	assert.Equal(t, 1, delivered, "only the item the refusal showed counts")
}

// A reaffirm during the turn never changes the hold a call observed
func TestRunHookToolReaffirmKeepsHold(t *testing.T) {
	prC := knowledge.Ref{ID: "pr-c", Version: 1}
	const cuedItem = `{"id":"pr-c","version":1,"kind":"judgment","content":"write the motivation first",` +
		`"run":{"producer":"session","labels":{"repo":["nodloop"]}},` +
		`"evidence":{"feedback_trace_ids":["t1"]},"basis":"stated",` +
		`"status":"approved","approver":"nodloop","approved_at":"2026-10-02T00:30:00Z",` +
		`"time":"2026-10-02T00:30:00Z","author":"nodloop",` +
		`"cue":{"tool":"Bash","command":"gh pr create"}}` + "\n"
	home, records, repo := hookHome(t, "use git -C")
	hookAppend(t, filepath.Join(records, "knowledge.jsonl"), cuedItem)
	hookMark(t, records, `{"knobs":{"activated":true,"item_hold":0.1}}`)
	b := &cueBench{
		getenv:  envOf(map[string]string{"HOME": home, envRecordDir: records}),
		records: records,
		repo:    repo,
		at:      curateNow,
	}
	session := map[string]any{"session_id": "s34"}
	b.hook(t, []string{"prompt", "--cues"}, session)
	assert.Empty(t, b.bash(t, "gh pr create --fill", session), "a held version is never delivered")
	var stderr bytes.Buffer
	require.Equal(t, 0, runKnowledge(
		[]string{"reaffirm", "pr-c", "--version", "1", "--approver", "ann"},
		b.getenv,
		nil,
		func() time.Time { b.at = b.at.Add(time.Second); return b.at },
		atTerminal,
		&bytes.Buffer{},
		&stderr,
	), stderr.String())
	b.hook(t, []string{"stop", "--cues"}, session)
	got := b.run(t)
	assert.Equal(t, []knowledge.Ref{prC}, got.Held)
	assert.Contains(t, got.Eligible, knowledge.Eligible{Ref: prC, PInclude: 0.9})
}

// A version nodloop approved during the turn before its call is held as the call drew it
// The turn began without it
func TestRunHookToolApprovedByNodloopDuringTurn(t *testing.T) {
	prC := knowledge.Ref{ID: "pr-c", Version: 1}
	home, records, repo := hookHome(t, "use git -C")
	hookMark(t, records, `{"knobs":{"activated":true,"item_hold":0.1}}`)
	b := &cueBench{
		getenv:  envOf(map[string]string{"HOME": home, envRecordDir: records}),
		records: records,
		repo:    repo,
		at:      curateNow,
	}
	session := map[string]any{"session_id": "s34"}
	b.hook(t, []string{"prompt", "--cues"}, session)
	approved := b.at.Add(time.Second).Format(time.RFC3339)
	hookAppend(
		t,
		filepath.Join(records, "knowledge.jsonl"),
		`{"id":"pr-c","version":1,"kind":"judgment","content":"write the motivation first",`+
			`"run":{"producer":"session","labels":{"repo":["nodloop"]}},`+
			`"evidence":{"feedback_trace_ids":["t1"]},"basis":"stated",`+
			`"status":"approved","approver":"nodloop","approved_at":"`+approved+`","time":"`+approved+`","author":"nodloop",`+
			`"cue":{"tool":"Bash","command":"gh pr create"}}`+"\n",
	)
	b.at = b.at.Add(2 * time.Second)
	assert.Empty(t, b.bash(t, "gh pr create --fill", session), "the hold keeps it from s34")
	b.hook(t, []string{"stop", "--cues"}, session)
	got := b.run(t)
	assert.Equal(t, []knowledge.Ref{prC}, got.Held)
	assert.Contains(t, got.Eligible, knowledge.Eligible{Ref: prC, PInclude: 0.9})
	assert.NotContains(t, got.Applied, prC)
}

// A version held at one call and delivered at a later one after a reaffirm counts as applied and never stays held
func TestRunHookToolHeldThenDelivered(t *testing.T) {
	prC := knowledge.Ref{ID: "pr-c", Version: 1}
	const cuedItem = `{"id":"pr-c","version":1,"kind":"judgment","content":"write the motivation first",` +
		`"run":{"producer":"session","labels":{"repo":["nodloop"]}},` +
		`"evidence":{"feedback_trace_ids":["t1"]},"basis":"stated",` +
		`"status":"approved","approver":"nodloop","approved_at":"2026-10-02T00:30:00Z",` +
		`"time":"2026-10-02T00:30:00Z","author":"nodloop",` +
		`"cue":{"tool":"Bash","command":"gh pr create"}}` + "\n"
	home, records, repo := hookHome(t, "use git -C")
	hookAppend(t, filepath.Join(records, "knowledge.jsonl"), cuedItem)
	hookMark(t, records, `{"knobs":{"activated":true,"item_hold":0.1}}`)
	b := &cueBench{
		getenv:  envOf(map[string]string{"HOME": home, envRecordDir: records}),
		records: records,
		repo:    repo,
		at:      curateNow,
	}
	session := map[string]any{"session_id": "s34"}
	b.hook(t, []string{"prompt", "--cues"}, session)
	require.Empty(t, b.bash(t, "gh pr create --fill", session))
	var stderr bytes.Buffer
	require.Equal(t, 0, runKnowledge(
		[]string{"reaffirm", "pr-c", "--version", "1", "--approver", "ann"},
		b.getenv,
		nil,
		func() time.Time { b.at = b.at.Add(time.Second); return b.at },
		atTerminal,
		&bytes.Buffer{},
		&stderr,
	), stderr.String())
	require.Contains(t, refusal(t, b.bash(t, "gh pr create --fill", session)), "write the motivation first")
	b.hook(t, []string{"stop", "--cues"}, session)
	got := b.run(t)
	assert.Contains(t, got.Applied, prC)
	assert.NotContains(t, got.Held, prC)
}

// A compaction during the turn empties the context but the stop still counts what the calls of the turn met
func TestRunHookToolCompactDuringTurn(t *testing.T) {
	b := newCueBench(t)
	b.hook(t, []string{"prompt", "--cues"}, nil)
	require.NotEmpty(t, refusal(t, b.bash(t, "git commit", nil)))
	b.hook(t, []string{"start", "--cues"}, map[string]any{"source": "compact"})
	b.hook(t, []string{"stop", "--cues"}, nil)
	assert.Contains(t, b.run(t).Applied, title)
}

// A compaction during the turn keeps a version the hold kept back at a call held
func TestRunHookToolCompactKeepsHold(t *testing.T) {
	prC := knowledge.Ref{ID: "pr-c", Version: 1}
	home, records, repo := hookHome(t, "use git -C")
	hookAppend(
		t,
		filepath.Join(records, "knowledge.jsonl"),
		`{"id":"pr-c","version":1,"kind":"judgment","content":"write the motivation first",`+
			`"run":{"producer":"session","labels":{"repo":["nodloop"]}},`+
			`"evidence":{"feedback_trace_ids":["t1"]},"basis":"stated",`+
			`"status":"approved","approver":"nodloop","approved_at":"2026-10-02T00:30:00Z",`+
			`"time":"2026-10-02T00:30:00Z","author":"nodloop",`+
			`"cue":{"tool":"Bash","command":"gh pr create"}}`+"\n",
	)
	hookMark(t, records, `{"knobs":{"activated":true,"item_hold":0.1}}`)
	b := &cueBench{
		getenv:  envOf(map[string]string{"HOME": home, envRecordDir: records}),
		records: records,
		repo:    repo,
		at:      curateNow,
	}
	session := map[string]any{"session_id": "s34"}
	b.hook(t, []string{"prompt", "--cues"}, session)
	require.Empty(t, b.bash(t, "gh pr create --fill", session))
	b.hook(t, []string{"start", "--cues"}, map[string]any{"session_id": "s34", "source": "compact"})
	b.hook(t, []string{"stop", "--cues"}, session)
	assert.Equal(t, []knowledge.Ref{prC}, b.run(t).Held)
}

// A corrupt line in the trace store still delivers the cued item as the prompt still adds the others
func TestRunHookToolCorruptLine(t *testing.T) {
	b := newCueBench(t)
	path := filepath.Join(b.records, "traces.jsonl")
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, append([]byte("{broken\n"), content...), 0o600))
	prompt, _ := b.hookOut(t, []string{"prompt", "--cues"}, nil)
	assert.Contains(t, prompt, "Use git -C")
	call := map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": `git commit -m "Fix it"`}}
	refused, _ := b.hookOut(t, []string{"tool", "--cues"}, call)
	assert.Contains(t, refusal(t, refused), cueLesson)
}
