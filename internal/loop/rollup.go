package loop

import (
	"cmp"
	"context"
	"encoding/json"
	"maps"
	"slices"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// The counts of one closed cycle the reports read in place of its runs once a prune may remove them
// loop counts them as effect counts runs so a cycle reads alike from its rollup and from its runs
type Rollup struct {
	CycleID string    `json:"cycle_id"`
	Start   time.Time `json:"start"`
	End     time.Time `json:"end"`
	// In producer order
	Producers []ProducerRollup `json:"producers"`
	// The approved versions the runs of the cycle were eligible for in the order of id and version
	Items []ItemRollup `json:"items"`
}

// The runs of one producer in the cycle
type ProducerRollup struct {
	Producer string `json:"producer"`
	// Runs with items to receive that went through no draw
	Undrawn int `json:"undrawn"`
	// Each share and scheme the drawn runs recorded
	Draws []DrawShare `json:"draws"`
	// The applied arm first and the units of each arm in order
	Units []UnitRollup `json:"units"`
}

// A share and the scheme of the draw it was drawn under
type DrawShare struct {
	Share  float64 `json:"share"`
	Scheme int     `json:"scheme"`
}

// The runs of one draw unit in one arm
type UnitRollup struct {
	Unit string `json:"unit"`
	Arm  Arm    `json:"arm"`
	// The share of the first run of the unit
	Share     float64 `json:"share"`
	Runs      int     `json:"runs"`
	Judged    int     `json:"judged"`
	Corrected int     `json:"corrected"`
	// The judged runs by whose verdict and reason as EffectRow counts them
	SameReason     int `json:"same_reason"`
	Named          int `json:"named"`
	Inferred       int `json:"inferred"`
	AuditJudged    int `json:"audit_judged"`
	AuditCorrected int `json:"audit_corrected"`
	Confirmed      int `json:"confirmed"`
	Refuted        int `json:"refuted"`
}

// How the runs of each draw unit fared with one approved version they were eligible for
type ItemRollup struct {
	ID      string     `json:"id"`
	Version int        `json:"version"`
	Units   []ItemUnit `json:"units"`
}

// The runs of one draw unit that included a version and those the item hold kept it from
// Each with its judged and corrected runs so an item effect keeps the design effect of the units
type ItemUnit struct {
	Unit              string `json:"unit"`
	Included          int    `json:"included"`
	IncludedJudged    int    `json:"included_judged"`
	IncludedCorrected int    `json:"included_corrected"`
	Held              int    `json:"held"`
	HeldJudged        int    `json:"held_judged"`
	HeldCorrected     int    `json:"held_corrected"`
}

// One run of the unit that included the version or was held from it
func (u *ItemUnit) add(held, judged, corrected bool) {
	runs, judgedRuns, correctedRuns := &u.Included, &u.IncludedJudged, &u.IncludedCorrected
	if held {
		runs, judgedRuns, correctedRuns = &u.Held, &u.HeldJudged, &u.HeldCorrected
	}
	*runs++
	if judged {
		*judgedRuns++
	}
	if corrected {
		*correctedRuns++
	}
}

// The runs and their eligible versions per version and unit in the order of id, version and unit
// A run inside the cycle of a rollup counts nothing since the rollup counted it
func (r Runs) items(rolled Rollups) []ItemRollup {
	units := map[knowledge.Ref]map[string]*ItemUnit{}
	for id, in := range r.inputs {
		if rolled.covers(r.times[id]) {
			continue
		}
		fb, judged := r.verdicts[id]
		for _, e := range in.Eligible {
			ref := e.Ref
			held := slices.Contains(in.Held, ref)
			if !held && !slices.Contains(in.Applied, ref) {
				continue
			}
			if units[ref] == nil {
				units[ref] = map[string]*ItemUnit{}
			}
			unit := cmp.Or(in.Unit, id)
			if units[ref][unit] == nil {
				units[ref][unit] = &ItemUnit{Unit: unit}
			}
			units[ref][unit].add(held, judged, judged && fb.Corrects())
		}
	}
	out := []ItemRollup{}
	refs := slices.SortedFunc(maps.Keys(units), func(a, b knowledge.Ref) int {
		return cmp.Or(cmp.Compare(a.ID, b.ID), cmp.Compare(a.Version, b.Version))
	})
	for _, ref := range refs {
		item := ItemRollup{ID: ref.ID, Version: ref.Version, Units: []ItemUnit{}}
		for _, unit := range slices.Sorted(maps.Keys(units[ref])) {
			item.Units = append(item.Units, *units[ref][unit])
		}
		out = append(out, item)
	}
	return out
}

// The first rollup of each cycle in the order of their starts
type Rollups []Rollup

// The rollups the traces hold
// 1. the first rollup of a cycle by time counts since a later one may come from a prune that ran beside it once runs were gone
// 2. a rollup that does not decode counts nothing
func NewRollups(traces trace.Traces) Rollups {
	first := map[string]trace.Trace{}
	rollups := map[string]Rollup{}
	for _, tr := range traces {
		var r Rollup
		if tr.Name != trace.NameRollup || json.Unmarshal(tr.Input, &r) != nil || r.CycleID == "" {
			continue
		}
		if old, ok := first[r.CycleID]; ok && (old.Time.Before(tr.Time) || old.Time.Equal(tr.Time) && old.ID < tr.ID) {
			continue
		}
		first[r.CycleID], rollups[r.CycleID] = tr, r
	}
	return slices.SortedFunc(maps.Values(rollups), func(a, b Rollup) int { return a.Start.Compare(b.Start) })
}

// Whether a rollup counts the runs of the time in place of them
func (rs Rollups) covers(t time.Time) bool {
	return slices.ContainsFunc(rs, func(r Rollup) bool { return !t.Before(r.Start) && t.Before(r.End) })
}

// The rollup of the closed cycle as JSON for retain to append before it prunes a run of the cycle
// 1. it counts every run of the cycle and every verdict, outcome and knowledge record as they stand
// 2. it counts the runs as effect counts them so a report reads the same from the rollup once the runs are gone
// 3. no count reads an answer so the runs are read without their output
func (s Stores) Rollup(ctx context.Context, c trace.Cycle) (json.RawMessage, error) {
	found, err := s.Traces.List(ctx, record.TraceFilter{Name: string(trace.NameRun), Since: c.Start, WithoutOutput: true})
	if err != nil {
		return nil, err
	}
	runs := slices.DeleteFunc(trace.RestoreAll(found), func(tr trace.Trace) bool { return !c.Holds(tr.Time) })
	verdicts, err := s.Verdicts.List(ctx, record.FeedbackFilter{})
	if err != nil {
		return nil, err
	}
	outcomes, err := s.Outcomes.List(ctx, "")
	if err != nil {
		return nil, err
	}
	items, err := s.Items.All(ctx)
	if err != nil {
		return nil, err
	}
	joined := NewRuns(runs, feedback.RestoreAll(verdicts))
	// A rollup of strings, numbers and times always encodes
	return json.Marshal(Rollup{
		CycleID: c.ID, Start: c.Start, End: c.End,
		Producers: joined.tally(items, feedback.RestoreOutcomes(outcomes), nil).producers(), Items: joined.items(nil),
	})
}
