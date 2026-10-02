// Package compact replaces the approved items of one crowded knowledge folder with fewer items that lose nothing the old ones fixed
package compact

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
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
	List(ctx context.Context, f feedback.Filter) ([]feedback.Feedback, error)
}

// The evidence traces
// Newest first
type TraceStore interface {
	List(ctx context.Context, f trace.Filter) (trace.Traces, error)
}

// The coverage checks
// Newest first
type CheckStore interface {
	Append(ctx context.Context, t trace.Trace) error
	List(ctx context.Context, f trace.Filter) (trace.Traces, error)
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

// The anchor and the approved items one run may carry with it, with the corrections behind them
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

// Whether a compaction is due: one run carries more than FolderItems of the items
func (f Folder) Due() bool {
	return f.Crowded()
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
	if err := c.recorded(ctx, drafts); err != nil {
		return knowledge.Compaction{}, err
	}
	return c.ledger.ProposeCompaction(ctx, f.Anchor, drafts)
}

// Approves the compaction on behalf of a named person once its newest coverage check passed
func (c *Compactor) Approve(ctx context.Context, id, approver string) (knowledge.Compaction, error) {
	cov, err := c.Coverage(ctx, id)
	if err != nil {
		return knowledge.Compaction{}, err
	}
	return c.ledger.ApproveCompaction(ctx, id, approver, cov)
}

// The latest verdict on every evidence trace of the items, once each in the order the items cite them
// The first listed record of a trace is its newest
func (c *Compactor) corrections(ctx context.Context, items knowledge.Set) ([]Correction, error) {
	verdicts, err := c.feedback.List(ctx, feedback.Filter{})
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, k := range items {
		for _, id := range k.Evidence.TraceIDs() {
			if !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
	}
	out := []Correction{}
	for _, id := range ids {
		if j := slices.IndexFunc(verdicts, feedback.Filter{TraceID: id}.Matches); j >= 0 {
			out = append(out, Correction{TraceID: id, Verdict: verdicts[j].Verdict, Reason: verdicts[j].Reason})
		}
	}
	return out, nil
}

// Every draft names labels some recorded run of its producer carries
// A draft without a producer is left for the ledger to refuse with the scope it lacks
func (c *Compactor) recorded(ctx context.Context, drafts []knowledge.Knowledge) error {
	runs, err := c.traces.List(ctx, trace.Filter{Name: trace.NameRun})
	if err != nil {
		return err
	}
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
