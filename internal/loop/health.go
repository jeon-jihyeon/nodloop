package loop

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/evidence"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
)

// How the conversation reviews that applied one knowledge version held up
// An association and not the effect of the item since a review applies several items and its cause may lie elsewhere
type Health struct {
	ID      string           `json:"id"`
	Version int              `json:"version"`
	Status  knowledge.Status `json:"status"`
	// Reviews whose model received the version
	Applied  int `json:"applied"`
	Approved int `json:"approved"`
	Edited   int `json:"edited"`
	Rejected int `json:"rejected"`
	// Outcomes of those reviews
	Confirmed    int `json:"confirmed"`
	Refuted      int `json:"refuted"`
	Inconclusive int `json:"inconclusive"`
	// Outcomes the version takes over from reviews of the versions a compaction merged into it
	// Only reviews of a change context and a moved metric it still reaches that no narrowing answered
	CarriedConfirmed int `json:"carried_confirmed,omitempty"`
	CarriedRefuted   int `json:"carried_refuted,omitempty"`
	// An approved version with a refuted outcome and no more confirmed than refuted
	// Carried outcomes count with its own
	// A flag for a person and never a retire
	RetireCandidate bool      `json:"retire_candidate"`
	LastReviewed    time.Time `json:"last_reviewed,omitzero"`
	Stale           bool      `json:"stale"`
}

// One row per recorded id and version sorted by id then version
func (h *History) Health(now time.Time) []Health {
	rows := map[knowledge.Ref]*Health{}
	for _, k := range h.knowledge.Versions() {
		rows[knowledge.Ref{ID: k.ID, Version: k.Version}] = &Health{
			ID: k.ID, Version: k.Version, Status: k.Status, LastReviewed: k.LastReviewed(), Stale: k.Stale(now),
		}
	}
	for _, r := range h.reviews {
		for _, ref := range r.applied() {
			if row := rows[ref]; row != nil {
				row.add(h.verdicts[r.trace.ID].Verdict, h.outcomes[r.trace.ID].Result)
			}
		}
	}
	out := make([]Health, 0, len(rows))
	for ref, row := range rows {
		for _, r := range h.inherited(ref) {
			row.carry(h.outcomes[r.trace.ID].Result)
		}
		refuted, confirmed := row.Refuted+row.CarriedRefuted, row.Confirmed+row.CarriedConfirmed
		row.RetireCandidate = row.Status == knowledge.StatusApproved && refuted > 0 && refuted >= confirmed
		out = append(out, *row)
	}
	slices.SortFunc(out, Health.compare)
	return out
}

// Orders rows by id then version
func (row Health) compare(other Health) int {
	return cmp.Or(cmp.Compare(row.ID, other.ID), cmp.Compare(row.Version, other.Version))
}

// An empty verdict or result counts only as applied
func (row *Health) add(verdict feedback.Verdict, result feedback.Result) {
	row.Applied++
	switch verdict {
	case feedback.VerdictApprove:
		row.Approved++
	case feedback.VerdictEdit:
		row.Edited++
	case feedback.VerdictReject:
		row.Rejected++
	}
	switch result {
	case feedback.ResultConfirmed:
		row.Confirmed++
	case feedback.ResultRefuted:
		row.Refuted++
	case feedback.ResultInconclusive:
		row.Inconclusive++
	}
}

// Only a confirmed or refuted outcome is carried
func (row *Health) carry(result feedback.Result) {
	switch result {
	case feedback.ResultConfirmed:
		row.CarriedConfirmed++
	case feedback.ResultRefuted:
		row.CarriedRefuted++
	}
}

// A reference of a current item that no longer resolves
type Issue struct {
	ID        string `json:"id"`
	Version   int    `json:"version"`
	Field     string `json:"field"`
	Reference string `json:"reference"`
}

// Broken references of the current items in id order
// 1. evidence trace ids must name a diagnose trace with feedback or with an outcome in any mode since seed knowledge cites batch reviews
// 2. paragraph ids must be paragraphs of the procedures
// 3. knowledge refs must name a recorded version
// 4. scope change contexts and exceptions must be valid and scope metrics and dim values must be observed in some event
// 5. exceptions must leave some change context of the scope or the item never applies
func (h *History) BrokenReferences(
	procedures evidence.Procedures, metrics []string, dims knowledge.Dims, contexts evidence.Contexts,
) []Issue {
	refs := references{feedback: h.withFeedback, outcomes: h.withOutcome, paragraphs: map[string]bool{},
		versions: map[knowledge.Ref]bool{}, metrics: metrics, dims: dims, contexts: contexts}
	for _, p := range procedures.Paragraphs() {
		refs.paragraphs[string(p.ID)] = true
	}
	for _, k := range h.knowledge {
		refs.versions[knowledge.Ref{ID: k.ID, Version: k.Version}] = true
	}
	out := []Issue{}
	for _, k := range h.knowledge.Current() {
		out = append(out, refs.issues(k)...)
	}
	return out
}

// What the references of an item may resolve to
type references struct {
	feedback, outcomes, paragraphs map[string]bool
	versions                       map[knowledge.Ref]bool
	metrics                        []string
	dims                           knowledge.Dims
	// A scope context the policy no longer declares reaches no event
	contexts evidence.Contexts
}

// One issue per broken reference of the item in field order
func (refs references) issues(k knowledge.Knowledge) []Issue {
	found := issues{id: k.ID, version: k.Version}
	for _, id := range k.Evidence.FeedbackTraceIDs {
		found.check("feedback_trace_ids", id, refs.feedback[id])
	}
	for _, id := range k.Evidence.OutcomeTraceIDs {
		found.check("outcome_trace_ids", id, refs.outcomes[id])
	}
	for _, id := range k.Evidence.ParagraphIDs {
		found.check("paragraph_ids", id, refs.paragraphs[id])
	}
	for _, ref := range k.Evidence.Knowledge {
		found.check("knowledge", fmt.Sprintf("%s v%d", ref.ID, ref.Version), refs.versions[ref])
	}
	for _, c := range k.Scope.ChangeContexts {
		found.check("change_contexts", string(c), refs.contexts.Valid(c))
	}
	for _, c := range k.Exceptions {
		found.check("exceptions", string(c), refs.contexts.Valid(c))
	}
	found.check("exceptions", fmt.Sprint(k.Exceptions), !k.Excluded(refs.contexts))
	for _, m := range k.Scope.Metrics {
		found.check("metrics", m, slices.Contains(refs.metrics, m))
	}
	for _, key := range slices.Sorted(maps.Keys(k.Scope.Dims)) {
		found.check("dims", key+"="+k.Scope.Dims[key], refs.dims.Has(key, k.Scope.Dims[key]))
	}
	return found.list
}

// The issues of one item without repeats
type issues struct {
	id      string
	version int
	list    []Issue
}

func (is *issues) check(field, ref string, resolved bool) {
	issue := Issue{ID: is.id, Version: is.version, Field: field, Reference: ref}
	if !resolved && !slices.Contains(is.list, issue) {
		is.list = append(is.list, issue)
	}
}

// The declared change contexts of the conversation reviews that applied the version or passed their outcome to it and were refuted
// Sorted once each so a narrowing proposal reads the same whatever the record order
func (h *History) RefutedContexts(id string, version int, contexts evidence.Contexts) []evidence.Context {
	var out []evidence.Context
	for _, r := range h.refuted(knowledge.Ref{ID: id, Version: version}) {
		if contexts.Valid(r.ChangeContext) && !slices.Contains(out, r.ChangeContext) {
			out = append(out, r.ChangeContext)
		}
	}
	slices.Sort(out)
	return out
}

// The trace ids of the conversation reviews that applied the version or passed their outcome to it and were refuted
// Sorted so a narrowing proposal reads the same whatever the record order
func (h *History) RefutedTraces(id string, version int) []string {
	var out []string
	for _, r := range h.refuted(knowledge.Ref{ID: id, Version: version}) {
		out = append(out, r.trace.ID)
	}
	slices.Sort(out)
	return out
}

// The conversation reviews that applied the version or passed their outcome to it and whose latest outcome refuted them
func (h *History) refuted(ref knowledge.Ref) []review {
	var out []review
	for _, r := range h.reviews {
		if h.outcomes[r.trace.ID].Result == feedback.ResultRefuted && slices.Contains(r.applied(), ref) {
			out = append(out, r)
		}
	}
	for _, r := range h.inherited(ref) {
		if h.outcomes[r.trace.ID].Result == feedback.ResultRefuted {
			out = append(out, r)
		}
	}
	return out
}

// The conversation reviews with an outcome that pass it to the version
// A review that applied several merged versions counts once
func (h *History) inherited(ref knowledge.Ref) []review {
	var out []review
	for _, r := range h.reviews {
		if _, ok := h.outcomes[r.trace.ID]; ok && h.knowledge.Inherits(ref, r.trace.ID, r.applied(), r.ChangeContext, r.Moved) {
			out = append(out, r)
		}
	}
	return out
}
