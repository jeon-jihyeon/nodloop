// Package loop reads the records of the runs and what people said about them and tells a person what to check next
package loop

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// One recorded run as the views read it
type entry struct {
	trace trace.Trace
	// What the run names in its input
	input knowledge.RunInput
}

// Whether both runs come from the same place: the same producer and every label
func (r entry) samePlace(other entry) bool {
	return r.trace.Producer == other.trace.Producer &&
		maps.EqualFunc(r.trace.Labels, other.trace.Labels, slices.Equal[[]string])
}

// The knowledge versions the run applied
func (r entry) applied() []knowledge.Ref {
	return r.input.Applied
}

// The versions the run applied or would have applied had the holdout not kept them
// A view that must not tell the arms apart reads these
func (r entry) received() []knowledge.Ref {
	if len(r.input.Applied) > 0 {
		return r.input.Applied
	}
	return r.input.Withheld
}

// The arm of a drawn run with items to receive and empty for any other run
func (r entry) arm() Arm {
	switch {
	case !r.input.Drawn:
		return ""
	case len(r.input.Applied) > 0:
		return ArmApplied
	case len(r.input.Withheld) > 0:
		return ArmWithheld
	}
	return ""
}

// Whether the run is the first to receive one of its versions
// first holds the earliest run per version
func (r entry) introduces(first map[knowledge.Ref]trace.Trace) bool {
	for _, ref := range r.received() {
		if first[ref].ID == r.trace.ID {
			return true
		}
	}
	return false
}

// Whether something was received and every received version is stated
// A version missing from versions counts as not stated so an unknown item never raises the signal
func (r entry) statedOnly(versions map[knowledge.Ref]knowledge.Knowledge) bool {
	applied := r.received()
	for _, ref := range applied {
		if k, ok := versions[ref]; !ok || k.Basis != knowledge.BasisStated {
			return false
		}
	}
	return len(applied) > 0
}

// The runs as the records tell them joined once
// 1. every run without error joins with the versions its input names, a run whose input does not read applied none
// 2. the verdict and the outcome of a run are the latest ones a person gave
type History struct {
	// Newest first as the store lists them
	entries []entry
	// Latest human records per trace id
	verdicts map[string]feedback.Feedback
	outcomes map[string]feedback.Outcome
	// Trace ids of the runs without error that have any feedback or any outcome
	withFeedback map[string]bool
	withOutcome  map[string]bool
	knowledge    knowledge.Set
	// The labels the runs of each producer carry
	vocabulary map[string]trace.Labels
	// The runs with every verdict so a lifetime signal sees what the conversation inferred too
	runs Runs
	// When a run last applied each version
	// Read over every run and not only the window
	lastApplied map[knowledge.Ref]time.Time
	// The earliest run that received each version applied or withheld
	// Read over every run so a window never makes a later run the first
	firstReceived map[knowledge.Ref]trace.Trace
}

type TraceStore interface {
	List(ctx context.Context, f trace.Filter) (trace.Traces, error)
}

type FeedbackStore interface {
	List(ctx context.Context, f feedback.Filter) ([]feedback.Feedback, error)
}

type OutcomeStore interface {
	List(ctx context.Context, traceID string) ([]feedback.Outcome, error)
}

type KnowledgeStore interface {
	All(ctx context.Context) (knowledge.Set, error)
}

// Every record of the stores read once and joined by New over the window from since on
func Load(
	ctx context.Context, traces TraceStore, verdicts FeedbackStore, outcomes OutcomeStore, items KnowledgeStore, since time.Time,
) (*History, error) {
	all, err := traces.List(ctx, trace.Filter{Name: trace.NameRun})
	if err != nil {
		return nil, err
	}
	records, err := verdicts.List(ctx, feedback.Filter{})
	if err != nil {
		return nil, err
	}
	checks, err := outcomes.List(ctx, "")
	if err != nil {
		return nil, err
	}
	set, err := items.All(ctx)
	if err != nil {
		return nil, err
	}
	return New(all, records, checks, set, since), nil
}

// The history of the runs since the time and the zero time reads every run
// 1. runs before since are left out so a retention window never mixes every judged run with the unjudged runs that survived
// 2. when a version was last applied and the first run to receive it are read over every run
// A run before the window still counts as a use of the version and as its first run
func New(traces trace.Traces, verdicts feedback.Records, outcomes feedback.Outcomes, items knowledge.Set, since time.Time) *History {
	window := slices.DeleteFunc(slices.Clone(traces), trace.Filter{Since: since}.Older)
	h := &History{
		verdicts: map[string]feedback.Feedback{}, outcomes: map[string]feedback.Outcome{},
		withFeedback: map[string]bool{}, withOutcome: map[string]bool{}, knowledge: items, vocabulary: map[string]trace.Labels{},
		runs: NewRuns(window, verdicts), lastApplied: map[knowledge.Ref]time.Time{}, firstReceived: map[knowledge.Ref]trace.Trace{},
	}
	recorded := map[string]bool{}
	for _, tr := range traces {
		if tr.Name != trace.NameRun || tr.Error != "" {
			continue
		}
		var in knowledge.RunInput
		_ = json.Unmarshal(tr.Input, &in)
		for _, ref := range in.Applied {
			if tr.Time.After(h.lastApplied[ref]) {
				h.lastApplied[ref] = tr.Time
			}
		}
		r := entry{trace: tr, input: in}
		h.receive(r)
		if tr.Time.Before(since) {
			continue
		}
		recorded[tr.ID] = true
		h.entries = append(h.entries, r)
		if _, ok := h.vocabulary[tr.Producer]; !ok {
			h.vocabulary[tr.Producer] = window.Vocabulary(tr.Producer)
		}
	}
	for _, fb := range verdicts {
		h.withFeedback[fb.TraceID] = recorded[fb.TraceID]
	}
	for _, o := range outcomes {
		h.withOutcome[o.TraceID] = recorded[o.TraceID]
	}
	for _, fb := range verdicts.Human().Latest() {
		h.verdicts[fb.TraceID] = fb
	}
	for _, o := range outcomes.Human().Latest() {
		h.outcomes[o.TraceID] = o
	}
	return h
}

// Keeps the run as the first to receive each of its versions when it is earlier than the one kept
// Runs of one time take the lower trace id so the order of the store never decides
func (h *History) receive(r entry) {
	for _, ref := range r.received() {
		old, ok := h.firstReceived[ref]
		if !ok || r.trace.Time.Before(old.Time) || (r.trace.Time.Equal(old.Time) && r.trace.ID < old.ID) {
			h.firstReceived[ref] = r.trace
		}
	}
}

// The recorded output per trace id for the ids that name a run
func (h *History) Outputs(traceIDs []string) map[string]json.RawMessage {
	wanted := make(map[string]bool, len(traceIDs))
	for _, id := range traceIDs {
		wanted[id] = true
	}
	out := make(map[string]json.RawMessage, len(traceIDs))
	for _, r := range h.entries {
		if wanted[r.trace.ID] {
			out[r.trace.ID] = r.trace.Output
		}
	}
	return out
}
