package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/loop"
)

// report curation as text lines or with --json as one JSON value
// It reads the time of the knob events against now so it is no report of a name the MCP tool and the HTTP API serve
func runCurationReport(args []string, getenv func(string) string, now func() time.Time, stdout, stderr io.Writer) int {
	fs := newFlagSet("report curation", stderr)
	var records recordFlags
	records.bind(fs)
	asJSON := fs.Bool("json", false, "print the report as one JSON value")
	if err := fs.Parse(args); err != nil {
		return parseFailed(err)
	}
	if fs.NArg() != 0 {
		return fail(stderr, "report", fmt.Errorf("%w %q", errUnknownAction, fs.Arg(0)))
	}
	a, err := records.app(getenv, now)
	if err != nil {
		return fail(stderr, "report", err)
	}
	ctx := context.Background()
	stores, err := a.reportStores(ctx)
	if err != nil {
		return fail(stderr, "report", err)
	}
	report, err := stores.Curation(ctx, now())
	if err != nil {
		return fail(stderr, "report", err)
	}
	if *asJSON {
		if err := json.NewEncoder(stdout).Encode(report); err != nil {
			return fail(stderr, "report", err)
		}
		return 0
	}
	loopCommand{app: a, out: stdout}.curation(report)
	return 0
}

// The window line first and one line per row a person reads after it in the order of the report
func (c loopCommand) curation(r loop.CurationReport) {
	fmt.Fprintf(c.out, "window\t%s\n", windowText(r.Since))
	for _, cy := range r.Cycles {
		fmt.Fprintf(c.out, "cycle\t%s\t%s to %s\t%s\tpolicy %s\tjudge %s %s prompt %s\t%s\n",
			cy.ID, cy.Start.Format(time.RFC3339), cy.End.Format(time.RFC3339), map[bool]string{true: "closed", false: "open"}[cy.Closed],
			list(cy.Policies), cy.Model, cy.Effort, cy.Prompt, map[bool]string{true: "active", false: "shadow"}[cy.Activated])
	}
	c.touchpoints(r.Touchpoints)
	c.kinds(r.Kinds, r.Calibration)
	c.shadow(r.Shadow)
	for _, e := range r.Estimates {
		fmt.Fprintf(c.out, "estimate\tcycle %s\ttier %d\tanswered %d\tdiffer %d\tno answer %d\trate %s\tkish %s\tworst %s\n",
			e.Cycle, e.Tier, e.Answered, e.Differ, e.NoAnswer, interval(e.Rate, e.RateLow, e.RateHigh), number(e.Kish), number(e.Worst))
	}
	c.overturns(r.Overturns)
	c.judge(r.CUSUM, r.Shift)
	for _, it := range r.Items {
		fmt.Fprintf(c.out, "item\t%s\tv%d\tunits %d\tincluded %d judged %d rate %s\theld %d judged %d rate %s\tdifference %s\n",
			it.ID, it.Version, it.Units, it.Included, it.IncludedJudged, interval(it.IncludedRate, it.IncludedLow, it.IncludedHigh),
			it.Held, it.HeldJudged, interval(it.HeldRate, it.HeldLow, it.HeldHigh), interval(it.Difference, it.DifferenceLow, it.DifferenceHigh))
	}
}

// One line per cycle then per week with the questions and the sessions shown one
// Notices and proposals count apart before the answers, the interventions and the runes
func (c loopCommand) touchpoints(rows []loop.Touchpoints) {
	for _, t := range rows {
		questions, kinds := 0, map[string]int{}
		for kind, sources := range t.Questions {
			for source, n := range sources {
				questions += n
				kinds[kind+" "+source] = n
			}
		}
		median := "-"
		if t.RunesMedian != nil {
			median = fmt.Sprintf("%.0f", *t.RunesMedian)
		}
		fmt.Fprintf(c.out, "touchpoints\t%s %s\tquestions %d in %d sessions\t%s\tnotices %d\tproposals %d\tanswers %d\tno answer %d",
			t.Period, t.Start, questions, t.Sessions, counts(kinds), t.Notices, t.Proposals, t.Answers, t.NoAnswer)
		fmt.Fprintf(c.out, "\tinterventions %s\trunes median %s max %d\n", counts(t.Interventions), median, t.RunesMax)
	}
}

// One line per kind and bin with its decisions, answers, posterior and bound
// Then the calibration of the judge per bin
func (c loopCommand) kinds(rows []loop.KindRow, cal loop.Calibration) {
	for _, k := range rows {
		ps := make([]string, 0, len(k.P))
		for _, p := range k.P {
			ps = append(ps, fmt.Sprintf("%.2f", p))
		}
		fmt.Fprintf(c.out, "kind\t%s\tbin %s\tdecided %d\tcarried %d\tasked %d\tdrawn %d at %s\texposed %d\tsame %d\tdiffer %d\tno answer %d",
			k.Kind, dash(k.Bin), k.Decided, k.Carried, k.Asked, k.Drawn, list(ps), k.Exposed, k.Same, k.Differ, k.NoAnswer)
		fmt.Fprintf(c.out, "\tposterior %.3f above %s since %s\tbound %s e %s\topen %s\tbound met %s\n",
			k.Mean, number(k.Above), dash(k.PosteriorSince), number(k.Bound), number(k.E), yesNo(k.Open), yesNo(k.BoundMet))
	}
	for _, b := range cal.Bins {
		fmt.Fprintf(c.out, "calibration\t%s\tanswered %d\tsame %d\trate %s\n", b.Bin, b.Answered, b.Same, interval(b.Rate, b.RateLow, b.RateHigh))
	}
	fmt.Fprintf(c.out, "calibration\tbrier %s over %d\n", number(cal.Brier), cal.Scored)
}

// The closed cycles in shadow then one line per kind decided in shadow
func (c loopCommand) shadow(s loop.ShadowReport) {
	fmt.Fprintf(c.out, "shadow\tcycles %d\n", s.Cycles)
	for _, k := range s.Kinds {
		selected := "-"
		if n := len(k.PSelect); n > 0 {
			selected = fmt.Sprintf("%.3f to %.3f", k.PSelect[0], k.PSelect[n-1])
		}
		fmt.Fprintf(c.out, "shadow\t%s\tdecided %d\tasked %d at %s\tanswered %d\tsame %d\tshare %s\tbound %s\tbound met %s\n",
			k.Kind, k.Decided, k.Asked, selected, k.Answered, k.Same, number(k.Share), number(k.Bound), yesNo(k.BoundMet))
	}
}

// The overturns line then one repeat line per approver
func (c loopCommand) overturns(o loop.Overturns) {
	fmt.Fprintf(c.out, "overturns\tdirection %d of %d\tmanual %d of %d\tundo %d\tpause %d\n",
		o.DirectionOverturned, o.Direction, o.ManualOverturned, o.Manual, o.Undo, o.Pause)
	for _, r := range o.Repeats {
		fmt.Fprintf(c.out, "repeat\t%s\titems %d\trepeat %d of %d\tnamed repeat %d of %d\tinferred repeat %d of %d\n",
			r.Approver, r.Items, r.Repeat, r.Judged, r.NamedRepeat, r.NamedJudged, r.InferredRepeat, r.InferredJudged)
	}
}

// The CUSUM line then one shift line per cycle
func (c loopCommand) judge(s loop.CUSUMReport, shifts []loop.ShiftRow) {
	steps := make([]string, 0, len(s.Alarms))
	for _, a := range s.Alarms {
		steps = append(steps, fmt.Sprint(a.Step))
	}
	fmt.Fprintf(c.out, "cusum\tanswers %d\ts %.3f\talarms at %s\talarm %s\tnarrow %s\n", s.Answers, s.S, list(steps), yesNo(s.Alarm), kindBins(s.Narrow))
	for _, r := range shifts {
		fmt.Fprintf(c.out, "shift\tcycle %s\tagainst %s\tjudged %d and %d\tp %s\tchanged %s\tshift %s\ttouched %s\n",
			r.Cycle, dash(r.Against), r.Judged, r.AgainstJudged, number(r.P), yesNo(r.Changed), yesNo(r.Shift), list(r.Touched))
	}
}

// The values joined by commas or a dash for none
func list(values []string) string {
	if len(values) == 0 {
		return "-"
	}
	return strings.Join(values, ",")
}

// The kinds and bins as `add b1` joined by commas or a dash for none
func kindBins(kbs []loop.KindBin) string {
	parts := make([]string, 0, len(kbs))
	for _, kb := range kbs {
		parts = append(parts, strings.TrimSpace(kb.Kind+" "+kb.Bin))
	}
	return list(parts)
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// The numbers the knobs of curate move on as the report found them
// The first pass of a cycle hands them to Knobs Start and every other pass to Knobs Alarm
// 1. a gauge per kind and bin with its posterior above the target and whether its bound was met
// 2. the kinds and bins of a CUSUM alarm after the newest knob event
// 3. a shift of the judge in the newest closed cycle
// 4. the closed cycles in shadow and whether the shadow answers of add met the bound
func curationNumbers(r loop.CurationReport) curate.Numbers {
	n := curate.Numbers{ShadowCycles: r.Shadow.Cycles}
	for _, k := range r.Kinds {
		g := curate.Gauge{
			Opening: curate.Opening{Kind: curate.ChangeKind(k.Kind), Bin: curate.Bin(k.Bin)}, Answers: k.Same + k.Differ, Differ: k.Differ,
			Met: k.BoundMet,
		}
		if k.Above != nil {
			g.Above = *k.Above
		}
		n.Gauges = append(n.Gauges, g)
	}
	if r.CUSUM.Alarm {
		for _, kb := range r.CUSUM.Narrow {
			n.Alarmed = append(n.Alarmed, curate.Opening{Kind: curate.ChangeKind(kb.Kind), Bin: curate.Bin(kb.Bin)})
		}
	}
	newest := ""
	for _, cy := range r.Cycles {
		if cy.Closed {
			newest = cy.ID
		}
	}
	i := slices.IndexFunc(r.Shift, func(s loop.ShiftRow) bool { return s.Cycle == newest })
	n.Shift = i >= 0 && r.Shift[i].Shift
	i = slices.IndexFunc(r.Shadow.Kinds, func(k loop.ShadowRow) bool { return k.Kind == string(curate.ChangeAdd) })
	n.ShadowMet = i >= 0 && r.Shadow.Kinds[i].BoundMet
	return n
}
