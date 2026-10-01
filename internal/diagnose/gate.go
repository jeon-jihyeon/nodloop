package diagnose

import (
	"fmt"
	"slices"
	"strings"

	"github.com/jeon-jihyeon/nodloop/internal/evidence"
)

// Paragraph ids a cause keeps at most
// The prompt rules allow two per cause
const causeCitations = 2

type citable map[string]struct{}

// Unknown and repeated ids are dropped
func (c citable) keep(ids []string) []string {
	var out []string
	for _, id := range ids {
		if _, ok := c[id]; ok && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// Ids of the review the context does not list in citation order
// The diagnose trace keeps them because the gated output no longer shows why a cause lost its citation
func (c citable) unknown(diag Diagnosis) []string {
	ids := diag.Checks.Paragraphs()
	for _, cause := range diag.Causes {
		ids = append(ids, cause.ParagraphIDs...)
	}
	return c.unlisted(ids)
}

// The ids the context does not list in citation order without repeats
func (c citable) unlisted(ids []string) []string {
	var out []string
	for _, id := range ids {
		if _, ok := c[id]; !ok && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// The review with unknown and repeated ids dropped and each cause cut to its first causeCitations ids
// Checks and causes are cloned first because their backing arrays belong to the caller
// Revisions and the gate both judge this review so what is sent back and what is recorded agree
// The cut drops the later ids silently so an edited review that overflows it is refused before it becomes an example
func (diag Diagnosis) cited(known citable) Diagnosis {
	diag.Checks = slices.Clone(diag.Checks)
	for i := range diag.Checks {
		diag.Checks[i].ParagraphIDs = known.keep(diag.Checks[i].ParagraphIDs)
	}
	diag.Causes = slices.Clone(diag.Causes)
	for i := range diag.Causes {
		ids := known.keep(diag.Causes[i].ParagraphIDs)
		diag.Causes[i].ParagraphIDs = ids[:min(len(ids), causeCitations)]
	}
	return diag
}

// Summaries of the causes whose known ids outnumber causeCitations in citation order
func (diag Diagnosis) overCited(known citable) []string {
	var out []string
	for _, c := range diag.Causes {
		if len(known.keep(c.ParagraphIDs)) > causeCitations {
			out = append(out, c.Summary)
		}
	}
	return out
}

// Forces hold when the cited review cannot stand
// 1. ready_for_review with a cause that no kept id supports becomes hold naming the cause
// 2. ready_for_review with no causes becomes hold
// 3. no_action with causes becomes hold because a no_action must not carry a cause
// 4. hold drops its causes and without hold reasons gets one that says the model gave none
// 5. every status keeps its checks so a hold still names the steps that would lift it
// 6. a hold keeps an empty causes list so the output keeps the schema's array
// Revisions send each of 1 to 3 back once so the gate holds only what a second submission left
func (diag Diagnosis) gate(firstSteps steps) (Diagnosis, bool) {
	var uncited []string
	for _, c := range diag.Causes {
		if !c.supported(firstSteps) {
			uncited = append(uncited, c.Summary)
		}
	}
	switch diag.Status {
	case evidence.StatusReadyForReview:
		if len(diag.Causes) == 0 {
			return diag.hold("no cause was given"), true
		}
		if len(uncited) > 0 {
			return diag.hold(fmt.Sprintf("no paragraph supports: %s", strings.Join(uncited, "; "))), true
		}
		return diag, false
	case evidence.StatusNoAction:
		if len(diag.Causes) > 0 {
			return diag.hold("no_action was returned together with causes"), true
		}
		return diag, false
	case evidence.StatusHold:
		diag.Causes = []Cause{}
		if len(diag.HoldReasons) == 0 {
			diag.HoldReasons = []string{"the model returned hold without a reason"}
		}
		return diag, false
	default:
		return diag.hold(fmt.Sprintf("unknown status %q", diag.Status)), true
	}
}

// Whether one cited id may state the cause
// A cause citing only a Decide paragraph or a first step is uncited
func (c Cause) supported(firstSteps steps) bool {
	return slices.ContainsFunc(c.ParagraphIDs, firstSteps.mayState)
}

// A Decide paragraph and a first step never state a cause
func (s steps) mayState(id string) bool {
	p := evidence.ParagraphID(id)
	return !p.IsDecide() && !s.has(p)
}

func (diag Diagnosis) hold(reason string) Diagnosis {
	diag.Status = evidence.StatusHold
	diag.Causes = []Cause{}
	diag.HoldReasons = append(slices.Clip(diag.HoldReasons), reason)
	return diag
}
