// Package compact replaces the approved items of one crowded knowledge folder with fewer items that lose nothing the old ones fixed
package compact

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/jeon-jihyeon/nodloop/internal/evidence"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// Author of a compaction drafted without a named author
const defaultAuthor = "claude"

// Where an expected status came from
type Origin string

const (
	OriginLabel    Origin = "label"    // the data set label
	OriginEdit     Origin = "edit"     // the status of the corrected review
	OriginApproval Origin = "approval" // the status of the approved review
)

// The status a replay of one event must reach
type Expectation struct {
	EventID  string          `json:"event_id"`
	Expected evidence.Status `json:"expected"`
	Origin   Origin          `json:"origin"`
	// The newest evidence trace of the event
	TraceID string `json:"trace_id"`
}

// One correction behind an old item as the drafter reads it
type Correction struct {
	TraceID string           `json:"trace_id"`
	EventID string           `json:"event_id"`
	Verdict feedback.Verdict `json:"verdict"`
	Reason  string           `json:"reason"`
}

// What a draft is written from
type Folder struct {
	Anchor string
	knowledge.Compactable
	Corrections []Correction
	Replay      []Expectation
	// Evidence events without an expectation
	Unverifiable []string
	// A compaction of these items still waiting for its replay and approval
	// Empty when none
	Pending string
}

// Verdicts of the evidence traces
// Newest first
type FeedbackStore interface {
	List(ctx context.Context, f feedback.Filter) ([]feedback.Feedback, error)
}

// The evidence traces and the replay traces
// Newest first
type TraceStore interface {
	List(ctx context.Context, f trace.Filter) (trace.Traces, error)
}

// The labels that give an event its expected status first
type Source interface {
	Labels(ctx context.Context) ([]evidence.Label, error)
}

// Drafts and proposes and replays and approves a compaction through the ledger
// Nothing is kept between calls
type Compactor struct {
	src      Source
	ledger   *knowledge.Ledger
	traces   TraceStore
	feedback FeedbackStore
	replays  TraceStore
}

func New(src Source, ledger *knowledge.Ledger, traces TraceStore, verdicts FeedbackStore, replays TraceStore) *Compactor {
	return &Compactor{src: src, ledger: ledger, traces: traces, feedback: verdicts, replays: replays}
}

// The anchor and its folder items split by whether an event can replay them
// Carries the corrections and expectations behind them
// Fails with knowledge ErrNotFound or ErrParagraphOnly as the ledger would refuse the anchor
func (c *Compactor) Folder(ctx context.Context, anchor string) (Folder, error) {
	all, err := c.ledger.All(ctx)
	if err != nil {
		return Folder{}, err
	}
	compactable, err := all.Compactable(anchor)
	if err != nil {
		return Folder{}, err
	}
	f := Folder{Anchor: anchor, Compactable: compactable, Pending: all.PendingCompaction(compactable.Items)}
	reviews, err := c.evidence(ctx, f.Items)
	if err != nil {
		return Folder{}, err
	}
	f.Corrections = reviews.corrections()
	if f.Replay, f.Unverifiable, err = c.expectations(ctx, reviews); err != nil {
		return Folder{}, err
	}
	return f, nil
}

// Approval needs a replay so a folder without an expected status cannot be compacted
func (f Folder) replayable() error {
	if len(f.Replay) == 0 {
		return fmt.Errorf("%w: %s has no label and no edit or approve verdict on the events %s",
			ErrNothingToReplay, f.Anchor, strings.Join(f.Unverifiable, ", "))
	}
	return nil
}

// Whether a compaction is due and could pass
// 1. crowding counts the items one review of a change context carries and never the union the draft is written from
// 2. only here is it known which events have an expected status
func (f Folder) Due() bool {
	return f.Crowded() && f.replayable() == nil
}

// Proposes the draft as a compaction of the anchor's folder and returns it with the events its replay reviews
// 1. an item that names an excluded item fails with knowledge ErrParagraphOnly
// 2. a folder whose events all lack an expectation fails with ErrNothingToReplay because approval needs a replay
// 3. the ledger runs the code checks and refuses with their sentinels
func (c *Compactor) Propose(ctx context.Context, anchor string, d Draft, author string) (knowledge.Compaction, []Expectation, error) {
	f, err := c.Folder(ctx, anchor)
	if err != nil {
		return knowledge.Compaction{}, nil, err
	}
	return c.propose(ctx, f, d, author)
}

func (c *Compactor) propose(ctx context.Context, f Folder, d Draft, author string) (knowledge.Compaction, []Expectation, error) {
	if err := f.replayable(); err != nil {
		return knowledge.Compaction{}, nil, err
	}
	drafts := make([]knowledge.Knowledge, 0, len(d.Items))
	for _, item := range d.Items {
		k, err := item.knowledge(f.Items, f.Excluded, cmp.Or(author, defaultAuthor))
		if err != nil {
			return knowledge.Compaction{}, nil, err
		}
		drafts = append(drafts, k)
	}
	proposed, err := c.ledger.ProposeCompaction(ctx, f.Anchor, drafts)
	if err != nil {
		return knowledge.Compaction{}, nil, err
	}
	return proposed, f.Replay, nil
}

// Approves the compaction on behalf of a named person once its replay passed
// A replay that has not passed fails with the events that missed their expectation
func (c *Compactor) Approve(ctx context.Context, id, approver string) (knowledge.Compaction, error) {
	r, err := c.Result(ctx, id)
	if err != nil {
		return knowledge.Compaction{}, err
	}
	if !r.Passed() {
		return knowledge.Compaction{}, fmt.Errorf("%w: %s", knowledge.ErrReplayNotPassed, replayFailures(r.Events))
	}
	return c.ledger.ApproveCompaction(ctx, id, approver, r)
}

// Events that missed their expectation for the person who decides to replay or redraft
type replayFailures []knowledge.ReplayEvent

func (fs replayFailures) String() string {
	var parts []string
	for _, e := range fs {
		switch {
		case e.Got == "":
			parts = append(parts, fmt.Sprintf("%s expects %s and has no replay", e.EventID, e.Expected))
		case e.Got != e.Expected:
			parts = append(parts, fmt.Sprintf("%s expects %s and got %s", e.EventID, e.Expected, e.Got))
		}
	}
	if len(parts) == 0 {
		return "no replay event"
	}
	return strings.Join(parts, ", ")
}

// One evidence trace of an old item with its latest verdict
type review struct {
	trace   trace.Trace
	verdict *feedback.Feedback
}

// The evidence traces of the items in the order the items name them
type reviews []review

// Every feedback and outcome trace the items cite once with its latest verdict
// One read of each store serves every trace
// The first listed record of a trace is its newest
func (c *Compactor) evidence(ctx context.Context, items knowledge.Set) (reviews, error) {
	var ids []string
	for _, k := range items {
		for _, id := range k.Evidence.TraceIDs() {
			if !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
	}
	traces, err := c.traces.List(ctx, trace.Filter{})
	if err != nil {
		return nil, err
	}
	verdicts, err := c.feedback.List(ctx, feedback.Filter{})
	if err != nil {
		return nil, err
	}
	out := make(reviews, 0, len(ids))
	for _, id := range ids {
		i := slices.IndexFunc(traces, trace.Filter{ID: id}.Matches)
		if i < 0 {
			return nil, fmt.Errorf("trace %q: %w", id, trace.ErrNotFound)
		}
		r := review{trace: traces[i]}
		if j := slices.IndexFunc(verdicts, feedback.Filter{TraceID: id}.Matches); j >= 0 {
			r.verdict = &verdicts[j]
		}
		out = append(out, r)
	}
	return out, nil
}

func (rs reviews) corrections() []Correction {
	out := []Correction{}
	for _, r := range rs {
		if r.verdict != nil {
			out = append(out, Correction{TraceID: r.trace.ID, EventID: r.trace.Subject, Verdict: r.verdict.Verdict, Reason: r.verdict.Reason})
		}
	}
	return out
}

// Event ids in first seen order
func (rs reviews) events() []string {
	var out []string
	for _, r := range rs {
		if !slices.Contains(out, r.trace.Subject) {
			out = append(out, r.trace.Subject)
		}
	}
	return out
}

// The newest evidence trace of the event
func (rs reviews) newest(eventID string) review {
	var out review
	for _, r := range rs {
		if r.trace.Subject == eventID && (out.trace.ID == "" || r.trace.Time.After(out.trace.Time)) {
			out = r
		}
	}
	return out
}

// Labels of the source in source order
type labelSet []evidence.Label

// The status the first label of the event expects
func (ls labelSet) expected(event string) (evidence.Status, bool) {
	for _, l := range ls {
		if l.EventID == event {
			return l.Expected, true
		}
	}
	return "", false
}

// The expected status of every evidence event and the events without one
// 1. the label of the event when the data set has one
// 2. otherwise the latest verdict on the newest evidence trace: edit gives the corrected status and approve the recorded one
// 3. otherwise none because a reject or an outcome says what was wrong and not what is right
func (c *Compactor) expectations(ctx context.Context, rs reviews) ([]Expectation, []string, error) {
	all, err := c.src.Labels(ctx)
	if err != nil {
		return nil, nil, err
	}
	labels := labelSet(all)
	expected := []Expectation{}
	unverifiable := []string{}
	for _, event := range rs.events() {
		newest := rs.newest(event)
		if status, ok := labels.expected(event); ok {
			expected = append(expected, Expectation{EventID: event, Expected: status, Origin: OriginLabel, TraceID: newest.trace.ID})
			continue
		}
		if e, ok := newest.expectation(); ok {
			expected = append(expected, e)
			continue
		}
		unverifiable = append(unverifiable, event)
	}
	return expected, unverifiable, nil
}

// The status the verdict on this review asks for
// False without a verdict that names a status
func (r review) expectation() (Expectation, bool) {
	if r.verdict == nil {
		return Expectation{}, false
	}
	var origin Origin
	var review json.RawMessage
	switch r.verdict.Verdict {
	case feedback.VerdictEdit:
		origin, review = OriginEdit, r.verdict.Edited
	case feedback.VerdictApprove:
		origin, review = OriginApproval, r.trace.Output
	default:
		return Expectation{}, false
	}
	var status struct {
		Status evidence.Status `json:"status"`
	}
	if json.Unmarshal(review, &status) != nil || !status.Status.Valid() {
		return Expectation{}, false
	}
	return Expectation{EventID: r.trace.Subject, Expected: status.Status, Origin: origin, TraceID: r.trace.ID}, true
}
