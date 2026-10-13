package loop

import (
	"cmp"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// Why a run sits high in the queue
type Reason string

const (
	ReasonPastCorrections Reason = "past_corrections" // earlier verdicts in the same place corrected
	ReasonNewKnowledge    Reason = "new_knowledge"    // the first run that applied a knowledge version
	ReasonStatedOnly      Reason = "stated_only"      // every applied item is stated and none verified
	ReasonNoApprovedItem  Reason = "no_approved_item" // no approved item reaches the place of the run
	ReasonNoKnowledge     Reason = "no_knowledge"     // the run had no item to receive
)

// The valid reasons with their full weight
// 1. the weights are design choices of the release
// 2. past corrections add their weight scaled by the share of corrections
var weights = map[Reason]int{
	ReasonPastCorrections: 3, ReasonNewKnowledge: 2, ReasonStatedOnly: 2, ReasonNoApprovedItem: 2, ReasonNoKnowledge: 1,
}

// One run in the order a person should check it
type QueueItem struct {
	TraceID  string       `json:"trace_id"`
	Producer string       `json:"producer"`
	Labels   trace.Labels `json:"labels,omitempty"`
	Subject  string       `json:"subject,omitempty"`
	Time     time.Time    `json:"time"`
	Score    int          `json:"score"`
	Reasons  []Reason     `json:"reasons"`
	// Drawn at random from below the priority slots
	Audit bool `json:"audit"`
	// Human verdicts in the same place before the run and how many of them corrected
	PlaceReviewed    int `json:"place_reviewed"`
	PlaceCorrections int `json:"place_corrections"`
	// The holdout arm the audit draws within and never part of the answer so a person judges blind
	arm Arm
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

// Scores every run on what it received or would have received so the order never depends on the holdout arm
func (h *History) rank(candidates []entry) []QueueItem {
	versions := map[knowledge.Ref]knowledge.Knowledge{}
	for _, k := range h.knowledge.Versions() {
		versions[knowledge.Ref{ID: k.ID, Version: k.Version}] = k
	}
	items := make([]QueueItem, 0, len(candidates))
	for _, r := range candidates {
		item := QueueItem{
			TraceID: r.trace.ID, Producer: r.trace.Producer, Labels: r.trace.Labels, Subject: r.trace.Subject,
			Time: r.trace.Time, Reasons: []Reason{}, arm: r.arm(),
		}
		h.pastCorrections(&item, r)
		if r.introduces(h.firstReceived) {
			item.signal(ReasonNewKnowledge)
		}
		if r.statedOnly(versions) {
			item.signal(ReasonStatedOnly)
		}
		if len(h.knowledge.For(r.trace.Producer, r.trace.Labels)) == 0 {
			item.signal(ReasonNoApprovedItem)
		}
		if len(r.received()) == 0 {
			item.signal(ReasonNoKnowledge)
		}
		items = append(items, item)
	}
	slices.SortFunc(items, QueueItem.compare)
	return items
}

// The share of earlier human verdicts in the place of the run that corrected
// Weighted up to the full weight of past corrections
// Only verdicts given before the run was recorded count so a later correction never ranks an older run
func (h *History) pastCorrections(item *QueueItem, r entry) {
	for _, past := range h.entries {
		fb, ok := h.verdicts[past.trace.ID]
		if !ok || !past.samePlace(r) || !fb.Time.Before(r.trace.Time) {
			continue
		}
		item.PlaceReviewed++
		if fb.Corrects() {
			item.PlaceCorrections++
		}
	}
	if item.PlaceReviewed > 0 {
		item.signalShare(ReasonPastCorrections, float64(item.PlaceCorrections)/float64(item.PlaceReviewed))
	}
}

// What a queue call takes when it leaves the limit or the audit rate out
const (
	QueueLimit     = 10
	QueueAuditRate = 0.2
)

type QueueOptions struct {
	// Zero lists every run without an audit share
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
// 1. a short order that fits the priority slots has no audit sample
// 2. the draw takes one run of each arm in turn so every arm gets audit samples whatever its share
// The runs outside both arms take turns as a third arm
func (o QueueOptions) sample(items []QueueItem) []QueueItem {
	if o.Limit == 0 {
		return items
	}
	priority := o.Limit - int(math.Ceil(float64(o.Limit)*o.AuditRate))
	if len(items) <= priority {
		return items
	}
	rng := rand.New(rand.NewPCG(uint64(o.Seed), 0))
	arms := map[Arm][]QueueItem{}
	for _, item := range items[priority:] {
		arms[item.arm] = append(arms[item.arm], item)
	}
	order := []Arm{ArmApplied, ArmWithheld, ""}
	for _, arm := range order {
		rng.Shuffle(len(arms[arm]), func(i, j int) { arms[arm][i], arms[arm][j] = arms[arm][j], arms[arm][i] })
	}
	out := slices.Clone(items[:priority])
	for len(out) < min(o.Limit, len(items)) {
		for _, arm := range order {
			if len(arms[arm]) > 0 && len(out) < o.Limit {
				item := arms[arm][0]
				item.Audit = true
				out, arms[arm] = append(out, item), arms[arm][1:]
			}
		}
	}
	return out
}

// The runs without a human verdict in queue order with an audit share
func (h *History) Queue(opts QueueOptions) ([]QueueItem, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	var pending []entry
	for _, r := range h.entries {
		if _, judged := h.verdicts[r.trace.ID]; !judged {
			pending = append(pending, r)
		}
	}
	return opts.sample(h.rank(pending)), nil
}
