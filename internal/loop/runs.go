package loop

import (
	"encoding/json"
	"slices"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// How one approved run item fared on the runs that applied it
type RunItem struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
	// Runs whose input names the version
	Applied int `json:"applied"`
	// Applied runs with a verdict of a person
	Judged int `json:"judged"`
	// Judged runs whose latest verdict is approve
	Followed int `json:"followed"`
	// Applied runs corrected again for the reason that taught the item
	Repeat int `json:"repeat"`
	// From the first correction the item cites to its approval
	// Zero when it cites none
	Settle time.Duration `json:"settle"`
}

// Runs and the verdicts of people on them
type Runs struct {
	applied map[knowledge.Ref][]string
	// Latest verdict of a person per trace
	verdicts map[string]feedback.Feedback
	// First correction of a person per trace
	firstCorrection map[string]time.Time
}

// Failed runs and runs whose input does not read are left out because they applied nothing a person could judge
func NewRuns(traces trace.Traces, verdicts feedback.Records) Runs {
	r := Runs{applied: map[knowledge.Ref][]string{}, verdicts: map[string]feedback.Feedback{}, firstCorrection: map[string]time.Time{}}
	for _, tr := range traces {
		if tr.Name != trace.NameRun || tr.Error != "" {
			continue
		}
		var in struct {
			Applied []knowledge.Ref `json:"applied"`
		}
		if json.Unmarshal(tr.Input, &in) != nil {
			continue
		}
		for _, ref := range in.Applied {
			r.applied[ref] = append(r.applied[ref], tr.ID)
		}
	}
	human := verdicts.Human()
	for _, fb := range human.Latest() {
		r.verdicts[fb.TraceID] = fb
	}
	for _, fb := range human {
		if first, ok := r.firstCorrection[fb.TraceID]; fb.Corrects() && (!ok || fb.Time.Before(first)) {
			r.firstCorrection[fb.TraceID] = fb.Time
		}
	}
	return r
}

// One row per approved run item in the order of the set
// 1. repeat counts a correction with a reason code the corrections the item cites gave, or any correction when they gave none
// 2. settle runs from the earliest correction among the traces the item cites
func (r Runs) Report(items knowledge.Set) []RunItem {
	out := []RunItem{}
	for _, k := range items.Current() {
		if k.Status != knowledge.StatusApproved || k.Run == nil {
			continue
		}
		row := RunItem{ID: k.ID, Version: k.Version}
		codes, taught := r.taught(k.Evidence.FeedbackTraceIDs)
		for _, id := range r.applied[knowledge.Ref{ID: k.ID, Version: k.Version}] {
			row.Applied++
			fb, ok := r.verdicts[id]
			if !ok {
				continue
			}
			row.Judged++
			if fb.Verdict == feedback.VerdictApprove {
				row.Followed++
			}
			if fb.Corrects() && (len(codes) == 0 || slices.Contains(codes, fb.ReasonCode)) {
				row.Repeat++
			}
		}
		if !taught.IsZero() {
			row.Settle = k.ApprovedAt.Sub(taught)
		}
		out = append(out, row)
	}
	return out
}

// The reason codes the corrections of the traces gave and the time of the earliest one
func (r Runs) taught(traceIDs []string) ([]feedback.ReasonCode, time.Time) {
	var codes []feedback.ReasonCode
	var first time.Time
	for _, id := range traceIDs {
		if fb, ok := r.verdicts[id]; ok && fb.Corrects() && fb.ReasonCode != "" && !slices.Contains(codes, fb.ReasonCode) {
			codes = append(codes, fb.ReasonCode)
		}
		if at, ok := r.firstCorrection[id]; ok && (first.IsZero() || at.Before(first)) {
			first = at
		}
	}
	return codes, first
}
