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
		b.WriteString("- " + r + "\n")
	}
	return b.String()
}

// Reasons a review must be revised before it is recorded
// Each reason names one defect the model can fix without new information
// 1. a Decide paragraph never states a cause
// 2. a ready_for_review keeps firstStep as a check
func (diag Diagnosis) revisions(firstStep string) []string {
	var out []string
	for _, c := range diag.Causes {
		if i := slices.IndexFunc(c.ParagraphIDs, func(id string) bool { return evidence.ParagraphID(id).IsDecide() }); i >= 0 {
			out = append(out, fmt.Sprintf("cause %q cites the Decide paragraph %s. A Decide paragraph states no cause", c.Summary, c.ParagraphIDs[i]))
		}
	}
	if diag.Status != StatusReadyForReview || firstStep == "" || slices.Contains(diag.Checks.Paragraphs(), firstStep) {
		return out
	}
	return append(out, fmt.Sprintf("check %s is missing. The first step of the lead runbook is always a check", firstStep))
}

// The one runbook every cause cites
// Empty when the causes cite none or more than one
func (diag Diagnosis) leadRunbook() string {
	var runbooks []string
	for _, c := range diag.Causes {
		for _, id := range c.ParagraphIDs {
			if r := evidence.ParagraphID(id).Runbook(); !slices.Contains(runbooks, r) {
				runbooks = append(runbooks, r)
			}
		}
	}
	if len(runbooks) != 1 {
		return ""
	}
	return runbooks[0]
}

// The first section of the runbook after its introduction
// Empty for an empty runbook
// A later step a cause cites is not demanded because it may be a finding rather than a check
func (c Context) firstStep(runbook string) string {
	for _, id := range c.ParagraphIDs {
		if p := evidence.ParagraphID(id); p.Runbook() == runbook && p.IsStep() {
			return id
		}
	}
	return ""
}
