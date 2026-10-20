package loop

import (
	"cmp"
	"maps"
	"slices"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// The effectiveness of curate besides the audit: what the person reversed and what corrections repeated
type Overturns struct {
	// Answers of the person to pushed direction questions and those that reversed what curate prepared
	Direction           int `json:"direction"`
	DirectionOverturned int `json:"direction_overturned"`
	// Answers the person asked for or gave by command and those that reversed what curate carried out
	// No estimate reads them since the person chose what to look at
	Manual           int `json:"manual"`
	ManualOverturned int `json:"manual_overturned"`
	// Interventions of the person that undid a version nodloop approved and that paused curate
	Undo  int `json:"undo"`
	Pause int `json:"pause"`
	// The repeat corrections of report loop on the approved run items of nodloop and of a person
	Repeats []RepeatRow `json:"repeats"`
}

// The repeat corrections on the approved run items of one approver as report loop counts them
type RepeatRow struct {
	// nodloop for the items curate approved and person for every other
	Approver string `json:"approver"`
	Items    int    `json:"items"`
	// Judged runs that applied them and those corrected again for the reason that taught the item
	Judged         int `json:"judged"`
	Repeat         int `json:"repeat"`
	NamedJudged    int `json:"named_judged"`
	NamedRepeat    int `json:"named_repeat"`
	InferredJudged int `json:"inferred_judged"`
	InferredRepeat int `json:"inferred_repeat"`
}

// The approver of a repeat row that is no name of curate
const approverPerson = "person"

// The overturns of the person
// carried names the versions nodloop approved so an undo of a version a person approved is no overturn of curate
func (e events) overturns(asked units, carried map[knowledge.Ref]bool, items knowledge.Set, runs Runs) Overturns {
	out := Overturns{}
	for _, u := range asked {
		if u.question == questionDirection && u.chose {
			out.Direction++
			if u.differ {
				out.DirectionOverturned++
			}
		}
	}
	for _, a := range e.answers {
		if a.Source.SelfSelected() && a.chose() {
			out.Manual++
			if a.in.Overturn {
				out.ManualOverturned++
			}
		}
	}
	for _, in := range e.interventions {
		switch {
		case in.in.Authority != trace.AuthorityPerson:
		case in.in.Action == string(trace.ActionJudge) && in.in.Choice == string(trace.ChoiceUndo) && carried[knowledge.Ref{ID: in.in.ID, Version: in.in.Version}]:
			out.Undo++
		case in.in.Action == actionPause:
			out.Pause++
		}
	}
	out.Repeats = runs.repeatRows(items)
	return out
}

// One row for the items nodloop approved and one for the items of a person
func (r Runs) repeatRows(items knowledge.Set) []RepeatRow {
	approvers := map[knowledge.Ref]string{}
	for _, k := range items.Current() {
		approvers[knowledge.Ref{ID: k.ID, Version: k.Version}] = k.Approver
	}
	rows := []RepeatRow{{Approver: knowledge.ApproverNodloop}, {Approver: approverPerson}}
	for _, item := range r.Report(items) {
		row := &rows[1]
		if approvers[knowledge.Ref{ID: item.ID, Version: item.Version}] == knowledge.ApproverNodloop {
			row = &rows[0]
		}
		row.Items++
		row.Judged, row.Repeat = row.Judged+item.Judged, row.Repeat+item.Repeat
		row.NamedJudged, row.NamedRepeat = row.NamedJudged+item.NamedJudged, row.NamedRepeat+item.NamedRepeat
		row.InferredJudged, row.InferredRepeat = row.InferredJudged+item.InferredJudged, row.InferredRepeat+item.InferredRepeat
	}
	return rows
}

// How the runs that included one version nodloop approved fared against the runs the item hold kept it from
// The hold is the only draw per version so this is the one effect of a single item and not an association
type ItemEffect struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
	// Draw units with a run of either kind
	Units             int `json:"units"`
	Included          int `json:"included"`
	IncludedJudged    int `json:"included_judged"`
	IncludedCorrected int `json:"included_corrected"`
	Held              int `json:"held"`
	HeldJudged        int `json:"held_judged"`
	HeldCorrected     int `json:"held_corrected"`
	// Corrected of judged with the 95 percent Wilson interval over the counts shrunk by the design effect of the units
	IncludedRate *float64 `json:"included_rate"`
	IncludedLow  *float64 `json:"included_low"`
	IncludedHigh *float64 `json:"included_high"`
	HeldRate     *float64 `json:"held_rate"`
	HeldLow      *float64 `json:"held_low"`
	HeldHigh     *float64 `json:"held_high"`
	// The included rate minus the held rate with its Newcombe interval and nil when either has no judged run
	Difference     *float64 `json:"difference"`
	DifferenceLow  *float64 `json:"difference_low"`
	DifferenceHigh *float64 `json:"difference_high"`
}

// One row per version nodloop approved in the order of id and version
// A closed cycle with a rollup counts from the rollup alone and its runs count nothing
func (r Runs) itemEffects(carried map[knowledge.Ref]bool, rolled Rollups) []ItemEffect {
	units := map[knowledge.Ref]map[string]*ItemUnit{}
	add := func(rows []ItemRollup) {
		for _, item := range rows {
			ref := knowledge.Ref{ID: item.ID, Version: item.Version}
			if !carried[ref] {
				continue
			}
			if units[ref] == nil {
				units[ref] = map[string]*ItemUnit{}
			}
			for _, u := range item.Units {
				if units[ref][u.Unit] == nil {
					units[ref][u.Unit] = &ItemUnit{Unit: u.Unit}
				}
				units[ref][u.Unit].merge(u)
			}
		}
	}
	add(r.items(rolled))
	for _, rollup := range rolled {
		add(rollup.Items)
	}
	refs := slices.SortedFunc(maps.Keys(units), func(a, b knowledge.Ref) int {
		return cmp.Or(cmp.Compare(a.ID, b.ID), cmp.Compare(a.Version, b.Version))
	})
	out := []ItemEffect{}
	for _, ref := range refs {
		out = append(out, newItemEffect(ref, units[ref]))
	}
	return out
}

func newItemEffect(ref knowledge.Ref, units map[string]*ItemUnit) ItemEffect {
	row := ItemEffect{ID: ref.ID, Version: ref.Version, Units: len(units)}
	for _, u := range units {
		row.Included, row.IncludedJudged, row.IncludedCorrected = row.Included+u.Included, row.IncludedJudged+u.IncludedJudged, row.IncludedCorrected+u.IncludedCorrected
		row.Held, row.HeldJudged, row.HeldCorrected = row.Held+u.Held, row.HeldJudged+u.HeldJudged, row.HeldCorrected+u.HeldCorrected
	}
	row.IncludedRate, row.IncludedLow, row.IncludedHigh = clustered(units, (*ItemUnit).included).wilson()
	row.HeldRate, row.HeldLow, row.HeldHigh = clustered(units, (*ItemUnit).held).wilson()
	if row.IncludedRate != nil && row.HeldRate != nil {
		row.Difference, row.DifferenceLow, row.DifferenceHigh = newcombe(
			interval{row.IncludedRate, row.IncludedLow, row.IncludedHigh}, interval{row.HeldRate, row.HeldLow, row.HeldHigh})
	}
	return row
}

// The counts of another part of the same unit added to it
func (u *ItemUnit) merge(other ItemUnit) {
	u.Included += other.Included
	u.IncludedJudged += other.IncludedJudged
	u.IncludedCorrected += other.IncludedCorrected
	u.Held += other.Held
	u.HeldJudged += other.HeldJudged
	u.HeldCorrected += other.HeldCorrected
}

// The corrected runs of the unit that included the version out of its judged ones
func (u *ItemUnit) included() (int, int) {
	return u.IncludedCorrected, u.IncludedJudged
}

// The corrected runs of the unit the hold kept the version from out of its judged ones
func (u *ItemUnit) held() (int, int) {
	return u.HeldCorrected, u.HeldJudged
}
