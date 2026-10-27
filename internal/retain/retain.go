// Package retain removes the run traces nobody judged or cited once they pass the retention window
package retain

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"math"
	"slices"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/call"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// The shortest retention
// A reader that looks back for the latest run of a session looks back at most this far so a prune never removes that run
const MinDays = 7

// Whole days of traces a prune keeps
// 0 keeps every trace so a record directory changes nothing until its user sets one
type Days int

func (d Days) Check() error {
	if d > Days(math.MaxInt64/int64(24*time.Hour)) {
		return fmt.Errorf("%w: %d", ErrDaysTooLong, d)
	}
	if d == 0 || d >= MinDays {
		return nil
	}
	return fmt.Errorf("%w: %d. Give 0 to keep every trace or %d or more days", ErrDaysTooShort, d, MinDays)
}

// Runs before the cutoff may be removed and the zero time keeps every run
// 1. days that Check refuses give the zero time so a report never reads a window a prune would refuse
// 2. whole days of 24 hours so a cutoff never moves with a change of daylight saving
func (d Days) Cutoff(now time.Time) time.Time {
	if d == 0 || d.Check() != nil {
		return time.Time{}
	}
	return now.Add(-time.Duration(d) * 24 * time.Hour)
}

// The start of the runs a report reads and the zero time for every run
// 1. the later of the cutoff of the days and the cutoff of the last prune of the store
// So a store pruned by a surface whose retention this one never set still reads only runs no prune touched
// 2. a last cutoff after now comes from a clock set back and is left out
func (d Days) Window(now, lastCutoff time.Time) time.Time {
	cutoff := d.Cutoff(now)
	if lastCutoff.After(cutoff) && !lastCutoff.After(now) {
		return lastCutoff
	}
	return cutoff
}

// The traces a prune reads, the rollups it appends and the one removal it calls
type TraceStore interface {
	Heads(ctx context.Context, names ...string) ([]record.Trace, error)
	Append(ctx context.Context, tr record.Trace) error
	// Removes the traces of the ids decide returns
	// The store runs decide where no run can be cited unseen between it and the removal as far as the store can tell
	Prune(ctx context.Context, decide func(context.Context) ([]string, error)) (int, error)
}

// Counts a closed cycle as the reports read it in place of its runs and gives the input of its rollup trace
// The composition root passes the count of loop so a cycle reads alike from its rollup and from its runs
type Count func(ctx context.Context, c trace.Cycle) (json.RawMessage, error)

type FeedbackStore interface {
	List(ctx context.Context, f record.FeedbackFilter) ([]record.Feedback, error)
}

type OutcomeStore interface {
	List(ctx context.Context, traceID string) ([]record.Outcome, error)
}

type KnowledgeStore interface {
	All(ctx context.Context) (knowledge.Set, error)
}

// Reads every store that may cite a run and removes only from the trace store
type Retainer struct {
	traces   TraceStore
	verdicts FeedbackStore
	outcomes OutcomeStore
	items    KnowledgeStore
	count    Count
	now      func() time.Time
}

func New(traces TraceStore, verdicts FeedbackStore, outcomes OutcomeStore, items KnowledgeStore, count Count, now func() time.Time) *Retainer {
	return &Retainer{traces: traces, verdicts: verdicts, outcomes: outcomes, items: items, count: count, now: now}
}

// What one prune removes and keeps
type Plan struct {
	// The earlier of the cutoff the prune was asked for and the start of the open cycle
	Cutoff time.Time
	// Run traces older than the cutoff that nothing keeps
	Runs []string
	// Classify traces whose Ref is one of Runs whatever their age
	Classify []string
	// Calls traces older than the cutoff whose own answered run is gone from the store
	// A run this plan removes still owns its calls so they go at the next prune once the run is gone
	// So a verdict that keeps a run at the last moment keeps its calls too and no store needs to tell whom calls belong to
	Calls []string
	// Turn, cue, instructions and capture traces older than the cutoff since a later record holds what they held
	// 1. a run holds the duration of its turn and a newer turn carries the refs a cue trace added
	// 2. an anchor version holds the files it read
	// 3. only the newest capture of a session holds an offset still read
	Session []string
	// Run traces older than the cutoff that a citation or the first or last use of an approved version keeps
	Kept int
	// The closed cycles the cutoff reaches that no rollup counts yet
	Rollups []trace.Cycle
	// The run each classify trace of the plan judged
	judged map[string]string
}

// The plan without the runs cited and without the classify traces of those runs
func (p Plan) without(cited map[string]bool) Plan {
	out := Plan{Cutoff: p.Cutoff, Kept: p.Kept, Session: p.Session, Rollups: p.Rollups, judged: p.judged, Calls: p.Calls}
	for _, id := range p.Runs {
		if cited[id] {
			out.Kept++
			continue
		}
		out.Runs = append(out.Runs, id)
	}
	for _, id := range p.Classify {
		if !cited[p.judged[id]] {
			out.Classify = append(out.Classify, id)
		}
	}
	return out
}

// The names a prune reads the heads of
var planned = []string{
	string(trace.NameRun), string(trace.NameClassify), string(trace.NameTurn), string(trace.NameCue), string(trace.NameCalls),
	string(trace.NameInstructions), string(trace.NameCapture), string(trace.NameKnob), string(trace.NameRollup),
}

// The traces older than the cutoff that nothing keeps
// 1. the cutoff moves back to the start of the open cycle so no run of the open cycle goes
// Before the first knob trace there is no cycle and the cutoff stays as asked
// 2. each closed cycle the cutoff reaches without a rollup is planned for one so Apply counts it before any of its runs goes
// 3. a run is kept by any verdict of a person or a session and by a withdraw
// 4. a run is kept by an outcome and by any knowledge version that cites it
// 5. the newest run that applied each approved version is kept so the health of the version still sees that use
// 6. the first run that received each approved version applied or withheld is kept so the queue never takes a later run for the first
// 7. a classify trace goes with the run it judged and with nothing else
// 8. a calls trace older than the cutoff goes once its own answered run is gone
// That is a prune after the one that removed the run
// 9. a turn, cue, instructions or capture trace older than the cutoff goes since a later record holds what it held
// 10. a trace of any other name such as check, decide or rollup is never planned
// 11. any read error fails the plan since a verdict on a corrupt line would leave its run unprotected
func (r *Retainer) Plan(ctx context.Context, cutoff time.Time) (Plan, error) {
	plan := Plan{Cutoff: cutoff, judged: map[string]string{}}
	if cutoff.IsZero() {
		return plan, nil
	}
	items, err := r.items.All(ctx)
	if err != nil {
		return Plan{}, err
	}
	keep, err := r.cited(ctx, items)
	if err != nil {
		return Plan{}, err
	}
	found, err := r.traces.Heads(ctx, planned...)
	if err != nil {
		return Plan{}, err
	}
	hs := heads(trace.RestoreAll(found))
	plan.Cutoff, plan.Rollups = hs.cycles(cutoff, r.now())
	approved := items.Approved().Refs()
	for id := range hs.lastToApply(approved) {
		keep[id] = true
	}
	for id := range hs.firstToReceive(approved) {
		keep[id] = true
	}
	removed := map[string]bool{}
	plan.Session = hs.sessionBefore(plan.Cutoff)
	for _, t := range hs {
		switch {
		case t.Name != trace.NameRun || !t.Time.Before(plan.Cutoff):
		case keep[t.ID]:
			plan.Kept++
		default:
			removed[t.ID] = true
			plan.Runs = append(plan.Runs, t.ID)
		}
	}
	for _, t := range hs {
		if t.Name == trace.NameClassify && removed[t.Ref] {
			plan.Classify = append(plan.Classify, t.ID)
			plan.judged[t.ID] = t.Ref
		}
	}
	plan.Calls = hs.callsBefore(plan.Cutoff)
	return plan, nil
}

// The calls traces older than the cutoff whose own answered run is gone
func (hs heads) callsBefore(cutoff time.Time) []string {
	answers := map[string]sessionAnswers{}
	for _, t := range hs {
		if t.Name == trace.NameRun && t.SessionID != "" && knowledge.Answered(t) {
			answers[t.SessionID] = append(answers[t.SessionID], t)
		}
	}
	for _, runs := range answers {
		slices.SortStableFunc(runs, func(a, b trace.Trace) int { return a.Time.Compare(b.Time) })
	}
	var out []string
	for _, t := range hs {
		if t.Name != trace.NameCalls || !t.Time.Before(cutoff) {
			continue
		}
		var in call.Input
		if json.Unmarshal(t.Input, &in) != nil || answers[t.SessionID].owner(in).ID == "" {
			out = append(out, t.ID)
		}
	}
	return out
}

// The answered runs of one session oldest first
type sessionAnswers trace.Traces

// The answered run of the turn of the calls and the zero trace when it is gone
// 1. calls that name their turn belong to the answer of that turn
// So do the calls a stop hook continuation made after it
// A turn named after an answer or before the answer before it is never its turn as TurnCalls reads it
// 2. calls that name none belong to the first answered run at or after them and else the newest before them
// Calls whose turn no answer carries have no owner
// The stored records cannot tell a run retention removed from a turn whose prompt left no trace
// The time join of TurnCalls may give them to either one
// So they stay while an answer around them stays
// A later answer of another turn never owns them so they go once their own run is gone
func (as sessionAnswers) owner(in call.Input) trace.Trace {
	if in.Turn.IsZero() && len(as) > 0 {
		if i := slices.IndexFunc(as, func(t trace.Trace) bool { return !t.Time.Before(in.At) }); i >= 0 {
			return as[i]
		}
		return as[len(as)-1]
	}
	for i, t := range as {
		turn := knowledge.TurnOf(t)
		if turn.IsZero() || !in.InTurn(turn) || in.Turn.After(t.Time) || in.Turn.Before(as.before(i)) {
			continue
		}
		return t
	}
	return trace.Trace{}
}

// The time of the newest answer before the answer at i and zero when none is
// An answer at the same time is no answer before it
func (as sessionAnswers) before(i int) time.Time {
	for j := i - 1; j >= 0; j-- {
		if as[j].Time.Before(as[i].Time) {
			return as[j].Time
		}
	}
	return time.Time{}
}

// The run ids a verdict or an outcome or a version of the items names
func (r *Retainer) cited(ctx context.Context, items knowledge.Set) (map[string]bool, error) {
	cited := map[string]bool{}
	verdicts, err := r.verdicts.List(ctx, record.FeedbackFilter{})
	if err != nil {
		return nil, err
	}
	for _, fb := range feedback.RestoreAll(verdicts) {
		cited[fb.TraceID] = true
	}
	outcomes, err := r.outcomes.List(ctx, "")
	if err != nil {
		return nil, err
	}
	for _, o := range feedback.RestoreOutcomes(outcomes) {
		cited[o.TraceID] = true
	}
	for _, k := range items {
		for _, id := range k.Evidence.TraceIDs() {
			cited[id] = true
		}
	}
	return cited, nil
}

// Removes the traces of the plan and returns how many the store removed
// 1. the rollups of the plan land before anything is removed so no run of a closed cycle goes uncounted
// 2. the store asks for the ids when its prune runs and the citations are read again then so a verdict recorded since the plan keeps its run
// 3. a verdict writer takes no lock of the trace store so a verdict recorded while the store writes the lines it keeps can still lose its run
func (r *Retainer) Apply(ctx context.Context, p Plan) (int, error) {
	if err := r.rollup(ctx, p.Rollups); err != nil {
		return 0, err
	}
	if len(p.Runs) == 0 && len(p.Session) == 0 && len(p.Calls) == 0 {
		return 0, nil
	}
	return r.traces.Prune(ctx, func(ctx context.Context) ([]string, error) {
		items, err := r.items.All(ctx)
		if err != nil {
			return nil, err
		}
		cited, err := r.cited(ctx, items)
		if err != nil {
			return nil, err
		}
		left := p.without(cited)
		return slices.Concat(left.Runs, left.Classify, left.Calls, left.Session), nil
	})
}

// Appends one rollup per closed cycle that none counts yet
// 1. the rollups are read again so a prune that ran beside this one since the plan never counts a cycle twice
// 2. a count or an append that fails fails the prune before it removes anything
func (r *Retainer) rollup(ctx context.Context, cycles []trace.Cycle) error {
	if len(cycles) == 0 {
		return nil
	}
	found, err := r.traces.Heads(ctx, string(trace.NameRollup))
	if err != nil {
		return err
	}
	counted := heads(trace.RestoreAll(found)).rolled()
	for _, c := range cycles {
		if counted[c.ID] {
			continue
		}
		input, err := r.count(ctx, c)
		if err != nil {
			return err
		}
		now := r.now()
		tr := trace.Trace{ID: trace.NewID(now), Name: trace.NameRollup, Subject: c.ID, Time: now, Input: input}
		if err := r.traces.Append(ctx, tr.Record()); err != nil {
			return err
		}
	}
	return nil
}

// Plans and applies the prune of the cutoff and returns how many traces the store removed
func (r *Retainer) Prune(ctx context.Context, cutoff time.Time) (int, error) {
	plan, err := r.Plan(ctx, cutoff)
	if err != nil {
		return 0, err
	}
	return r.Apply(ctx, plan)
}

// The traces newest first as a prune reads them
type heads trace.Traces

// The names of session traces a prune removes by age alone
var sessionNames = []trace.Name{trace.NameTurn, trace.NameCue, trace.NameInstructions, trace.NameCapture}

// The session traces older than the cutoff
func (hs heads) sessionBefore(cutoff time.Time) []string {
	var out []string
	for _, t := range hs {
		if slices.Contains(sessionNames, t.Name) && t.Time.Before(cutoff) {
			out = append(out, t.ID)
		}
	}
	return out
}

// The cycles of a rollup trace
type rollupMark struct {
	CycleID string `json:"cycle_id"`
}

// The cycle ids a rollup counts
// A rollup that does not decode counts no cycle so its cycle is counted again
func (hs heads) rolled() map[string]bool {
	out := map[string]bool{}
	for _, t := range hs {
		var m rollupMark
		if t.Name == trace.NameRollup && json.Unmarshal(t.Input, &m) == nil && m.CycleID != "" {
			out[m.CycleID] = true
		}
	}
	return out
}

// The cutoff a prune keeps to and the closed cycles it reaches that no rollup counts yet
// 1. the cutoff is the earlier of the asked one and the start of the open cycle so no run of the open cycle goes
// 2. before the first knob trace there is no cycle and the cutoff stays as asked
// 3. every cycle that starts before the cutoff is closed since the cutoff never passes the start of the open cycle
func (hs heads) cycles(cutoff, now time.Time) (time.Time, []trace.Cycle) {
	cycles := trace.Traces(hs).Cycles(now)
	if open, ok := cycles.Open(now); ok && open.Start.Before(cutoff) {
		cutoff = open.Start
	}
	counted := hs.rolled()
	var reached []trace.Cycle
	for _, c := range cycles {
		if c.Start.Before(cutoff) && !c.End.After(now) && !counted[c.ID] {
			reached = append(reached, c)
		}
	}
	return cutoff, reached
}

// The runs that applied nothing as the reports count it are left out: a failed run and a run whose input does not read
func (hs heads) inputs() iter.Seq2[trace.Trace, knowledge.RunInput] {
	return func(yield func(trace.Trace, knowledge.RunInput) bool) {
		for _, t := range hs {
			if t.Name != trace.NameRun || t.Error != "" {
				continue
			}
			var in knowledge.RunInput
			if json.Unmarshal(t.Input, &in) != nil {
				continue
			}
			if !yield(t, in) {
				return
			}
		}
	}
}

// The latest run by time that applied each of the versions as health reads the last use
func (hs heads) lastToApply(versions []knowledge.Ref) map[string]bool {
	last := map[knowledge.Ref]trace.Trace{}
	for t, in := range hs.inputs() {
		for _, ref := range in.Applied {
			if old, ok := last[ref]; !ok || t.Time.After(old.Time) {
				last[ref] = t
			}
		}
	}
	return pick(last, versions)
}

// The earliest run by time that received each of the versions applied or withheld as the queue reads the first
// Runs of one time take the lower trace id
func (hs heads) firstToReceive(versions []knowledge.Ref) map[string]bool {
	first := map[knowledge.Ref]trace.Trace{}
	for t, in := range hs.inputs() {
		for _, ref := range slices.Concat(in.Applied, in.Withheld) {
			if old, ok := first[ref]; !ok || t.Time.Before(old.Time) || (t.Time.Equal(old.Time) && t.ID < old.ID) {
				first[ref] = t
			}
		}
	}
	return pick(first, versions)
}

// The ids of the runs kept for the versions
func pick(runs map[knowledge.Ref]trace.Trace, versions []knowledge.Ref) map[string]bool {
	out := map[string]bool{}
	for _, ref := range versions {
		if t, ok := runs[ref]; ok {
			out[t.ID] = true
		}
	}
	return out
}
