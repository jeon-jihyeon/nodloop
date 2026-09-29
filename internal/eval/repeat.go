package eval

import (
	"cmp"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/jeon-jihyeon/nodloop/internal/evidence"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// The run of a repeated job counted from one
type repeat int

const repeatPrefix = "repeat:"

func (r repeat) tag() string {
	return repeatPrefix + strconv.Itoa(int(r))
}

// The repeat a trace carries in its tags
// One for a trace of a single run
func repeatOf(tags []string) repeat {
	for _, tag := range tags {
		if value, ok := strings.CutPrefix(tag, repeatPrefix); ok {
			if n, err := strconv.Atoi(value); err == nil && n > 0 {
				return repeat(n)
			}
		}
	}
	return 1
}

func repeated(tags []string) bool {
	return slices.ContainsFunc(tags, isRepeatTag)
}

func isRepeatTag(tag string) bool {
	return strings.HasPrefix(tag, repeatPrefix)
}

// The reviews of the lowest repeat in the session
func (rs reviews) firstRepeat() reviews {
	if len(rs) == 0 {
		return nil
	}
	lowest := repeatOf(rs[0].Tags)
	for _, tr := range rs {
		lowest = min(lowest, repeatOf(tr.Tags))
	}
	var out reviews
	for _, tr := range rs {
		if repeatOf(tr.Tags) == lowest {
			out = append(out, tr)
		}
	}
	return out
}

// The reviews tagged with one of the conditions
func (rs reviews) of(conditions []Condition) reviews {
	var out reviews
	for _, tr := range rs {
		for _, cond := range conditions {
			if slices.Contains(tr.Tags, string(cond)) {
				out = append(out, tr)
				break
			}
		}
	}
	return out
}

// Refuses a run whose repeat tags would mix with the reviews already tagged
// A single run carries no repeat tag and a repeated run carries one on every review
func (rs reviews) checkRepeat(count int) error {
	if count < 0 {
		return fmt.Errorf("%w: %d", ErrRepeat, count)
	}
	for _, tr := range rs {
		if repeated(tr.Tags) != (count > 1) {
			return fmt.Errorf("%w: %s", ErrRepeatSession, tr.ID)
		}
	}
	return nil
}

// The discordant events between two conditions of one run
type Pair struct {
	// The condition earlier in report order
	Reference Condition `json:"reference"`
	Condition Condition `json:"condition"`
	Events    int       `json:"events"`
	// Wrong under the reference and right under the condition
	Fixed int `json:"fixed"`
	// Right under the reference and wrong under the condition
	Regressed int     `json:"regressed"`
	McNemarP  float64 `json:"mcnemar_p"`
}

// Condition pairs in report order
type pairs []Pair

func (ps pairs) table() string {
	if len(ps) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n| reference | condition | paired events | fixed | regressed | exact McNemar p |\n" +
		"|---|---|---|---|---|---|\n")
	for _, p := range ps {
		fmt.Fprintf(&b, "| %s | %s | %d | %d | %d | %.4f |\n", p.Reference, p.Condition,
			p.Events, p.Fixed, p.Regressed, p.McNemarP)
	}
	return b.String()
}

// One repeat of a repeated session
type RunReport struct {
	Repeat    int       `json:"repeat"`
	Summaries []Summary `json:"summaries"`
	Scores    []Score   `json:"scores"`
	Pairs     []Pair    `json:"pairs"`
}

// The share of the runs of one event that agree on the status
type EventAgreement struct {
	EventID   string  `json:"event_id"`
	Runs      int     `json:"runs"`
	Agreement float64 `json:"agreement"`
}

// Orders agreements by event id
func (e EventAgreement) compare(other EventAgreement) int {
	return cmp.Compare(e.EventID, other.EventID)
}

// One condition across the repeats
type Stability struct {
	Condition      Condition        `json:"condition"`
	Runs           int              `json:"runs"`
	Mean           float64          `json:"status_accuracy_mean"`
	Min            float64          `json:"status_accuracy_min"`
	Max            float64          `json:"status_accuracy_max"`
	MisappliedMean float64          `json:"misapplied_mean"`
	MisappliedMin  int              `json:"misapplied_min"`
	MisappliedMax  int              `json:"misapplied_max"`
	Events         []EventAgreement `json:"events"`
}

// Scores every repeat of the session
// 1. the first repeat gives the summaries and scores and comparison and condition pairs so a single run reports as before
// 2. a repeated session adds one run report per repeat and the stability across them
// 3. the paired bootstrap spans every repeat with each event as one cluster
func (ls labelSet) report(session string, rs reviews, latest feedback.Records, revised map[string]struct{}) Report {
	byRepeat := map[repeat]reviews{}
	for _, tr := range rs {
		n := repeatOf(tr.Tags)
		byRepeat[n] = append(byRepeat[n], tr)
	}
	var rep Report
	var all runs
	for i, n := range slices.Sorted(maps.Keys(byRepeat)) {
		run := ls.score(byRepeat[n].newest(), latest, revised).report(session)
		if i == 0 {
			rep = run
		}
		all = append(all, RunReport{Repeat: int(n), Summaries: run.Summaries, Scores: run.Scores, Pairs: run.Pairs})
	}
	rep.Intervals = all.intervals()
	if slices.ContainsFunc(rs, isRepeatedReview) {
		rep.Runs, rep.Stability = all, all.stability()
	}
	return rep
}

func isRepeatedReview(tr trace.Trace) bool {
	return repeated(tr.Tags)
}

// Every holdout condition pair in report order
func (cs conditionScores) pairs() pairs {
	var out pairs
	conditions := holdoutConditions()
	for i, reference := range conditions {
		base := cs[reference].byEvent()
		for _, cond := range conditions[i+1:] {
			p := Pair{Reference: reference, Condition: cond}
			for _, s := range cs[cond] {
				b, ok := base[s.EventID]
				if !ok {
					continue
				}
				p.Events++
				if s.StatusOK && !b.StatusOK {
					p.Fixed++
				}
				if !s.StatusOK && b.StatusOK {
					p.Regressed++
				}
			}
			if p.Events > 0 {
				p.McNemarP = exactMcNemar(p.Fixed, p.Regressed)
				out = append(out, p)
			}
		}
	}
	return out
}

// Exact two sided McNemar p over the discordant events
// A binomial test at one half since the discordant counts are small
// Each repeat is tested apart so repeats of one event never count as independent samples
func exactMcNemar(fixed, regressed int) float64 {
	n := fixed + regressed
	if n == 0 {
		return 1
	}
	logTerm := -float64(n) * math.Ln2
	probability := math.Exp(logTerm)
	for k := 1; k <= min(fixed, regressed); k++ {
		logTerm += math.Log(float64(n-k+1)) - math.Log(float64(k))
		probability += math.Exp(logTerm)
	}
	return min(1, 2*probability)
}

// What one review of an event answered
// A failed review is its own answer apart from any status
type answer struct {
	status evidence.Status
	failed bool
}

func (rs runs) stability() []Stability {
	var out []Stability
	for _, cond := range allConditions() {
		s := Stability{Condition: cond, Min: 1}
		counts := map[string]map[answer]int{}
		for _, run := range rs {
			for _, summary := range run.Summaries {
				if summary.Condition == cond {
					s.add(summary)
				}
			}
			for _, score := range scores(run.Scores).of(cond) {
				if counts[score.EventID] == nil {
					counts[score.EventID] = map[answer]int{}
				}
				counts[score.EventID][answer{score.Status, score.Failed}]++
			}
		}
		if s.Runs == 0 {
			continue
		}
		s.Mean /= float64(s.Runs)
		s.MisappliedMean /= float64(s.Runs)
		for id, answers := range counts {
			var total, most int
			for _, count := range answers {
				total += count
				most = max(most, count)
			}
			s.Events = append(s.Events, EventAgreement{id, total, float64(most) / float64(total)})
		}
		slices.SortFunc(s.Events, EventAgreement.compare)
		out = append(out, s)
	}
	return out
}

// Sums one run so the caller divides by the runs once
func (s *Stability) add(summary Summary) {
	if s.Runs == 0 {
		s.MisappliedMin = summary.Misapplications
	}
	s.Runs++
	s.Mean += summary.StatusAccuracy
	s.Min = min(s.Min, summary.StatusAccuracy)
	s.Max = max(s.Max, summary.StatusAccuracy)
	s.MisappliedMean += float64(summary.Misapplications)
	s.MisappliedMin = min(s.MisappliedMin, summary.Misapplications)
	s.MisappliedMax = max(s.MisappliedMax, summary.Misapplications)
}

func (rep Report) repeatTable() string {
	if len(rep.Stability) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Repeated metrics\n\n| condition | runs | status mean | status min | status max | misapplied mean | misapplied min | misapplied max |\n|---|---|---|---|---|---|---|---|\n")
	for _, s := range rep.Stability {
		fmt.Fprintf(&b, "| %s | %d | %.3f | %.3f | %.3f | %.3f | %d | %d |\n",
			s.Condition, s.Runs, s.Mean, s.Min, s.Max, s.MisappliedMean, s.MisappliedMin, s.MisappliedMax)
	}
	b.WriteString("\n| condition | event | observed runs | status agreement |\n|---|---|---|---|\n")
	for _, s := range rep.Stability {
		for _, event := range s.Events {
			fmt.Fprintf(&b, "| %s | %s | %d | %.3f |\n", s.Condition, event.EventID, event.Runs, event.Agreement)
		}
	}
	b.WriteString("\n| repeat | reference | condition | paired events | fixed | regressed | exact McNemar p |\n|---|---|---|---|---|---|---|\n")
	for _, run := range rep.Runs {
		for _, p := range run.Pairs {
			fmt.Fprintf(&b, "| %d | %s | %s | %d | %d | %d | %.4f |\n",
				run.Repeat, p.Reference, p.Condition, p.Events, p.Fixed, p.Regressed, p.McNemarP)
		}
	}
	b.WriteString(intervals(rep.Intervals).table())
	b.WriteString("\nFirst repeat details\n\n")
	return b.String()
}
