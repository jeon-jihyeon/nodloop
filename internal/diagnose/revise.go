package diagnose

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/jeon-jihyeon/nodloop/internal/evidence"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// Stored as the revise trace Input
// The Output is the review that was sent back
// Strings only so encoding never fails
type reviseInput struct {
	Reasons []string `json:"reasons"`
}

// Writes a revise trace and returns the reasons when there are any and no revise trace exists yet
// The second review of a context is never sent back so a model that cannot fix the defect still gets recorded
func (d *Diagnoser) revise(ctx context.Context, c Context, diag Diagnosis, reasons []string, run modelRun) (Result, error) {
	if len(reasons) == 0 {
		return Result{}, nil
	}
	earlier, err := d.traces.List(ctx, trace.Filter{Name: trace.NameRevise, Ref: c.PendingID, Limit: 1})
	if err != nil || len(earlier) > 0 {
		return Result{}, err
	}
	tr := c.runTrace(trace.NameRevise, d.now(), run)
	tr.Input, _ = json.Marshal(reviseInput{Reasons: reasons})
	tr.Output, _ = json.Marshal(diag)
	if err := d.traces.Append(ctx, tr); err != nil {
		return Result{}, err
	}
	return Result{TraceID: tr.ID, Revisions: reasons}, nil
}

// The prompt of the second model call: the first review and what to fix
func revisePrompt(prompt string, diag Diagnosis, reasons []string) string {
	previous, _ := json.Marshal(diag)
	var b strings.Builder
	b.WriteString(prompt)
	b.WriteString("\n\n## Your previous review\n\n")
	b.Write(previous)
	b.WriteString("\n\n## Revise\n\nReturn the review again with only these defects fixed:\n")
	for _, r := range reasons {
		b.WriteString("- ")
		b.WriteString(r)
		b.WriteString("\n")
	}
	return b.String()
}

// Reasons a review must be revised before it is recorded
// Each reason names one defect the model can fix without new information
// The review is sent back once so every reason judges the review its shape fix leaves
// A no_action with causes keeps them only as a ready_for_review so its causes are judged as ready
// 1. the status and the causes agree as the shape reasons say
// 2. a Decide paragraph never states a cause
// 3. a ready cause cites no first step because a first step is a check
// 4. a ready cause that no listed id may state names the ids the list does not hold because the cut drops them
// 5. a ready cause keeps a listed paragraph id because the gate holds a cause without one
// 6. a ready review keeps the first step of the lead procedure as a check
// A hold loses its causes at the gate so 3 to 5 never cost it a second model call
func (diag Diagnosis) revisions(known citable, firstSteps steps) []string {
	out := diag.shapeDefects()
	ready := diag.Status == evidence.StatusReadyForReview || diag.Status == evidence.StatusNoAction && len(diag.Causes) > 0
	cited := diag.cited(known)
	for i, c := range cited.Causes {
		if id, ok := c.cites(evidence.ParagraphID.IsDecide); ok {
			out = append(out, fmt.Sprintf("cause %q cites the Decide paragraph %s. A Decide paragraph states no cause", c.Summary, id))
		}
		if id, ok := c.cites(firstSteps.has); ready && ok {
			out = append(out, fmt.Sprintf("cause %q cites %s, the first step of its procedure. "+
				"A first step is a check and never states a cause. Cite the paragraph that states the cause", c.Summary, id))
		}
		if !ready || c.supported(firstSteps) {
			continue
		}
		if unlisted := known.unlisted(diag.Causes[i].ParagraphIDs); len(unlisted) > 0 {
			out = append(out, fmt.Sprintf("cause %q cites %s, which the list does not hold. "+
				"Cite the listed paragraph id that states it or return hold", c.Summary, strings.Join(unlisted, ", ")))
			continue
		}
		if len(c.ParagraphIDs) == 0 {
			out = append(out, fmt.Sprintf("cause %q cites no paragraph id from the list. Cite the paragraph that states it or return hold", c.Summary))
		}
	}
	step, ok := firstSteps.of(cited.leadProcedure())
	if !ready || !ok || slices.Contains(cited.Checks.Paragraphs(), step) {
		return out
	}
	return append(out, fmt.Sprintf("check %s is missing. The first step of the lead procedure is always a check", step))
}

// Reasons the status and the causes contradict each other
// The gate would hold each of them although the model can fix it
// 1. a status outside the valid set
// 2. a ready_for_review without a cause
// 3. a no_action with causes
func (diag Diagnosis) shapeDefects() []string {
	switch {
	case !diag.Status.Valid():
		return []string{fmt.Sprintf("status %q is not one of %s, %s or %s",
			diag.Status, evidence.StatusNoAction, evidence.StatusReadyForReview, evidence.StatusHold)}
	case diag.Status == evidence.StatusReadyForReview && len(diag.Causes) == 0:
		return []string{"ready_for_review gives no cause. Give the cause with the paragraph that states it " +
			"and keep the first step of its procedure as a check, or return hold"}
	case diag.Status == evidence.StatusNoAction && len(diag.Causes) > 0:
		return []string{"no_action carries causes. Return ready_for_review with the causes or no_action without them"}
	}
	return nil
}

// The first paragraph id the cause cites that the predicate holds for
func (c Cause) cites(holds func(evidence.ParagraphID) bool) (string, bool) {
	for _, id := range c.ParagraphIDs {
		if holds(evidence.ParagraphID(id)) {
			return id, true
		}
	}
	return "", false
}

// The one procedure every cause cites
// Empty when the causes cite none or more than one
func (diag Diagnosis) leadProcedure() string {
	var procedures []string
	for _, c := range diag.Causes {
		for _, id := range c.ParagraphIDs {
			if r := evidence.ParagraphID(id).Procedure(); !slices.Contains(procedures, r) {
				procedures = append(procedures, r)
			}
		}
	}
	if len(procedures) != 1 {
		return ""
	}
	return procedures[0]
}

// Paragraph ids of first steps in paragraph order
type steps []string

func (s steps) has(id evidence.ParagraphID) bool {
	return slices.Contains(s, string(id))
}

// The first step of the procedure
func (s steps) of(procedure string) (string, bool) {
	for _, id := range s {
		if evidence.ParagraphID(id).Procedure() == procedure {
			return id, true
		}
	}
	return "", false
}

// Every paragraph of the first section after the introduction of every included procedure in paragraph order
// A procedure without a step has none
// A later step is not a check by position because it may state a finding
func (c Context) firstSteps() steps {
	var out steps
	for _, id := range c.ParagraphIDs {
		p := evidence.ParagraphID(id)
		if !p.IsStep() {
			continue
		}
		if first, ok := out.of(p.Procedure()); !ok || evidence.ParagraphID(first).Section() == p.Section() {
			out = append(out, id)
		}
	}
	return out
}
