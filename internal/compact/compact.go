// Package compact replaces the approved items of one knowledge folder with fewer items that lose nothing the old ones fixed
package compact

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// Author of a compaction drafted without a named author
const defaultAuthor = "claude"

// One correction behind an old item as the drafter reads it
type Correction struct {
	TraceID string           `json:"trace_id"`
	Verdict feedback.Verdict `json:"verdict"`
	Reason  string           `json:"reason"`
}

// What a draft is written from
type Folder struct {
	Anchor string
	knowledge.Compactable
	Corrections []Correction
	// A compaction of these items still waiting for its check and approval
	// Empty when none
	Pending string
}

// Verdicts of the evidence traces
// Newest first
type FeedbackStore interface {
	List(ctx context.Context, f record.FeedbackFilter) ([]record.Feedback, error)
}

// The evidence traces
// Newest first
type TraceStore interface {
	List(ctx context.Context, f record.TraceFilter) ([]record.Trace, error)
}

// The coverage checks and the compact traces of the drafts beside the runs
// Newest first
type CheckStore interface {
	Append(ctx context.Context, t record.Trace) error
	List(ctx context.Context, f record.TraceFilter) ([]record.Trace, error)
}

// Drafts and proposes and checks and approves a compaction through the ledger
// Nothing is kept between calls
type Compactor struct {
	ledger   *knowledge.Ledger
	traces   TraceStore
	feedback FeedbackStore
	checks   CheckStore
	now      func() time.Time
}

func New(ledger *knowledge.Ledger, traces TraceStore, verdicts FeedbackStore, checks CheckStore, now func() time.Time) *Compactor {
	return &Compactor{ledger: ledger, traces: traces, feedback: verdicts, checks: checks, now: now}
}

// The anchor and its compactable items with their corrections
// Fails with knowledge ErrNotFound as the ledger would refuse the anchor
func (c *Compactor) Folder(ctx context.Context, anchor string) (Folder, error) {
	all, err := c.ledger.All(ctx)
	if err != nil {
		return Folder{}, err
	}
	compactable, err := c.ledger.Compactable(ctx, anchor)
	if err != nil {
		return Folder{}, err
	}
	corrections, err := c.corrections(ctx, compactable.Items)
	if err != nil {
		return Folder{}, err
	}
	return Folder{Anchor: anchor, Compactable: compactable, Corrections: corrections, Pending: all.PendingCompaction(compactable.Items)}, nil
}

// Proposes the draft as a compaction of the anchor's folder
// The ledger runs the code checks and refuses with their sentinels
func (c *Compactor) Propose(ctx context.Context, anchor string, d Draft, author string) (knowledge.Compaction, error) {
	f, err := c.Folder(ctx, anchor)
	if err != nil {
		return knowledge.Compaction{}, err
	}
	return c.propose(ctx, f, d, author)
}

func (c *Compactor) propose(ctx context.Context, f Folder, d Draft, author string) (knowledge.Compaction, error) {
	drafts := make([]knowledge.Knowledge, 0, len(d.Items))
	for _, item := range d.Items {
		drafts = append(drafts, item.knowledge(f.Items, cmp.Or(author, defaultAuthor)))
	}
	found, err := c.traces.List(ctx, record.TraceFilter{Name: string(trace.NameRun)})
	if err != nil {
		return knowledge.Compaction{}, err
	}
	runs := trace.RestoreAll(found)
	if err := recordedLabels(runs, drafts); err != nil {
		return knowledge.Compaction{}, err
	}
	if err := keptReach(runs, f.Items, drafts); err != nil {
		return knowledge.Compaction{}, err
	}
	return c.ledger.ProposeCompaction(ctx, f.Anchor, drafts)
}

// One reconciled verdict per evidence trace in citation order
// A later inferred verdict never replaces a person or settles a dispute
func (c *Compactor) corrections(ctx context.Context, items knowledge.Set) ([]Correction, error) {
	found, err := c.feedback.List(ctx, record.FeedbackFilter{})
	if err != nil {
		return nil, err
	}
	verdicts := feedback.RestoreAll(found).Reconciled()
	byTrace := make(map[string]feedback.Feedback, len(verdicts))
	for _, verdict := range verdicts {
		byTrace[verdict.TraceID] = verdict
	}
	var ids []string
	seen := map[string]bool{}
	for _, k := range items {
		for _, id := range k.Evidence.TraceIDs() {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	out := []Correction{}
	for _, id := range ids {
		if verdict, ok := byTrace[id]; ok {
			out = append(out, Correction{TraceID: id, Verdict: verdict.Verdict, Reason: verdict.Reason})
		}
	}
	return out, nil
}

// Every draft names labels some recorded run of its producer carries
// A draft without a producer is left for the ledger to refuse with the scope it lacks
func recordedLabels(runs trace.Traces, drafts []knowledge.Knowledge) error {
	for i, k := range drafts {
		if k.Run == nil || k.Run.Producer == "" {
			continue
		}
		if err := k.Run.Recorded(runs.Vocabulary(k.Run.Producer)); err != nil {
			return fmt.Errorf("draft %d: %w", i+1, err)
		}
	}
	return nil
}

// Every recorded run an old item reached is reached by a draft that names it
// The ledger rejects wider reach
// This check rejects lost reach across the drafts
// so an old fact never silently stops reaching a place it held for
func keptReach(runs trace.Traces, old knowledge.Set, drafts []knowledge.Knowledge) error {
	for _, k := range old {
		for _, run := range runs {
			if !k.Run.Admits(run.Producer, run.Labels) || slices.ContainsFunc(drafts, func(d knowledge.Knowledge) bool {
				return d.Names(k) && d.Run.Admits(run.Producer, run.Labels)
			}) {
				continue
			}
			return fmt.Errorf("%w: no draft that names %s reaches the recorded run %s with %v, which %s reached",
				ErrReachLost, k.ID, run.ID, map[string][]string(run.Labels), k.ID)
		}
	}
	return nil
}
