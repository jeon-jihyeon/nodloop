package loop

import (
	"cmp"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/diagnose"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// Why a review sits high in the queue
type Reason string

const (
	ReasonForcedHold        Reason = "forced_hold"         // the citation gate turned the review into a hold
	ReasonRevised           Reason = "revised"             // record sent the first submission back
	ReasonPastCorrections   Reason = "past_corrections"    // earlier verdicts in the same change context corrected
	ReasonNewKnowledge      Reason = "new_knowledge"       // the first review that applied a knowledge version
	ReasonStatedOnly        Reason = "stated_only"         // every applied item is stated and none verified
	ReasonNoApprovedContext Reason = "no_approved_context" // no approved item covers the change context
	ReasonNoKnowledge       Reason = "no_knowledge"        // nothing was applied
	ReasonNoCitations       Reason = "no_citations"        // the causes and checks cite no paragraph
	ReasonFewCitations      Reason = "few_citations"       // the causes and checks cite one paragraph
)

// The valid reasons with their full weight
// 1. the weights are design choices of the release
// 2. past corrections add their weight scaled by the share of corrections
var weights = map[Reason]int{
	ReasonForcedHold: 4, ReasonRevised: 3, ReasonPastCorrections: 3, ReasonNewKnowledge: 2, ReasonStatedOnly: 2,
	ReasonNoApprovedContext: 2, ReasonNoKnowledge: 1, ReasonNoCitations: 1, ReasonFewCitations: 1,
}

// One review in the order a person should check it
type QueueItem struct {
	TraceID string    `json:"trace_id"`
	EventID string    `json:"event_id"`
	Time    time.Time `json:"time"`
	Score   int       `json:"score"`
	Reasons []Reason  `json:"reasons"`
	// Drawn at random from below the priority slots
	Audit     bool `json:"audit"`
	Citations int  `json:"citations"`
	// Human verdicts in the same change context before the review and how many of them corrected
	ContextReviewed    int `json:"context_reviewed"`
	ContextCorrections int `json:"context_corrections"`
}

// Adds the full weight of the reason
func (item *QueueItem) signal(reason Reason) {
	item.signalShare(reason, 1)
}

// Adds the weight of the reason scaled by share and rounded up
// A share that leaves no weight adds no reason
func (item *QueueItem) signalShare(reason Reason, share float64) {
	weight := int(math.Ceil(float64(weights[reason]) * share))
	if weight > 0 {
		item.Score += weight
		item.Reasons = append(item.Reasons, reason)
	}
}

// Orders by score then older first then trace id
func (item QueueItem) compare(other QueueItem) int {
	return cmp.Or(cmp.Compare(other.Score, item.Score), item.Time.Compare(other.Time), cmp.Compare(item.TraceID, other.TraceID))
}

// The candidates in queue order scored against the history
// A candidate may be any diagnose trace so eval ranks its own reviews the way the queue ranks the conversation
// Fails with diagnose.ErrMalformed on a candidate that does not read
func (h *History) Rank(candidates trace.Traces) ([]QueueItem, error) {
	reviews := make([]review, 0, len(candidates))
	for _, tr := range candidates {
		rec, err := diagnose.ReadRecorded(tr)
		if err != nil {
			return nil, err
		}
		reviews = append(reviews, review{trace: tr, Recorded: rec})
	}
	return h.rank(reviews), nil
}

func (h *History) rank(candidates []review) []QueueItem {
	first := h.firstApplications(candidates)
	versions := map[knowledge.Ref]knowledge.Knowledge{}
	for _, k := range h.knowledge.Versions() {
		versions[knowledge.Ref{ID: k.ID, Version: k.Version}] = k
	}
	items := make([]QueueItem, 0, len(candidates))
	for _, r := range candidates {
		item := QueueItem{TraceID: r.trace.ID, EventID: r.trace.Subject, Time: r.trace.Time, Reasons: []Reason{}}
		if r.Forced {
			item.signal(ReasonForcedHold)
		}
		if _, sentBack := h.first[r.trace.Ref]; sentBack {
			item.signal(ReasonRevised)
		}
		h.pastCorrections(&item, r)
		if r.introduces(first) {
			item.signal(ReasonNewKnowledge)
		}
		if r.statedOnly(versions) {
			item.signal(ReasonStatedOnly)
		}
		if !h.covers(r) {
			item.signal(ReasonNoApprovedContext)
		}
		if len(r.applied()) == 0 {
			item.signal(ReasonNoKnowledge)
		}
		if !r.isRun() {
			r.citations(&item)
		}
		items = append(items, item)
	}
	slices.SortFunc(items, QueueItem.compare)
	return items
}

// Whether an approved item reaches the place of the entry
// A review asks the change context and a run the producer and its labels
func (h *History) covers(r review) bool {
	if r.isRun() {
		return len(h.knowledge.For(r.trace.Producer, r.trace.Labels)) > 0
	}
	return h.knowledge.Covers(r.ChangeContext)
}

// Only a review cites paragraphs, so a run is never ranked for citing none
func (r review) citations(item *QueueItem) {
	item.Citations = len(r.Diagnosis.Citations())
	switch item.Citations {
	case 0:
		item.signal(ReasonNoCitations)
	case 1:
		item.signal(ReasonFewCitations)
	}
}

// The share of earlier human verdicts in the place of the entry that corrected
// Weighted up to the full weight of past corrections
// Only verdicts given before the review was recorded count so a later correction never ranks an older review
func (h *History) pastCorrections(item *QueueItem, r review) {
	for _, past := range h.reviews {
		fb, ok := h.verdicts[past.trace.ID]
		if !ok || !past.samePlace(r) || !fb.Time.Before(r.trace.Time) {
			continue
		}
		item.ContextReviewed++
		if fb.Corrects() {
			item.ContextCorrections++
		}
	}
	if item.ContextReviewed > 0 {
		item.signalShare(ReasonPastCorrections, float64(item.ContextCorrections)/float64(item.ContextReviewed))
	}
}

// The trace id of the earliest review that applied each version among the conversation and the candidates
func (h *History) firstApplications(candidates []review) map[knowledge.Ref]string {
	earliest := map[knowledge.Ref]trace.Trace{}
	for _, r := range slices.Concat(h.reviews, candidates) {
		for _, ref := range r.applied() {
			old, ok := earliest[ref]
			if !ok || r.trace.Time.Before(old.Time) || (r.trace.Time.Equal(old.Time) && r.trace.ID < old.ID) {
				earliest[ref] = r.trace
			}
		}
	}
	out := make(map[knowledge.Ref]string, len(earliest))
	for ref, tr := range earliest {
		out[ref] = tr.ID
	}
	return out
}

// What a queue call takes when it leaves the limit or the audit rate out
const (
	QueueLimit     = 10
	QueueAuditRate = 0.2
)

type QueueOptions struct {
	// Zero lists every review without an audit share
	Limit int
	// Share of the limit drawn at random from below the priority slots
	// From 0 to 1
	AuditRate float64
	// The same seed draws the same audit samples
	Seed int64
}

func (o QueueOptions) validate() error {
	if o.Limit < 0 || math.IsNaN(o.AuditRate) || o.AuditRate < 0 || o.AuditRate > 1 {
		return fmt.Errorf("%w: limit %d audit rate %v", ErrQueueOptions, o.Limit, o.AuditRate)
	}
	return nil
}

// The first limit items where the last ceil of limit times the audit rate slots are drawn from the rest of the order
// A short order that fits the priority slots has no audit sample
func (o QueueOptions) sample(items []QueueItem) []QueueItem {
	if o.Limit == 0 {
		return items
	}
	priority := o.Limit - int(math.Ceil(float64(o.Limit)*o.AuditRate))
	if len(items) <= priority {
		return items
	}
	rest := items[priority:]
	rng := rand.New(rand.NewPCG(uint64(o.Seed), 0))
	rng.Shuffle(len(rest), func(i, j int) { rest[i], rest[j] = rest[j], rest[i] })
	count := min(o.Limit, len(items))
	for i := priority; i < count; i++ {
		items[i].Audit = true
	}
	return items[:count]
}

// The conversation reviews without a human verdict in queue order with an audit share
func (h *History) Queue(opts QueueOptions) ([]QueueItem, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	var pending []review
	for _, r := range h.reviews {
		if _, judged := h.verdicts[r.trace.ID]; !judged {
			pending = append(pending, r)
		}
	}
	return opts.sample(h.rank(pending)), nil
}
