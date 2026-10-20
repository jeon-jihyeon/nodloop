package loop

import (
	"cmp"
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
	// Who approved or last reaffirmed the version: the person by name or nodloop for what curate approved without them
	Approver string `json:"approver"`
	// Runs whose input names the version
	Applied int `json:"applied"`
	// Applied runs with a verdict of a person a proof shows
	Judged int `json:"judged"`
	// Judged runs whose latest verdict is approve
	Followed int `json:"followed"`
	// Applied runs corrected again for the reason that taught the item
	Repeat int `json:"repeat"`
	// The same three counts over the runs whose verdict names a person with no proof
	// Such as the verdict of a script or the Go package or a stdio server since a program may pass a name as well as a person
	NamedJudged   int `json:"named_judged"`
	NamedFollowed int `json:"named_followed"`
	NamedRepeat   int `json:"named_repeat"`
	// The same three counts over the runs only the conversation or the capture judged so a number always says whose verdict it is
	InferredJudged   int `json:"inferred_judged"`
	InferredFollowed int `json:"inferred_followed"`
	InferredRepeat   int `json:"inferred_repeat"`
	// From the first correction the item cites to its approval
	// Zero when it cites none
	Settle time.Duration `json:"settle_ns"`
}

// How far the runs went through the loop so the stage where it stalls reads at a glance
type Totals struct {
	Runs int `json:"runs"`
	// Runs with a verdict of a person a proof shows
	Judged int `json:"judged"`
	// Runs whose verdict names a person with no proof
	Named int `json:"named"`
	// Runs with only a verdict the conversation or the capture inferred
	Inferred int `json:"inferred"`
	// Judged named or inferred runs whose verdict corrects
	Corrected int `json:"corrected"`
	// Run candidates waiting for approval outside a compaction
	Waiting int `json:"waiting"`
	// Approved run items
	Approved int `json:"approved"`
}

// Runs and the verdicts on them
type Runs struct {
	ids map[string]bool
	// The input per run with its items and its draw and its plugin version
	inputs map[string]knowledge.RunInput
	// The producer and the time per run
	producers map[string]string
	times     map[string]time.Time
	applied   map[knowledge.Ref][]string
	// Latest verdict of a person per trace or else the one the conversation and the capture leave together
	verdicts map[string]feedback.Feedback
	// Every record the conversation inferred per trace in the order of the store
	conversation map[string]feedback.Records
	// The newest verdict per trace of the conversation and of the capture as Inferred reconciles them
	said, heard map[string]feedback.Feedback
	// First correction per trace by anyone
	firstCorrection map[string]time.Time
}

// Failed runs and runs whose input does not read are left out because they applied nothing a person could judge
func NewRuns(traces trace.Traces, verdicts feedback.Records) Runs {
	r := Runs{
		ids: map[string]bool{}, inputs: map[string]knowledge.RunInput{}, producers: map[string]string{}, times: map[string]time.Time{},
		applied:  map[knowledge.Ref][]string{},
		verdicts: map[string]feedback.Feedback{}, conversation: map[string]feedback.Records{}, firstCorrection: map[string]time.Time{},
		said: verdicts.Reader(feedback.ReviewerSession), heard: verdicts.Reader(feedback.ReviewerNodloop),
	}
	for _, tr := range traces {
		if tr.Name != trace.NameRun || tr.Error != "" {
			continue
		}
		var in knowledge.RunInput
		if json.Unmarshal(tr.Input, &in) != nil {
			continue
		}
		r.ids[tr.ID] = true
		r.inputs[tr.ID] = in
		r.producers[tr.ID], r.times[tr.ID] = tr.Producer, tr.Time
		for _, ref := range in.Applied {
			r.applied[ref] = append(r.applied[ref], tr.ID)
		}
	}
	for _, fb := range verdicts.Inferred() {
		r.verdicts[fb.TraceID] = fb
	}
	// A person's verdict wins over an inferred one whatever their order
	for _, fb := range verdicts.Human().Latest() {
		r.verdicts[fb.TraceID] = fb
	}
	for _, fb := range verdicts {
		if fb.Reviewer == feedback.ReviewerSession {
			r.conversation[fb.TraceID] = append(r.conversation[fb.TraceID], fb)
		}
		if first, ok := r.firstCorrection[fb.TraceID]; fb.Corrects() && (!ok || fb.Time.Before(first)) {
			r.firstCorrection[fb.TraceID] = fb.Time
		}
	}
	return r
}

// The runs and their verdicts and the run items of the set
func (r Runs) Totals(items knowledge.Set) Totals {
	t := Totals{Runs: len(r.ids)}
	for id, fb := range r.verdicts {
		switch {
		case !r.ids[id]:
			continue
		case fb.Implicit():
			t.Inferred++
		case fb.Proven():
			t.Judged++
		default:
			t.Named++
		}
		if fb.Corrects() {
			t.Corrected++
		}
	}
	for _, k := range items.Versions() {
		if k.Status == knowledge.StatusCandidate && k.Compaction == "" && k.Run != nil {
			t.Waiting++
		}
	}
	for _, k := range items.Approved() {
		if k.Run != nil {
			t.Approved++
		}
	}
	return t
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
		row := RunItem{ID: k.ID, Version: k.Version, Approver: k.Approver}
		codes, taught := r.taught(k.Evidence.FeedbackTraceIDs)
		for _, id := range r.applied[knowledge.Ref{ID: k.ID, Version: k.Version}] {
			row.Applied++
			if fb, ok := r.verdicts[id]; ok {
				row.count(fb, codes)
			}
		}
		if !taught.IsZero() {
			row.Settle = k.ApprovedAt.Sub(taught)
		}
		out = append(out, row)
	}
	return out
}

// One judged run in the counts of a proven person or a name or an inferred reader
// A correction repeats when it gives a code of codes, or any code when codes is empty
func (row *RunItem) count(fb feedback.Feedback, codes []feedback.ReasonCode) {
	judged, followed, repeat := &row.Judged, &row.Followed, &row.Repeat
	switch {
	case fb.Implicit():
		judged, followed, repeat = &row.InferredJudged, &row.InferredFollowed, &row.InferredRepeat
	case !fb.Proven():
		judged, followed, repeat = &row.NamedJudged, &row.NamedFollowed, &row.NamedRepeat
	}
	*judged++
	if fb.Verdict == feedback.VerdictApprove {
		*followed++
	}
	if fb.Corrects() && (len(codes) == 0 || slices.Contains(codes, fb.ReasonCode)) {
		*repeat++
	}
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

// The plugin version a run was recorded under and unknown when it names none
// Empty for a run recorded before 0.7 or outside the conversation hooks
func (r Runs) version(runID string) string {
	return cmp.Or(r.inputs[runID].Plugin, unknown)
}

// A version or path the records do not name
// Runs before 0.7 and runs recorded outside the conversation hooks carry no version
const unknown = "unknown"

// Runs that applied the approved item and were corrected again for a reason it was taught by
// Proven and named and inferred verdicts all count
func (r Runs) repeats(k knowledge.Knowledge) int {
	rows := r.Report(knowledge.Set{k})
	if len(rows) == 0 {
		return 0
	}
	return rows[0].Repeat + rows[0].NamedRepeat + rows[0].InferredRepeat
}
