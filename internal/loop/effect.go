package loop

import (
	"cmp"
	"maps"
	"math"
	"slices"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
)

// The standing thresholds of the effect report
const (
	// An arm split this unlikely under the shares the runs recorded means the arms are not the draw
	// The difference is left out since such arms compare something else
	SRMThreshold = 0.001
	// A gap in the judged rate this unlikely between the arms means people judged one arm more
	JudgedRateThreshold = 0.05
	// Judged runs each arm needs before the difference reads as more than noise
	EffectMinJudged = 30
	// The z of a two sided 95 percent interval
	z95 = 1.959963984540054
)

// The arm of a drawn run
type Arm string

const (
	ArmApplied  Arm = "applied"  // the draw gave the run its items
	ArmWithheld Arm = "withheld" // the holdout kept the items from the run
)

// Why the difference of a producer may mislead
type EffectWarning string

const (
	WarnSRM          EffectWarning = "srm"          // the arms split unlike the shares so the difference is left out
	WarnJudgedRate   EffectWarning = "judged_rate"  // people judged one arm at a different rate
	WarnInsufficient EffectWarning = "insufficient" // an arm has fewer judged runs than EffectMinJudged
	WarnMixedShares  EffectWarning = "mixed_shares" // the drawn runs carry more than one share or scheme so the arms mix periods
)

// How the runs of one arm of one producer fared
// An arm is the drawn runs that applied items or the drawn runs a holdout kept them from
// Arm is the first field and Producer comes before the fields added later so a reader that knows only the arm columns reads every row
type EffectRow struct {
	Arm    Arm `json:"arm"`
	Runs   int `json:"runs"`
	Judged int `json:"judged"`
	// Judged runs whose verdict corrects
	Corrected int `json:"corrected"`
	// Corrections with a reason code an item of the run was taught by
	// Any correction counts for items taught without a code
	SameReason int `json:"same_reason"`
	// Empty on the two rows of a report without runs
	Producer string `json:"producer,omitempty"`
	// Judged runs whose verdict names a person with no proof and judged runs only the conversation or the capture judged
	// Judged counts them with the verdicts of a proven person so the rate keeps every verdict and the counts say whose they are
	Named    int `json:"named"`
	Inferred int `json:"inferred"`
	// The start of the runs the report read and zero for every run
	Since time.Time `json:"since,omitzero"`
	// The draw units of the arm
	// Every run of one holdout key is one unit since the key draws the arm once
	Units int `json:"units"`
	// The share of the draw units the arm should hold by the shares they recorded
	ExpectedShare float64 `json:"expected_share"`
	// Corrected of judged with its 95 percent Wilson interval over the counts shrunk by the design effect of the units
	// Nil without a judged run
	Rate     *float64 `json:"rate"`
	RateLow  *float64 `json:"rate_low"`
	RateHigh *float64 `json:"rate_high"`
	// Judged of runs and nil without a run
	JudgedRate *float64 `json:"judged_rate"`
	// The verdicts on random audit samples of the queue
	// The queue draws them per arm so they stay free of what people chose to judge
	AuditJudged    int `json:"audit_judged"`
	AuditCorrected int `json:"audit_corrected"`
	// The latest outcome a person recorded on the runs
	Confirmed int `json:"confirmed"`
	Refuted   int `json:"refuted"`
	// The fields below are the same on both rows of a producer
	// Runs with items to receive that went through no draw and count in neither arm
	Undrawn int `json:"undrawn"`
	// The chi squared p value of the split of the units against the shares and nil without a drawn run
	SRMP *float64 `json:"srm_p"`
	// The rate of the applied arm minus that of the withheld arm with its 95 percent Newcombe interval
	// Nil when an arm has no judged run or the split fails SRMThreshold
	Difference     *float64 `json:"difference"`
	DifferenceLow  *float64 `json:"difference_low"`
	DifferenceHigh *float64 `json:"difference_high"`
	// An arm has fewer judged runs than EffectMinJudged
	Insufficient bool            `json:"insufficient"`
	Warnings     []EffectWarning `json:"warnings"`
}

// The applied arm and the withheld arm of each producer in producer order
// 1. only drawn runs that had items to receive count so both arms come from one draw over places with approved items
// 2. arms split per producer so the share of one producer never weighs the arms of another
// 3. a producer with only undrawn runs still gets its two rows so the undrawn count shows
// 4. without such runs the two arms come with zero counts and no producer so the report always has rows
// 5. the runs of one draw unit count as one unit and a drawn run without a unit counts as a unit of its own
func (r Runs) Effect(items knowledge.Set, outcomes feedback.Outcomes, since time.Time) []EffectRow {
	evidence := map[knowledge.Ref][]string{}
	for _, k := range items {
		evidence[knowledge.Ref{ID: k.ID, Version: k.Version}] = k.Evidence.FeedbackTraceIDs
	}
	latest := map[string]feedback.Outcome{}
	for _, o := range outcomes.Human().Latest() {
		latest[o.TraceID] = o
	}
	arms := map[string]*effectArms{}
	for _, id := range slices.Sorted(maps.Keys(r.ids)) {
		in := r.inputs[id]
		refs, arm := in.Applied, 0
		if len(refs) == 0 {
			refs, arm = in.Withheld, 1
		}
		if len(refs) == 0 {
			continue
		}
		producer := r.producers[id]
		a, ok := arms[producer]
		if !ok {
			a = newEffectArms(producer, since)
			arms[producer] = a
		}
		if !in.Drawn {
			a.undrawn++
			continue
		}
		fb, judged := r.verdicts[id]
		a.add(arm, cmp.Or(in.Unit, id), in.Share, in.Scheme, judged, judged && fb.Corrects())
		if judged {
			r.tally(&a.rows[arm], fb, refs, evidence)
		}
		switch latest[id].Result {
		case feedback.ResultConfirmed:
			a.rows[arm].Confirmed++
		case feedback.ResultRefuted:
			a.rows[arm].Refuted++
		}
	}
	if len(arms) == 0 {
		return []EffectRow{{Arm: ArmApplied, Since: since, Warnings: []EffectWarning{}}, {Arm: ArmWithheld, Since: since, Warnings: []EffectWarning{}}}
	}
	var out []EffectRow
	for _, producer := range slices.Sorted(maps.Keys(arms)) {
		out = append(out, arms[producer].finish()...)
	}
	return out
}

// The audit and reviewer and reason counts of one judged run of the arm with the items it received or would have received
func (r Runs) tally(row *EffectRow, fb feedback.Feedback, refs []knowledge.Ref, evidence map[knowledge.Ref][]string) {
	switch {
	case fb.Implicit():
		row.Inferred++
	case !fb.Proven():
		row.Named++
	}
	if fb.Audit {
		row.AuditJudged++
	}
	if !fb.Corrects() {
		return
	}
	if fb.Audit {
		row.AuditCorrected++
	}
	var codes []feedback.ReasonCode
	for _, ref := range refs {
		taught, _ := r.taught(evidence[ref])
		codes = append(codes, taught...)
	}
	if len(codes) == 0 || slices.Contains(codes, fb.ReasonCode) {
		row.SameReason++
	}
}

// The runs of one draw unit in one arm
type unitRuns struct {
	runs, judged, corrected int
	// The share of the first run of the unit
	share float64
}

// The two arms of one producer while the runs are counted
type effectArms struct {
	rows    [2]EffectRow
	undrawn int
	// The draw units of each arm by unit
	// A unit drawn under two shares may land in both arms and counts once in each
	units [2]map[string]*unitRuns
	// Each share and scheme the drawn runs recorded
	draws map[[2]float64]bool
}

func newEffectArms(producer string, since time.Time) *effectArms {
	return &effectArms{
		rows:  [2]EffectRow{{Producer: producer, Arm: ArmApplied, Since: since}, {Producer: producer, Arm: ArmWithheld, Since: since}},
		units: [2]map[string]*unitRuns{{}, {}},
		draws: map[[2]float64]bool{},
	}
}

// One drawn run of the arm in its unit
func (a *effectArms) add(arm int, unit string, share float64, scheme int, judged, corrected bool) {
	a.draws[[2]float64{share, float64(scheme)}] = true
	u, ok := a.units[arm][unit]
	if !ok {
		u = &unitRuns{share: share}
		a.units[arm][unit] = u
	}
	row := &a.rows[arm]
	row.Runs++
	u.runs++
	if !judged {
		return
	}
	row.Judged++
	u.judged++
	if corrected {
		row.Corrected++
		u.corrected++
	}
}

// The two rows with their rates and the checks of the arms
// 1. the split is tested on draw units since the draw picks a unit and not a run
// 2. each rate and the judged rate gap shrink their counts by the design effect of their units so alike runs of one unit never narrow an interval
func (a *effectArms) finish() []EffectRow {
	applied, withheld := &a.rows[0], &a.rows[1]
	applied.Units, withheld.Units = len(a.units[0]), len(a.units[1])
	drawn := applied.Units + withheld.Units
	warnings := []EffectWarning{}
	var srm, diff, low, high *float64
	if drawn > 0 {
		shares := 0.0
		for _, units := range a.units {
			for _, u := range units {
				shares += u.share
			}
		}
		expected := shares / float64(drawn)
		applied.ExpectedShare, withheld.ExpectedShare = 1-expected, expected
		srm = ptr(splitP(float64(applied.Units), float64(withheld.Units), float64(drawn)-shares, shares))
	}
	var judgedRates [2]effective
	for i := range a.rows {
		row := &a.rows[i]
		row.Rate, row.RateLow, row.RateHigh = clustered(a.units[i], (*unitRuns).corrections).wilson()
		row.JudgedRate = share(row.Judged, row.Runs)
		judgedRates[i] = clustered(a.units[i], (*unitRuns).judgments)
	}
	insufficient := applied.Judged < EffectMinJudged || withheld.Judged < EffectMinJudged
	switch {
	case srm != nil && *srm < SRMThreshold:
		warnings = append(warnings, WarnSRM)
	case applied.Rate != nil && withheld.Rate != nil:
		diff, low, high = newcombe(applied, withheld)
	}
	if p, ok := judgedRates[0].differs(judgedRates[1]); ok && p < JudgedRateThreshold {
		warnings = append(warnings, WarnJudgedRate)
	}
	if insufficient {
		warnings = append(warnings, WarnInsufficient)
	}
	if len(a.draws) > 1 {
		warnings = append(warnings, WarnMixedShares)
	}
	for i := range a.rows {
		row := &a.rows[i]
		row.Undrawn, row.SRMP, row.Difference, row.DifferenceLow, row.DifferenceHigh = a.undrawn, srm, diff, low, high
		row.Insufficient, row.Warnings = insufficient, warnings
	}
	return a.rows[:]
}

// The corrected runs of the unit out of its judged runs
func (u *unitRuns) corrections() (int, int) {
	return u.corrected, u.judged
}

// The judged runs of the unit out of its runs
func (u *unitRuns) judgments() (int, int) {
	return u.judged, u.runs
}

// A count of x in n shrunk by a design effect so a binomial interval or test over it allows for clustering
type effective struct {
	x, n float64
}

// The counts the part picks from each unit shrunk by their design effect
// 1. the design effect is the cluster robust variance of the ratio over the binomial variance and never below 1
// 2. units of one run each give a design effect of 1 so the counts stay as they are
// 3. a rate of 0 or 1 has no binomial variance and keeps its counts
func clustered(units map[string]*unitRuns, part func(*unitRuns) (int, int)) effective {
	var x, n float64
	for _, u := range units {
		ux, un := part(u)
		x, n = x+float64(ux), n+float64(un)
	}
	if n == 0 {
		return effective{}
	}
	p := x / n
	binomial := p * (1 - p) * n
	if binomial == 0 {
		return effective{x: x, n: n}
	}
	robust := 0.0
	for _, u := range units {
		ux, un := part(u)
		robust += (float64(ux) - p*float64(un)) * (float64(ux) - p*float64(un))
	}
	deff := max(1, robust/binomial)
	return effective{x: x / deff, n: n / deff}
}

func ptr(v float64) *float64 {
	return &v
}

// The chi squared p value of two observed counts against two expected ones with one degree of freedom
func splitP(observedA, observedB, expectedA, expectedB float64) float64 {
	chi := 0.0
	for _, cell := range [][2]float64{{observedA, expectedA}, {observedB, expectedB}} {
		if cell[1] > 0 {
			chi += (cell[0] - cell[1]) * (cell[0] - cell[1]) / cell[1]
		}
	}
	return math.Erfc(math.Sqrt(chi / 2))
}

// The rate with its 95 percent Wilson score interval and nils without a count
func (e effective) wilson() (*float64, *float64, *float64) {
	if e.n == 0 {
		return nil, nil, nil
	}
	p, total := e.x/e.n, e.n
	denom := 1 + z95*z95/total
	center := (p + z95*z95/(2*total)) / denom
	half := z95 * math.Sqrt(p*(1-p)/total+z95*z95/(4*total*total)) / denom
	return ptr(p), ptr(max(0, center-half)), ptr(min(1, center+half))
}

// The rate of a minus the rate of b with the Newcombe hybrid score interval built from their Wilson intervals
func newcombe(a, b *EffectRow) (*float64, *float64, *float64) {
	d := *a.Rate - *b.Rate
	low := d - math.Hypot(*a.Rate-*a.RateLow, *b.RateHigh-*b.Rate)
	high := d + math.Hypot(*a.RateHigh-*a.Rate, *b.Rate-*b.RateLow)
	return ptr(d), ptr(low), ptr(high)
}

// The two sided p value of the pooled z test that both counts share one rate
// Not ok when either count is empty or the pooled rate leaves no variance
func (e effective) differs(o effective) (float64, bool) {
	if e.n == 0 || o.n == 0 {
		return 0, false
	}
	pooled := (e.x + o.x) / (e.n + o.n)
	se := math.Sqrt(pooled * (1 - pooled) * (1/e.n + 1/o.n))
	if se == 0 {
		return 0, false
	}
	z := (e.x/e.n - o.x/o.n) / se
	return math.Erfc(math.Abs(z) / math.Sqrt2), true
}
