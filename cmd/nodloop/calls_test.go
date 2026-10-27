package main

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/call"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/schema"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// The calls traces of the bench oldest first as their inputs
func (b captureBench) calls(t *testing.T) call.Inputs {
	t.Helper()
	var out call.Inputs
	for _, tr := range slices.Backward(b.traces(t, trace.NameCalls)) {
		require.NoError(t, schema.Validate(string(trace.NameCalls), tr.Record()))
		assert.Equal(t, "s1", tr.SessionID)
		var in call.Input
		require.NoError(t, json.Unmarshal(tr.Input, &in))
		out = append(out, in)
	}
	return out
}

// The capture keeps the shapes of the primary tool calls one group per span between the stops of runs
// and a later capture records none of them again
func TestRunSessionCaptureCalls(t *testing.T) {
	b := newCaptureBench(t, twoStops, "deferred")
	path := b.transcript(t, "calls.jsonl")
	client, _ := captureClient(t, sureNeither, nil)
	at := captureDay.Add(2 * time.Minute)
	code, _, stderr := b.capture(t, client, path, at)
	require.Equal(t, 0, code, stderr)
	want := call.Inputs{
		{At: captureDay.Add(3 * time.Second), Calls: call.Shapes{{Tool: "Bash", Command: "git commit"}}},
		{
			At:    captureDay.Add(62 * time.Second),
			Turn:  captureDay.Add(60 * time.Second),
			Calls: call.Shapes{{Tool: "Edit", File: "*_test.go"}, {Tool: "Bash", Command: "git commit"}},
		},
		{
			At:    captureDay.Add(80 * time.Second),
			Turn:  captureDay.Add(60 * time.Second),
			Calls: call.Shapes{{Tool: "Bash", Command: "gh pr create"}},
		},
	}
	got := b.calls(t)
	assert.Equal(t, want, got)
	raw, err := json.Marshal(got)
	require.NoError(t, err)
	for _, text := range []string{"C1", "retry", "scratch", "/work", "ann"} {
		assert.NotContains(t, string(raw), text)
	}
	code, _, stderr = b.capture(t, client, path, at.Add(time.Minute))
	require.Equal(t, 0, code, stderr)
	assert.Equal(t, want, b.calls(t), "a capture past the offset records no call again")
}

func TestTranscriptPartGrouped(t *testing.T) {
	// An answer measured from the turn at 5 s as the stop measures it
	stop := func(after time.Duration) trace.Trace {
		return trace.Trace{
			Name:       trace.NameRun,
			Time:       captureDay.Add(after),
			DurationMS: (after - 5*time.Second).Milliseconds(),
		}
	}
	shaped := func(after time.Duration, command string) shapedCall {
		return shapedCall{at: captureDay.Add(after), shapes: call.Shapes{{Tool: "Bash", Command: command}}}
	}
	tcs := []struct {
		name string
		args []shapedCall
		want []call.Input
	}{
		{"no call", nil, nil},
		{
			"calls of one span share a group with the time of the newest",
			[]shapedCall{shaped(1*time.Second, "ls"), shaped(2*time.Second, "pwd"), shaped(3*time.Second, "ls")},
			[]call.Input{
				{
					At:    captureDay.Add(3 * time.Second),
					Calls: call.Shapes{{Tool: "Bash", Command: "ls"}, {Tool: "Bash", Command: "pwd"}},
				},
			},
		},
		{
			"a group names the turn its calls follow",
			[]shapedCall{shaped(6*time.Second, "ls")},
			[]call.Input{
				{
					At:    captureDay.Add(6 * time.Second),
					Turn:  captureDay.Add(5 * time.Second),
					Calls: call.Shapes{{Tool: "Bash", Command: "ls"}},
				},
			},
		},
		{
			"a call at the time of a stop belongs before it",
			[]shapedCall{shaped(10*time.Second, "ls"), shaped(11*time.Second, "pwd")},
			[]call.Input{
				{
					At:    captureDay.Add(10 * time.Second),
					Turn:  captureDay.Add(5 * time.Second),
					Calls: call.Shapes{{Tool: "Bash", Command: "ls"}},
				},
				{
					At:    captureDay.Add(11 * time.Second),
					Turn:  captureDay.Add(5 * time.Second),
					Calls: call.Shapes{{Tool: "Bash", Command: "pwd"}},
				},
			},
		},
		{
			"a turn start splits the calls of an interrupted turn from the next",
			[]shapedCall{shaped(72*time.Second, "ls"), shaped(78*time.Second, "pwd")},
			[]call.Input{
				{
					At:    captureDay.Add(72 * time.Second),
					Turn:  captureDay.Add(5 * time.Second),
					Calls: call.Shapes{{Tool: "Bash", Command: "ls"}},
				},
				{
					At:    captureDay.Add(78 * time.Second),
					Turn:  captureDay.Add(75 * time.Second),
					Calls: call.Shapes{{Tool: "Bash", Command: "pwd"}},
				},
			},
		},
		{
			"calls after the newest stop form the last group",
			[]shapedCall{shaped(80*time.Second, "ls"), shaped(90*time.Second, "pwd")},
			[]call.Input{
				{
					At:    captureDay.Add(90 * time.Second),
					Turn:  captureDay.Add(75 * time.Second),
					Calls: call.Shapes{{Tool: "Bash", Command: "ls"}, {Tool: "Bash", Command: "pwd"}},
				},
			},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := transcriptPart{calls: tc.args}
			turns := trace.Traces{
				{Name: trace.NameTurn, Time: captureDay.Add(75 * time.Second)},
				{Name: trace.NameTurn, Time: captureDay.Add(5 * time.Second)},
			}
			assert.Equal(t, tc.want, p.grouped(trace.Traces{stop(70 * time.Second), stop(10 * time.Second)}, turns))
		})
	}
}

// The calls after an answer of the newest turn keep its name
// and those after an answer whose prompt left no trace name none
func TestTranscriptPartGroupedUntracedTurn(t *testing.T) {
	at := func(s int) time.Time { return captureDay.Add(time.Duration(s) * time.Second) }
	answer := func(s, turn int) trace.Trace {
		r := trace.Trace{Name: trace.NameRun, Time: at(s), Input: json.RawMessage(`{"applied":[]}`)}
		if turn >= 0 {
			r.DurationMS = at(s).Sub(at(turn)).Milliseconds()
		}
		return r
	}
	shaped := func(s int, command string) shapedCall {
		return shapedCall{at: at(s), shapes: call.Shapes{{Tool: "Bash", Command: command}}}
	}
	p := transcriptPart{calls: []shapedCall{shaped(12, "git push"), shaped(32, "git tag")}}
	got := p.grouped(trace.Traces{answer(10, 0), answer(30, -1)}, trace.Traces{{Name: trace.NameTurn, Time: at(0)}})
	require.Len(t, got, 2)
	assert.Equal(t, at(0), got[0].Turn, "a continuation of the answer of the newest turn keeps its name")
	assert.True(t, got[1].Turn.IsZero(), "an answer of a turn that left no trace leaves its calls unnamed")
}

// A turn whose prompt left no trace keeps its calls from the answer before it whether one pass or two read them
// and a continuation of that earlier answer stays with it
func TestTranscriptPartGroupedPassesOfAnUntracedTurn(t *testing.T) {
	at := func(s int) time.Time { return captureDay.Add(time.Duration(s) * time.Second) }
	answer := trace.Trace{
		ID:         "a",
		Name:       trace.NameRun,
		SessionID:  "s1",
		Time:       at(10),
		DurationMS: 10000,
		Input:      json.RawMessage(`{"applied":[]}`),
	}
	continued := trace.Trace{
		ID:         "c",
		Name:       trace.NameRun,
		SessionID:  "s1",
		Time:       at(14),
		DurationMS: 4000,
		Input:      json.RawMessage(`{"applied":[],"continued":true}`),
	}
	untraced := trace.Trace{
		ID:        "r2",
		Name:      trace.NameRun,
		SessionID: "s1",
		Time:      at(30),
		Input:     json.RawMessage(`{"applied":[]}`),
	}
	turns := trace.Traces{{ID: "t0", Name: trace.NameTurn, SessionID: "s1", Time: at(0)}}
	shaped := func(s int, command string) shapedCall {
		return shapedCall{at: at(s), shapes: call.Shapes{{Tool: "Bash", Command: command}}}
	}
	message := personMessages{{text: "next", at: at(20)}}
	whole := transcriptPart{
		calls:    []shapedCall{shaped(12, "git push"), shaped(22, "git commit"), shaped(25, "gh pr create")},
		messages: message,
	}
	early := transcriptPart{calls: []shapedCall{shaped(12, "git push"), shaped(22, "git commit")}, messages: message}
	late := transcriptPart{calls: []shapedCall{shaped(25, "gh pr create")}}
	tcs := []struct {
		name string
		args []call.Input
	}{
		{"one pass", whole.grouped(trace.Traces{answer, continued, untraced}, turns)},
		{
			"two passes",
			append(
				early.grouped(trace.Traces{answer, continued}, turns),
				late.grouped(trace.Traces{answer, continued, untraced}, turns)...),
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			session := trace.Traces{answer, continued, untraced, turns[0]}
			for _, g := range tc.args {
				in, err := json.Marshal(g)
				require.NoError(t, err)
				session = append(session, trace.Trace{Name: trace.NameCalls, SessionID: "s1", Time: g.At, Input: in})
			}
			assert.Equal(t, call.Shapes{{Tool: "Bash", Command: "git push"}}, knowledge.TurnCalls(answer, session))
			assert.Equal(
				t,
				call.Shapes{{Tool: "Bash", Command: "git commit"}, {Tool: "Bash", Command: "gh pr create"}},
				knowledge.TurnCalls(untraced, session),
			)
		})
	}
}

// The calls of a turn that left no trace join its answer and never the answer before however short that one was
func TestTranscriptPartGroupedUntracedTurnName(t *testing.T) {
	at := func(ms int) time.Time { return captureDay.Add(time.Duration(ms) * time.Millisecond) }
	answer := func(id string, ms, duration int) trace.Trace {
		return trace.Trace{
			ID:         id,
			Name:       trace.NameRun,
			SessionID:  "s1",
			Time:       at(ms),
			DurationMS: int64(duration),
			Input:      json.RawMessage(`{"applied":[]}`),
		}
	}
	turns := trace.Traces{{ID: "t0", Name: trace.NameTurn, SessionID: "s1", Time: at(0)}}
	tcs := []struct {
		name string
		args trace.Trace
	}{
		{"the next answer dates no turn", answer("b", 20, 0)},
		{"the next answer dates its turn", answer("b", 20, 10)},
		{"the next answer ends a millisecond after the one before", answer("b", 2, 0)},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			first := answer("a", 1, 1)
			p := transcriptPart{
				calls: []shapedCall{
					{
						at:     first.Time.Add(tc.args.Time.Sub(first.Time) / 2),
						shapes: call.Shapes{{Tool: "Bash", Command: "git push"}},
					},
				},
			}
			session := trace.Traces{first, tc.args, turns[0]}
			for _, g := range p.grouped(trace.Traces{first, tc.args}, turns) {
				in, err := json.Marshal(g)
				require.NoError(t, err)
				session = append(session, trace.Trace{Name: trace.NameCalls, SessionID: "s1", Time: g.At, Input: in})
			}
			assert.Empty(t, knowledge.TurnCalls(first, session))
			assert.Equal(t, call.Shapes{{Tool: "Bash", Command: "git push"}}, knowledge.TurnCalls(tc.args, session))
		})
	}
}

// Two turns in a row whose prompts left no trace keep their calls apart whether one pass or two read the second
func TestTranscriptPartGroupedTwoUntracedTurns(t *testing.T) {
	at := func(s int) time.Time { return captureDay.Add(time.Duration(s) * time.Second) }
	answer := func(id string, s int, duration int64) trace.Trace {
		return trace.Trace{
			ID:         id,
			Name:       trace.NameRun,
			SessionID:  "s1",
			Time:       at(s),
			DurationMS: duration,
			Input:      json.RawMessage(`{"applied":[]}`),
		}
	}
	first, second, third := answer("a", 10, 10000), answer("r1", 30, 0), answer("r2", 50, 0)
	turns := trace.Traces{{ID: "t0", Name: trace.NameTurn, SessionID: "s1", Time: at(0)}}
	shaped := func(s int, command string) shapedCall {
		return shapedCall{at: at(s), shapes: call.Shapes{{Tool: "Bash", Command: command}}}
	}
	before := transcriptPart{
		calls:    []shapedCall{shaped(22, "git commit")},
		messages: personMessages{{text: "next", at: at(20)}},
	}
	early := transcriptPart{
		calls:    []shapedCall{shaped(42, "make")},
		messages: personMessages{{text: "then", at: at(40)}},
	}
	late := transcriptPart{calls: []shapedCall{shaped(45, "gh pr create")}}
	whole := transcriptPart{
		calls:    []shapedCall{shaped(42, "make"), shaped(45, "gh pr create")},
		messages: personMessages{{text: "then", at: at(40)}},
	}
	read := before.grouped(trace.Traces{first, second}, turns)
	tcs := []struct {
		name string
		args []call.Input
	}{
		{"one pass", append(slices.Clone(read), whole.grouped(trace.Traces{first, second, third}, turns)...)},
		{
			"two passes",
			slices.Concat(
				read,
				early.grouped(trace.Traces{first, second}, turns),
				late.grouped(trace.Traces{first, second, third}, turns),
			),
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			session := trace.Traces{first, second, third, turns[0]}
			for _, g := range tc.args {
				in, err := json.Marshal(g)
				require.NoError(t, err)
				session = append(session, trace.Trace{Name: trace.NameCalls, SessionID: "s1", Time: g.At, Input: in})
			}
			assert.Equal(t, call.Shapes{{Tool: "Bash", Command: "git commit"}}, knowledge.TurnCalls(second, session))
			assert.Equal(
				t,
				call.Shapes{{Tool: "Bash", Command: "make"}, {Tool: "Bash", Command: "gh pr create"}},
				knowledge.TurnCalls(third, session),
			)
		})
	}
}

// The capture keeps the first call.Kept distinct shapes of a group in the order first seen
func TestRunSessionCaptureCallsKept(t *testing.T) {
	tcs := []struct {
		name string
		args int
		want int
	}{
		{"under the bound", call.Kept - 1, call.Kept - 1},
		{"at the bound", call.Kept, call.Kept},
		{"past the bound", call.Kept + 1, call.Kept},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := newCaptureBench(t, twoStops, "deferred")
			use := func(id, name string) map[string]any {
				return map[string]any{"type": "tool_use", "id": id, "name": name, "input": map[string]any{}}
			}
			var blocks []map[string]any
			for i := range tc.args {
				blocks = append(blocks, use(fmt.Sprintf("t%d", i), fmt.Sprintf("Tool%d", i)))
			}
			blocks = append(blocks, use("again", "Tool0"))
			lines := []map[string]any{
				{
					"type": "user", "sessionId": "s1", "cwd": "/work/acme", "uuid": "u1", "timestamp": "2026-10-13T09:00:00.000Z",
					"message": map[string]any{"role": "user", "content": "Run the tools"},
				},
				{
					"type": "assistant", "sessionId": "s1", "cwd": "/work/acme", "uuid": "a1", "parentUuid": "u1",
					"timestamp": "2026-10-13T09:00:03.000Z",
					"message":   map[string]any{"role": "assistant", "content": blocks},
				},
			}
			var content []byte
			for _, l := range lines {
				raw, err := json.Marshal(l)
				require.NoError(t, err)
				content = append(append(content, raw...), '\n')
			}
			path := b.transcriptPath(t)
			require.NoError(t, os.WriteFile(path, content, 0o600))
			client, _ := captureClient(t, sureNeither, nil)
			code, _, stderr := b.capture(t, client, path, captureDay.Add(2*time.Minute))
			require.Equal(t, 0, code, stderr)
			got := b.calls(t)
			require.Len(t, got, 1)
			require.Len(t, got[0].Calls, tc.want)
			for i, s := range got[0].Calls {
				assert.Equal(t, call.Shape{Tool: fmt.Sprintf("Tool%d", i)}, s)
			}
		})
	}
}
