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
// 1. a Decide paragraph never states a cause
// 2. a ready_for_review cause cites no first step because a first step is a check
// 3. a ready_for_review cause keeps a listed paragraph id because the gate holds a cause without one
// 4. a ready_for_review keeps the first step of the lead procedure as a check
// Any other status loses its causes at the gate so 2 and 3 never cost it a second model call
func (diag Diagnosis) revisions(firstSteps []string) []string {
	var out []string
	ready := diag.Status == evidence.StatusReadyForReview
	for _, c := range diag.Causes {
		if i := slices.IndexFunc(c.ParagraphIDs, func(id string) bool { return evidence.ParagraphID(id).IsDecide() }); i >= 0 {
			out = append(out, fmt.Sprintf("cause %q cites the Decide paragraph %s. A Decide paragraph states no cause", c.Summary, c.ParagraphIDs[i]))
		}
		if i := slices.IndexFunc(c.ParagraphIDs, func(id string) bool { return slices.Contains(firstSteps, id) }); ready && i >= 0 {
			out = append(out, fmt.Sprintf("cause %q cites %s, the first step of its procedure. "+
				"A first step is a check and never states a cause. Cite the paragraph that states the cause", c.Summary, c.ParagraphIDs[i]))
		}
		if ready && len(c.ParagraphIDs) == 0 {
			out = append(out, fmt.Sprintf("cause %q cites no paragraph id from the list. Cite the paragraph that states it or return hold", c.Summary))
		}
	}
	lead := diag.leadProcedure()
	i := slices.IndexFunc(firstSteps, func(id string) bool { return evidence.ParagraphID(id).Procedure() == lead })
	if !ready || i < 0 || slices.Contains(diag.Checks.Paragraphs(), firstSteps[i]) {
		return out
	}
	return append(out, fmt.Sprintf("check %s is missing. The first step of the lead procedure is always a check", firstSteps[i]))
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

// The first section after the introduction of every included procedure in paragraph order
// A procedure without a step has none
// A later step is not a check by position because it may state a finding
func (c Context) firstSteps() []string {
	var procedures, out []string
	for _, id := range c.ParagraphIDs {
		p := evidence.ParagraphID(id)
		if p.IsStep() && !slices.Contains(procedures, p.Procedure()) {
			procedures = append(procedures, p.Procedure())
			out = append(out, id)
		}
	}
	return out
}
