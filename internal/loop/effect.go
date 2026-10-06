package loop

import (
	"slices"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
)

// How the runs of one arm fared
// An arm is the runs that applied items or the runs a holdout kept them from
type EffectRow struct {
	Arm    string `json:"arm"`
	Runs   int    `json:"runs"`
	Judged int    `json:"judged"`
	// Judged runs whose verdict corrects
	Corrected int `json:"corrected"`
	// Corrections with a reason code an item of the run was taught by
	// Any correction counts for items taught without a code
	SameReason int `json:"same_reason"`
}

// The applied arm and the withheld arm in that order
// Only runs that had items to receive count so both arms come from places with approved items
func (r Runs) Effect(items knowledge.Set) []EffectRow {
	evidence := map[knowledge.Ref][]string{}
	for _, k := range items {
		evidence[knowledge.Ref{ID: k.ID, Version: k.Version}] = k.Evidence.FeedbackTraceIDs
	}
	applied, withheld := EffectRow{Arm: "applied"}, EffectRow{Arm: "withheld"}
	for id := range r.ids {
		switch {
		case len(r.received[id]) > 0:
			r.tally(&applied, id, r.received[id], evidence)
		case len(r.withheld[id]) > 0:
			r.tally(&withheld, id, r.withheld[id], evidence)
		}
	}
	return []EffectRow{applied, withheld}
}

// One run of the arm with the items it received or would have received
func (r Runs) tally(row *EffectRow, runID string, refs []knowledge.Ref, evidence map[knowledge.Ref][]string) {
	row.Runs++
	fb, ok := r.verdicts[runID]
	if !ok {
		return
	}
	row.Judged++
	if !fb.Corrects() {
		return
	}
	row.Corrected++
	var codes []feedback.ReasonCode
	for _, ref := range refs {
		taught, _ := r.taught(evidence[ref])
		codes = append(codes, taught...)
	}
	if len(codes) == 0 || slices.Contains(codes, fb.ReasonCode) {
		row.SameReason++
	}
}
