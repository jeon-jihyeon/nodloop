package loop

import (
	"maps"
	"slices"
)

// The judge of one cycle against the last cycle before it with the same judge configuration
type ShiftRow struct {
	Cycle string `json:"cycle"`
	// The last earlier cycle with the same configuration and decisions of the judge
	// Empty when there is none or the configuration changed
	Against string `json:"against"`
	// Decisions the judge ran on in each cycle
	Judged        int `json:"judged"`
	AgainstJudged int `json:"against_judged"`
	// The share of each outcome and of each bin among them
	Outcomes        map[string]float64 `json:"outcomes"`
	Bins            map[string]float64 `json:"bins"`
	AgainstOutcomes map[string]float64 `json:"against_outcomes"`
	AgainstBins     map[string]float64 `json:"against_bins"`
	// The chi squared p of the outcomes with their bins between the two cycles and nil without a cycle to hold against
	P *float64 `json:"p"`
	// Whether the judge configuration differs from the one of the cycle before
	Changed bool `json:"changed"`
	// A change of configuration or a p below ShiftThreshold
	Shift bool `json:"shift"`
	// The bins whose own share moved at a p below ShiftThreshold
	// A shift of the same configuration starts their posteriors again and the bound stays
	Touched []string `json:"touched"`
}

// What the judge found on the decides of one cycle
type judgeTally struct {
	// By outcome and bin
	cells categories
	// By outcome alone and by bin alone
	outcomes, bins categories
}

func (j *judgeTally) add(d decideRecord) {
	j.cells[d.out.Outcome+" "+d.out.Bin]++
	j.outcomes[d.out.Outcome]++
	if d.out.Bin != "" {
		j.bins[d.out.Bin]++
	}
}

// The share of each category of the counts among the decisions of the cycle
func (j judgeTally) shares(c categories) map[string]float64 {
	out := map[string]float64{}
	n := j.outcomes.total()
	for k, v := range c {
		out[k] = float64(v) / float64(n)
	}
	return out
}

// One row per cycle after the first whose judge ran or whose configuration changed in time order
func (e events) shifts() []ShiftRow {
	cycles := map[string]*judgeTally{}
	for _, d := range e.decides {
		if d.out.RuleCode == ruleDelegated || d.out.Outcome == "" {
			continue
		}
		c := d.cycle()
		if cycles[c] == nil {
			cycles[c] = &judgeTally{cells: categories{}, outcomes: categories{}, bins: categories{}}
		}
		cycles[c].add(d)
	}
	out := []ShiftRow{}
	for i := 1; i < len(e.cycles); i++ {
		cur, _ := e.knobFor(e.cycles[i])
		prev, _ := e.knobFor(e.cycles[i-1])
		now, ran := cycles[e.cycles[i].ID]
		changed := !cur.in.Judge.same(prev.in.Judge)
		if !ran && !changed {
			continue
		}
		row := ShiftRow{
			Cycle: e.cycles[i].ID, Changed: changed, Shift: changed, Touched: []string{},
			Outcomes: map[string]float64{}, Bins: map[string]float64{}, AgainstOutcomes: map[string]float64{}, AgainstBins: map[string]float64{},
		}
		if ran {
			row.Judged, row.Outcomes, row.Bins = now.outcomes.total(), now.shares(now.outcomes), now.shares(now.bins)
		}
		for j := i - 1; ran && !changed && j >= 0; j-- {
			k, _ := e.knobFor(e.cycles[j])
			if before, ok := cycles[e.cycles[j].ID]; ok && k.in.Judge.same(cur.in.Judge) {
				row.against(e.cycles[j].ID, *now, *before)
				break
			}
		}
		out = append(out, row)
	}
	return out
}

// The row held against the cycle before with the same configuration
func (row *ShiftRow) against(cycle string, now, before judgeTally) {
	row.Against, row.AgainstJudged = cycle, before.outcomes.total()
	row.AgainstOutcomes, row.AgainstBins = before.shares(before.outcomes), before.shares(before.bins)
	row.P = ptr(now.cells.homogeneity(before.cells))
	row.Shift = *row.P < ShiftThreshold
	n, m := float64(now.outcomes.total()), float64(before.outcomes.total())
	bins := slices.Sorted(maps.Keys(now.bins))
	for b := range before.bins {
		if !slices.Contains(bins, b) {
			bins = append(bins, b)
		}
	}
	slices.Sort(bins)
	for _, b := range bins {
		p, ok := effective{x: float64(now.bins[b]), n: n}.differs(effective{x: float64(before.bins[b]), n: m})
		if ok && p < ShiftThreshold {
			row.Touched = append(row.Touched, b)
		}
	}
}
