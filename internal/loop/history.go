// Package loop reads the records of the runs and what people said about them and tells a person what to check next
package loop

import (
	"context"
	"encoding/json"
	"maps"
	"slices"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// One recorded run as the views read it
type review struct {
	trace trace.Trace
	// The versions the run names in its input
	runApplied []knowledge.Ref
}

// Whether both runs come from the same place: the same producer and every label
func (r review) samePlace(other review) bool {
	return r.trace.Producer == other.trace.Producer &&
		maps.EqualFunc(r.trace.Labels, other.trace.Labels, slices.Equal[[]string])
}

// The knowledge versions the run applied
func (r review) applied() []knowledge.Ref {
	return r.runApplied
}

// Whether the run is the first to apply one of its versions
// first names the earliest run per version
func (r review) introduces(first map[knowledge.Ref]string) bool {
	for _, ref := range r.applied() {
		if first[ref] == r.trace.ID {
			return true
		}
	}
	return false
}

// Whether something was applied and every applied version is stated
// A version missing from versions counts as not stated so an unknown item never raises the signal
func (r review) statedOnly(versions map[knowledge.Ref]knowledge.Knowledge) bool {
	applied := r.applied()
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
	reviews []review
	// Latest human records per trace id
	verdicts map[string]feedback.Feedback
	outcomes map[string]feedback.Outcome
	// Trace ids of the runs without error that have any feedback or any outcome
	withFeedback map[string]bool
	withOutcome  map[string]bool
	knowledge    knowledge.Set
	// The labels the runs of each producer carry
	vocabulary map[string]trace.Labels
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

// Every record of the stores read once and joined
func Load(
	ctx context.Context, traces TraceStore, verdicts FeedbackStore, outcomes OutcomeStore, items KnowledgeStore,
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
	return New(all, records, checks, set), nil
}

func New(traces trace.Traces, verdicts feedback.Records, outcomes feedback.Outcomes, items knowledge.Set) *History {
	h := &History{
		verdicts: map[string]feedback.Feedback{}, outcomes: map[string]feedback.Outcome{},
		withFeedback: map[string]bool{}, withOutcome: map[string]bool{}, knowledge: items, vocabulary: map[string]trace.Labels{},
	}
	recorded := map[string]bool{}
	for _, tr := range traces {
		if tr.Name != trace.NameRun || tr.Error != "" {
			continue
		}
		recorded[tr.ID] = true
		var in struct {
			Applied []knowledge.Ref `json:"applied"`
		}
		_ = json.Unmarshal(tr.Input, &in)
		h.reviews = append(h.reviews, review{trace: tr, runApplied: in.Applied})
		if _, ok := h.vocabulary[tr.Producer]; !ok {
			h.vocabulary[tr.Producer] = traces.Vocabulary(tr.Producer)
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

// The recorded output per trace id for the ids that name a run
func (h *History) Outputs(traceIDs []string) map[string]json.RawMessage {
	wanted := make(map[string]bool, len(traceIDs))
	for _, id := range traceIDs {
		wanted[id] = true
	}
	out := make(map[string]json.RawMessage, len(traceIDs))
	for _, r := range h.reviews {
		if wanted[r.trace.ID] {
			out[r.trace.ID] = r.trace.Output
		}
	}
	return out
}
