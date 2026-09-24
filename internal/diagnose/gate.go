package diagnose

import (
	"fmt"
	"slices"
	"strings"
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

// The review with unknown and repeated ids dropped and each cause cut to its first causeCitations ids
// Checks and causes are cloned first because their backing arrays belong to the caller
// Revisions and the gate both judge this review so what is sent back and what is recorded agree
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

// Forces hold when the cited review cannot stand
// 1. ready_for_review with a cause that has no id left becomes hold naming the cause
// 2. ready_for_review with no causes becomes hold
// 3. no_action with causes becomes hold because a no_action must not carry a cause
// 4. hold drops its causes and without hold reasons gets one that says the model gave none
// 5. every status keeps its checks so a hold still names the steps that would lift it
// 6. a hold keeps an empty causes list so the output keeps the schema's array
func (diag Diagnosis) gate() (Diagnosis, bool) {
	var uncited []string
	for _, c := range diag.Causes {
		if len(c.ParagraphIDs) == 0 {
			uncited = append(uncited, c.Summary)
		}
	}
	switch diag.Status {
	case StatusReadyForReview:
		if len(diag.Causes) == 0 {
			return diag.hold("no cause was given"), true
		}
		if len(uncited) > 0 {
			return diag.hold(fmt.Sprintf("no paragraph supports: %s", strings.Join(uncited, "; "))), true
		}
		return diag, false
	case StatusNoAction:
		if len(diag.Causes) > 0 {
			return diag.hold("no_action was returned together with causes"), true
		}
		return diag, false
	case StatusHold:
		diag.Causes = []Cause{}
		if len(diag.HoldReasons) == 0 {
			diag.HoldReasons = []string{"the model returned hold without a reason"}
		}
		return diag, false
	default:
		return diag.hold(fmt.Sprintf("unknown status %q", diag.Status)), true
	}
}

func (diag Diagnosis) hold(reason string) Diagnosis {
	diag.Status = StatusHold
	diag.Causes = []Cause{}
	diag.HoldReasons = append(slices.Clip(diag.HoldReasons), reason)
	return diag
}
