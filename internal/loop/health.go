package loop

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// How the runs that applied one knowledge version held up
// An association and not the effect of the item since a run applies several items and its cause may lie elsewhere
type Health struct {
	ID      string           `json:"id"`
	Version int              `json:"version"`
	Status  knowledge.Status `json:"status"`
	// Runs that received the version
	Applied  int `json:"applied"`
	Approved int `json:"approved"`
	Edited   int `json:"edited"`
	Rejected int `json:"rejected"`
	// Outcomes of those runs
	Confirmed    int `json:"confirmed"`
	Refuted      int `json:"refuted"`
	Inconclusive int `json:"inconclusive"`
	// Outcomes the version takes over from runs of the versions a compaction merged into it
	// Only runs it still reaches that no narrowing answered
	CarriedConfirmed int `json:"carried_confirmed,omitempty"`
	CarriedRefuted   int `json:"carried_refuted,omitempty"`
	// An approved version with a refuted outcome and no more confirmed than refuted
	// Carried outcomes count with its own
	// A flag for a person and never a retire
	RetireCandidate bool `json:"retire_candidate"`
	// An approved version of basis stated with a confirmed outcome and none refuted
	// Carried outcomes count with its own
	// A flag for a person and never an approval
	PromotionCandidate bool      `json:"promotion_candidate"`
	LastReviewed       time.Time `json:"last_reviewed,omitzero"`
	Stale              bool      `json:"stale"`
}

// One row per recorded id and version sorted by id then version
func (h *History) Health(now time.Time) []Health {
	rows := map[knowledge.Ref]*Health{}
	stated := map[knowledge.Ref]bool{}
	for _, k := range h.knowledge.Versions() {
		ref := knowledge.Ref{ID: k.ID, Version: k.Version}
		rows[ref] = &Health{ID: k.ID, Version: k.Version, Status: k.Status, LastReviewed: k.LastReviewed(), Stale: k.Stale(now)}
		stated[ref] = k.Basis == knowledge.BasisStated
	}
	for _, r := range h.entries {
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
		row.PromotionCandidate = row.Status == knowledge.StatusApproved && stated[ref] && confirmed > 0 && refuted == 0
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
// 1. evidence trace ids must name a run with feedback or with an outcome
// 2. knowledge refs must name a recorded version
// 3. every label and exception of a run scope must be one a run of its producer carries
func (h *History) BrokenReferences() []Issue {
	refs := references{feedback: h.withFeedback, outcomes: h.withOutcome, versions: map[knowledge.Ref]bool{}, vocabulary: h.vocabulary}
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
	feedback, outcomes map[string]bool
	versions           map[knowledge.Ref]bool
	// Labels the runs of each producer carry
	vocabulary map[string]trace.Labels
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
	for _, ref := range k.Evidence.Knowledge {
		found.check("knowledge", fmt.Sprintf("%s v%d", ref.ID, ref.Version), refs.versions[ref])
	}
	if k.Run != nil {
		return refs.runIssues(found, *k.Run)
	}
	return found.list
}

// A label no run of the producer carries any more, so the item reaches no run
func (refs references) runIssues(found issues, run knowledge.RunScope) []Issue {
	vocab := refs.vocabulary[run.Producer]
	for _, field := range []struct {
		name   string
		labels trace.Labels
	}{{"labels", run.Labels}, {"except", run.Except}} {
		for _, key := range slices.Sorted(maps.Keys(field.labels)) {
			for _, v := range field.labels[key] {
				found.check(field.name, key+"="+v, vocab.Has(key, v))
			}
		}
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

// The values of the key that the refuted runs which applied the version carried
// Sorted once each so a narrowing proposal reads the same whatever the record order
func (h *History) RefutedValues(id string, version int, key string) []string {
	var out []string
	for _, r := range h.resulted(knowledge.Ref{ID: id, Version: version}, feedback.ResultRefuted) {
		for _, v := range r.trace.Labels[key] {
			if !slices.Contains(out, v) {
				out = append(out, v)
			}
		}
	}
	slices.Sort(out)
	return out
}

// The trace ids of the runs that applied the version or passed their outcome to it and were refuted
// Sorted so a narrowing proposal reads the same whatever the record order
func (h *History) RefutedTraces(id string, version int) []string {
	return h.traces(knowledge.Ref{ID: id, Version: version}, feedback.ResultRefuted)
}

// The trace ids of the runs that applied the version or passed their outcome to it and were confirmed
// Sorted so a promotion proposal reads the same whatever the record order
func (h *History) ConfirmedTraces(id string, version int) []string {
	return h.traces(knowledge.Ref{ID: id, Version: version}, feedback.ResultConfirmed)
}

func (h *History) traces(ref knowledge.Ref, result feedback.Result) []string {
	var out []string
	for _, r := range h.resulted(ref, result) {
		out = append(out, r.trace.ID)
	}
	slices.Sort(out)
	return out
}

// The runs that applied the version or passed their outcome to it and whose latest outcome is result
func (h *History) resulted(ref knowledge.Ref, result feedback.Result) []entry {
	var out []entry
	for _, r := range h.entries {
		if h.outcomes[r.trace.ID].Result == result && slices.Contains(r.applied(), ref) {
			out = append(out, r)
		}
	}
	for _, r := range h.inherited(ref) {
		if h.outcomes[r.trace.ID].Result == result {
			out = append(out, r)
		}
	}
	return out
}

// The runs with an outcome that pass it to the version
// A run that applied several merged versions counts once
func (h *History) inherited(ref knowledge.Ref) []entry {
	var out []entry
	for _, r := range h.entries {
		if _, ok := h.outcomes[r.trace.ID]; ok && h.knowledge.Inherits(ref, r.trace.ID, r.applied(), r.trace.Producer, r.trace.Labels) {
			out = append(out, r)
		}
	}
	return out
}
