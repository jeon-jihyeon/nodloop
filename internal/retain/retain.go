// Package retain removes the run traces nobody judged or cited once they pass the retention window
package retain

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"slices"
	"time"

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

// 0 or MinDays and more
func (d Days) Check() error {
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

// The traces a prune reads and the one removal it calls
type TraceStore interface {
	Heads(ctx context.Context, names ...string) ([]record.Trace, error)
	// Removes the traces of the ids decide returns
	// The store runs decide where no run can be cited unseen between it and the removal as far as the store can tell
	Prune(ctx context.Context, decide func(context.Context) ([]string, error)) (int, error)
}

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
}

func New(traces TraceStore, verdicts FeedbackStore, outcomes OutcomeStore, items KnowledgeStore) *Retainer {
	return &Retainer{traces: traces, verdicts: verdicts, outcomes: outcomes, items: items}
}

// What one prune removes and keeps
type Plan struct {
	Cutoff time.Time
	// Run traces older than the cutoff that nothing keeps
	Runs []string
	// Classify traces whose Ref is one of Runs whatever their age
	Classify []string
	// Turn traces older than the cutoff
	// The run of a turn holds its duration once recorded and nothing cites a turn
	Turns []string
	// Run traces older than the cutoff that a citation or the first or last use of an approved version keeps
	Kept int
	// The run each classify trace of the plan judged
	judged map[string]string
}

// The plan without the runs cited and without the classify traces of those runs
func (p Plan) without(cited map[string]bool) Plan {
	out := Plan{Cutoff: p.Cutoff, Kept: p.Kept, Turns: p.Turns, judged: p.judged}
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

// The traces older than the cutoff that nothing keeps
// 1. a run is kept by any verdict of a person or a session and by a withdraw
// 2. a run is kept by an outcome and by any knowledge version that cites it
// 3. the newest run that applied each approved version is kept so the health of the version still sees that use
// 4. the first run that received each approved version applied or withheld is kept so the queue never takes a later run for the first
// 5. a classify trace goes with the run it judged and with nothing else
// 6. a turn trace older than the cutoff goes since its run holds the duration
// 7. a trace of any other name such as check or refill is never planned
// 8. any read error fails the plan since a verdict on a corrupt line would leave its run unprotected
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
	found, err := r.traces.Heads(ctx, string(trace.NameRun), string(trace.NameClassify), string(trace.NameTurn))
	if err != nil {
		return Plan{}, err
	}
	hs := heads(trace.RestoreAll(found))
	approved := items.Approved().Refs()
	for id := range hs.lastToApply(approved) {
		keep[id] = true
	}
	for id := range hs.firstToReceive(approved) {
		keep[id] = true
	}
	removed := map[string]bool{}
	plan.Turns = hs.turnsBefore(cutoff)
	for _, t := range hs {
		switch {
		case t.Name != trace.NameRun || !t.Time.Before(cutoff):
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
	return plan, nil
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
// 1. the store asks for the ids when its prune runs and the citations are read again then so a verdict recorded since the plan keeps its run
// 2. a verdict writer takes no lock of the trace store so a verdict recorded while the store writes the lines it keeps can still lose its run
func (r *Retainer) Apply(ctx context.Context, p Plan) (int, error) {
	if len(p.Runs) == 0 && len(p.Turns) == 0 {
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
		return slices.Concat(left.Runs, left.Classify, left.Turns), nil
	})
}

// Plans and applies the prune of the cutoff and returns how many traces the store removed
func (r *Retainer) Prune(ctx context.Context, cutoff time.Time) (int, error) {
	plan, err := r.Plan(ctx, cutoff)
	if err != nil {
		return 0, err
	}
	return r.Apply(ctx, plan)
}

// The run, classify and turn traces newest first as a prune reads them
type heads trace.Traces

// The turn traces older than the cutoff
func (hs heads) turnsBefore(cutoff time.Time) []string {
	var out []string
	for _, t := range hs {
		if t.Name == trace.NameTurn && t.Time.Before(cutoff) {
			out = append(out, t.ID)
		}
	}
	return out
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
