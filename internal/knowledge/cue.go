package knowledge

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/call"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// The tool call a judgment comes with instead of the prompt
// It moves when the item arrives and never what the item says or where it reaches
type Cue struct {
	Tool string `json:"tool"`
	// Words a command line of the call starts with
	Command string `json:"command,omitempty"`
	// The file shape the call's file matches
	File string `json:"file,omitempty"`
}

// The cue as the line a prompt and approved.md show
func (c Cue) String() string {
	s := "before each " + c.Tool + " call"
	if c.Command != "" {
		s += " running " + c.Command
	}
	if c.File != "" {
		s += " on a " + c.File + " file"
	}
	return s
}

// Fails with ErrCueInvalid naming the part outside the grammar of call shapes
func (c Cue) Check() error {
	switch {
	case !call.Tool(c.Tool):
		return fmt.Errorf("%w: tool %q", ErrCueInvalid, c.Tool)
	case c.Command != "" && !call.Command(c.Command):
		return fmt.Errorf("%w: command %q is not a listed program alone or with a subcommand it defines", ErrCueInvalid, c.Command)
	case c.File != "" && !call.File(c.File):
		return fmt.Errorf("%w: file %q is not a file shape such as *.go or *_test.go", ErrCueInvalid, c.File)
	}
	return nil
}

// Whether one shape of a call fits the cue
func (c Cue) Fits(shapes call.Shapes) bool {
	for _, s := range shapes {
		if s.Tool == c.Tool && (c.Command == "" || s.Starts(c.Command)) && (c.File == "" || s.Matches(c.File)) {
			return true
		}
	}
	return false
}

func (k Knowledge) checkCue() error {
	switch {
	case k.Cue == nil:
		return nil
	case k.Kind != KindJudgment:
		return ErrCueKind
	case k.Veto != nil || k.Run.Timed():
		return ErrCueVeto
	}
	return k.Cue.Check()
}

// Whether the new version keeps the cue of the old one
// A version may add a cue to an uncued item since that only moves when it arrives
func (k Knowledge) keepsCue(old *Cue) bool {
	return old == nil || k.Cue != nil && *k.Cue == *old
}

// The cued items in order
func (s Set) Cued() Set {
	out := Set{}
	for _, k := range s {
		if k.Cue != nil {
			out = append(out, k)
		}
	}
	return out
}

// The items without a cue in order
func (s Set) Uncued() Set {
	out := Set{}
	for _, k := range s {
		if k.Cue == nil {
			out = append(out, k)
		}
	}
	return out
}

// The cued items whose cue fits one of the shapes in order
func (s Set) Fitting(shapes call.Shapes) Set {
	out := Set{}
	for _, k := range s {
		if k.Cue != nil && k.Cue.Fits(shapes) {
			out = append(out, k)
		}
	}
	return out
}

// The start of the turn of a run read from its duration and zero when it recorded none
// The hook measures an answered run from the turn trace of its prompt in whole milliseconds
func TurnOf(run trace.Trace) time.Time {
	if run.DurationMS <= 0 {
		return time.Time{}
	}
	return run.Time.Add(-time.Duration(run.DurationMS) * time.Millisecond)
}

// The shapes of the calls of the turn of an answered run in the order they were made
// 1. the turn of the run is its newest turn trace and else its start read from its duration
// Retention removes turn traces so the duration stands in for them
// 2. a calls trace that names its turn joins when that is the turn of the run whenever its calls were made
// So the calls a stop hook continuation made after the answer join it and those of an interrupted turn before it never do
// 3. a calls trace that names no turn joins by time: after the newest answered run or turn before the run and before the next one after it
// 4. session holds the runs, turns and calls traces of the session of the run in any order
// 5. a run of no session has no turn and no calls
func TurnCalls(run trace.Trace, session trace.Traces) call.Shapes {
	if run.SessionID == "" {
		return nil
	}
	span := spanOf(run, session)
	var inputs call.Inputs
	for _, t := range session {
		var in call.Input
		if t.Name == trace.NameCalls && t.SessionID == run.SessionID && json.Unmarshal(t.Input, &in) == nil && span.joins(in) {
			inputs = append(inputs, in)
		}
	}
	slices.SortStableFunc(inputs, func(a, b call.Input) int { return a.At.Compare(b.At) })
	var out call.Shapes
	for _, in := range inputs {
		for _, s := range in.Calls {
			out = out.With(s)
		}
	}
	return out
}

// The turn of an answered run and the bounds of its time between the answers and turns around it
type turnSpan struct {
	began, ended, turn time.Time
	// The time of the run
	// A turn named after it belongs to a later turn
	at time.Time
	// The newest answered run before the run
	// A turn trace before it belongs to an earlier turn
	answered time.Time
}

// 1. the span begins at the newest answered run or turn before the run or at the turn of the run when that is later
// A turn trace before the newest answer is an earlier turn
// So a turn whose prompt left no trace has none and joins by time
// 2. it ends where the next turn begins
// That is the next turn trace and else the turn of the next answer read from its duration and else that answer
// So calls a continuation made after the answer stay in the span and the calls of the next turn never join it
func spanOf(run trace.Trace, session trace.Traces) turnSpan {
	span := turnSpan{at: run.Time}
	for _, t := range session {
		if t.SessionID == run.SessionID && (t.Name == trace.NameTurn || t.Name == trace.NameRun && Answered(t)) {
			span.see(run.Time, t)
		}
	}
	if span.turn.Before(span.answered) {
		span.turn = time.Time{}
	}
	if span.turn.IsZero() {
		span.turn = TurnOf(run)
	}
	if start := span.turn.Add(-call.TurnSlack); start.After(span.began) {
		span.began = start
	}
	return span
}

// The span with one turn or answered run of the session taken in
func (span *turnSpan) see(at time.Time, t trace.Trace) {
	start := t.Time
	if t.Name == trace.NameRun && !TurnOf(t).IsZero() {
		start = TurnOf(t)
	}
	switch {
	case t.Time.Before(at) && t.Time.After(span.began):
		span.began = t.Time
	case t.Time.After(at) && (span.ended.IsZero() || start.Before(span.ended)):
		span.ended = start
	}
	if t.Name == trace.NameTurn && t.Time.Before(at) && t.Time.After(span.turn) {
		span.turn = t.Time
	}
	if t.Name == trace.NameRun && t.Time.Before(at) && t.Time.After(span.answered) {
		span.answered = t.Time
	}
}

// Whether the calls belong to the turn by the turn they name and else by their time
// Calls named by a turn before the answer before the run belong to an earlier turn whatever their time
// and those named by a turn after the run to a later one
func (span turnSpan) joins(in call.Input) bool {
	if !in.Turn.IsZero() && (in.Turn.Before(span.answered) || in.Turn.After(span.at)) {
		return false
	}
	if !in.Turn.IsZero() && !span.turn.IsZero() {
		return in.InTurn(span.turn)
	}
	return in.At.After(span.began) && (span.ended.IsZero() || in.At.Before(span.ended))
}

// Whether the run is an answer the person read and no stop hook continued
// An input that does not decode reads as an answer like every run of an earlier release
func Answered(run trace.Trace) bool {
	var in RunInput
	return json.Unmarshal(run.Input, &in) != nil || !in.Continued
}
