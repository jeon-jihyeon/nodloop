// Package loop reads the records of the conversation and tells a person what to check next
package loop

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/jeon-jihyeon/nodloop/internal/diagnose"
	"github.com/jeon-jihyeon/nodloop/internal/evidence"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// One recorded review as the views read it
type review struct {
	trace trace.Trace
	diagnose.Recorded
}

// The knowledge versions that reached the model once each
func (r review) applied() []knowledge.Ref {
	var out []knowledge.Ref
	for _, k := range r.Knowledge {
		ref := knowledge.Ref{ID: k.ID, Version: k.Version}
		if k.Chars > 0 && !slices.Contains(out, ref) {
			out = append(out, ref)
		}
	}
	return out
}

// Whether the review is the first to apply one of its versions
// first names the earliest review per version
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

// The conversation as the records tell it joined once
// 1. a conversation review is a diagnose trace without error written in the interactive mode or by a one-off batch run without a session
// 2. every eval run names a session so its reviews stay out
// 3. the verdict and the outcome of a review are the latest ones a person gave
// 4. the first submission of a review is the revise trace of its context
type History struct {
	// Newest first as the store lists them
	reviews []review
	// The status record sent back per context id
	first map[string]evidence.Status
	// Latest human records per trace id
	verdicts map[string]feedback.Feedback
	outcomes map[string]feedback.Outcome
	// Trace ids of the diagnose traces without error that have any feedback or any outcome
	withFeedback map[string]bool
	withOutcome  map[string]bool
	knowledge    knowledge.Set
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
	all, err := traces.List(ctx, trace.Filter{})
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
	return New(all, records, checks, set)
}

// Fails with diagnose.ErrMalformed when a diagnose trace without error does not read
func New(traces trace.Traces, verdicts feedback.Records, outcomes feedback.Outcomes, items knowledge.Set) (*History, error) {
	h := &History{
		first: map[string]evidence.Status{}, verdicts: map[string]feedback.Feedback{}, outcomes: map[string]feedback.Outcome{},
		withFeedback: map[string]bool{}, withOutcome: map[string]bool{}, knowledge: items,
	}
	recorded := map[string]bool{}
	for _, tr := range traces {
		switch {
		case tr.Error != "":
		case tr.Name == trace.NameRevise:
			h.noteFirst(tr)
		case tr.Name == trace.NameDiagnose:
			rec, err := diagnose.ReadRecorded(tr)
			if err != nil {
				return nil, err
			}
			recorded[tr.ID] = true
			if rec.Mode == diagnose.ModeInteractive || tr.SessionID == "" {
				h.reviews = append(h.reviews, review{trace: tr, Recorded: rec})
			}
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
	return h, nil
}

// Traces come newest first so the oldest revise trace of a context is written last and wins
// record sends a context back once so there is one in practice
// A revise output that does not read names no status and the review counts as its own first submission
func (h *History) noteFirst(tr trace.Trace) {
	var diag diagnose.Diagnosis
	if json.Unmarshal(tr.Output, &diag) != nil || tr.Ref == "" {
		return
	}
	h.first[tr.Ref] = diag.Status
}

// The recorded review per trace id for the ids that name a conversation review
func (h *History) Reviews(traceIDs []string) map[string]json.RawMessage {
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

// The status of the first submission of the review
func (h *History) firstStatus(r review) evidence.Status {
	if status, ok := h.first[r.trace.Ref]; ok {
		return status
	}
	return r.Diagnosis.Status
}
