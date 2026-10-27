package main

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/call"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
)

// The tool calls of one primary assistant line of a transcript
type shapedCall struct {
	at time.Time
	// The offset of the line so a call past a cut waits for the next capture
	offset int64
	shapes call.Shapes
}

// The shapes of the tool calls on a primary assistant line with no argument text
func (e transcriptEntry) shapes() call.Shapes {
	var blocks []struct {
		Type  string         `json:"type"`
		Name  string         `json:"name"`
		Input map[string]any `json:"input"`
	}
	if e.Type != "assistant" || !e.primary() || json.Unmarshal(e.Message.Content, &blocks) != nil {
		return nil
	}
	var out call.Shapes
	for _, b := range blocks {
		if b.Type != "tool_use" {
			continue
		}
		for _, s := range call.Of(b.Name, b.Input) {
			out = out.With(s)
		}
	}
	return out
}

// The calls of the part as one group per span between the stops of runs, the starts of turns and the messages the person typed
// and one for those after the newest of them
// 1. no group spans a stop or an interrupted turn
// 2. each group names the start of its turn so a reader still tells the turns apart once retention removed the turn traces
// 3. see callBounds turn for the turn a group names
func (p transcriptPart) grouped(runs, turns trace.Traces) []call.Input {
	b := p.bounds(runs, turns)
	var groups []call.Input
	last := -2
	for _, c := range p.calls {
		i := slices.IndexFunc(b.stops, func(stop time.Time) bool { return !stop.Before(c.at) })
		if i != last {
			groups = append(groups, call.Input{Turn: b.turn(c.at)})
			last = i
		}
		g := &groups[len(groups)-1]
		g.At = c.at
		for _, s := range c.shapes {
			g.Calls = g.Calls.With(s)
		}
	}
	return groups
}

// What tells the turns of the calls of a part apart
// Each list is in time order
type callBounds struct {
	// The stops of runs, the starts of turns and the typed messages no group spans
	stops []time.Time
	// The starts of turns
	starts []time.Time
	// The stops of answered runs and the runs themselves
	answers []time.Time
	runs    trace.Traces
	// The messages the person typed
	typed []time.Time
}

func (p transcriptPart) bounds(runs, turns trace.Traces) callBounds {
	b := callBounds{runs: runs}
	for _, t := range slices.Concat(runs, turns) {
		b.stops = append(b.stops, t.Time)
	}
	for _, t := range turns {
		b.starts = append(b.starts, t.Time)
	}
	for _, r := range runs {
		if knowledge.Answered(r) {
			b.answers = append(b.answers, r.Time)
		}
	}
	for _, m := range p.messages {
		if m.question == "" {
			b.typed = append(b.typed, m.at)
			b.stops = append(b.stops, m.at)
		}
	}
	for _, list := range [][]time.Time{b.stops, b.starts, b.answers, b.typed} {
		slices.SortFunc(list, time.Time.Compare)
	}
	return b
}

// The start of the turn a call at the time belongs to and zero when no trace names it
// 1. the newest turn trace at or before it
// 2. a typed message whose turn left no trace after the answer before it names the turn by its time
// 3. an answer after the call that dates no turn or another one names a turn that left no trace
// The call is of that turn when no turn trace and no typed message came between and that turn began by the call
// 4. else a newer answer that dates no turn or another one than that trace means its turn left no trace and the pass never read its message
// Those calls name no turn and join by time
func (b callBounds) turn(at time.Time) time.Time {
	turn, message := latest(b.starts, at), latest(b.typed, at)
	answer := lastAnswer(b.runs, at)
	switch {
	case !message.IsZero() && turn.Before(latest(b.answers, message.Add(-time.Nanosecond))):
		return message
	case b.leftNoTrace(at, turn):
		return b.untracedTurn(at)
	case answer.Time.After(turn) && b.untraced(answer, turn):
		return time.Time{}
	}
	return turn
}

// The name of the turn that left no trace of a call at the time
// 1. the turn the next answer dates
// 2. else a time just past the run before
// No earlier answer takes calls named after it and the next answer dates no turn so it joins them by time
func (b callBounds) untracedTurn(at time.Time) time.Time {
	if start := knowledge.TurnOf(nextAnswer(b.runs, at)); !start.IsZero() {
		return start
	}
	return latestRun(b.runs, at).Add(time.Nanosecond)
}

// Whether the answer dates no turn or another one than the turn
func (b callBounds) untraced(answer trace.Trace, turn time.Time) bool {
	return knowledge.TurnOf(answer).IsZero() || !(call.Input{Turn: turn}).InTurn(knowledge.TurnOf(answer))
}

// Whether the first answer after the call shows that its turn left no trace and nothing in between began another
func (b callBounds) leftNoTrace(at, turn time.Time) bool {
	next := nextAnswer(b.runs, at)
	return !turn.IsZero() && next.ID != "" && latest(b.starts, next.Time).Equal(turn) && !latest(b.typed, next.Time).After(at) &&
		b.untraced(next, turn) && !knowledge.TurnOf(next).After(at)
}

// The newest answered run at or before at and the zero trace when none is
func lastAnswer(runs trace.Traces, at time.Time) trace.Trace {
	var out trace.Trace
	for _, r := range runs {
		if knowledge.Answered(r) && !r.Time.After(at) && r.Time.After(out.Time) {
			out = r
		}
	}
	return out
}

// The first answered run after at and the zero trace when none is
func nextAnswer(runs trace.Traces, at time.Time) trace.Trace {
	var out trace.Trace
	for _, r := range runs {
		if knowledge.Answered(r) && r.Time.After(at) && (out.ID == "" || r.Time.Before(out.Time)) {
			out = r
		}
	}
	return out
}

// The time of the newest run at or before at and zero when none is
func latestRun(runs trace.Traces, at time.Time) time.Time {
	var out time.Time
	for _, r := range runs {
		if !r.Time.After(at) && r.Time.After(out) {
			out = r.Time
		}
	}
	return out
}

// The newest time at or before at and zero when none is
func latest(times []time.Time, at time.Time) time.Time {
	var out time.Time
	for _, t := range times {
		if !t.After(at) {
			out = t
		}
	}
	return out
}

// One calls trace per group in the order of the transcript
func (c captureCommand) recordCalls(ctx context.Context, store *tracefile.Store, session string, groups []call.Input) error {
	for _, g := range groups {
		g.Calls = g.Calls.Cut()
		// Shapes of strings and a time always encode
		input, _ := json.Marshal(g)
		now := c.app.now()
		tr := trace.Trace{ID: trace.NewID(now), Name: trace.NameCalls, SessionID: session, Time: now, Input: input}
		if err := store.Append(ctx, tr.Record()); err != nil {
			return err
		}
	}
	return nil
}
