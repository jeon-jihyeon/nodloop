// Package analysis turns one event into Observations by code so the AI never computes numbers
package analysis

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/jeon-jihyeon/nodloop/internal/evidence"
)

type Rule string

const (
	RuleZScore        Rule = "zscore"
	RuleProportion    Rule = "proportion_control"
	RuleConcentration Rule = "concentration_change"
	RuleCoverage      Rule = "coverage_rule"
)

type Policy struct {
	Version   string     `yaml:"version" json:"version"`
	Analyzers []RuleSpec `yaml:"analyzers" json:"analyzers"`
}

type RuleSpec struct {
	Rule Rule `yaml:"rule" json:"rule"`
	// Metrics the rule reads
	// proportion_control reads numerator then denominator
	Metrics []string `yaml:"metrics" json:"metrics"`
	// Dimension the rule groups by
	// concentration_change needs one
	GroupBy string `yaml:"group_by,omitempty" json:"group_by,omitempty"`
	// Points before the window under test that form the baseline
	Baseline int `yaml:"baseline" json:"baseline"`
	// Points under test at the end of the series
	Window int `yaml:"window" json:"window"`
	// Rule specific
	// 1. zscore: z
	// 2. proportion_control: control limit multiplier
	// 3. concentration_change: share delta
	// 4. coverage_rule: missing ratio
	Threshold float64 `yaml:"threshold" json:"threshold"`
	// Series whose baseline has fewer points are reported inadequate and never scored
	MinSamples int `yaml:"min_samples" json:"min_samples"`
	// Newest window points reported apart from the rest of the window
	// Zero means the window is one number
	// proportion_control uses it so a rate that fell only in the freshest hours is visible as such
	Recent int `yaml:"recent,omitempty" json:"recent,omitempty"`
}

type Observation struct {
	Rule Rule `json:"rule"`
	// Dimension values of the series or group
	// Empty for whole event rules
	Target map[string]string `json:"target,omitempty"`
	// Empty for the change context observation because it flags the whole event
	Metric   string  `json:"metric"`
	Window   Window  `json:"window"`
	Current  float64 `json:"current"`
	Baseline float64 `json:"baseline"`
	// Rule specific
	// 1. zscore: z
	// 2. proportion_control: rate ratio
	// 3. concentration_change: share delta
	// 4. coverage_rule: missing ratio
	Change float64 `json:"change"`
	// 0 to 1 for ordering only
	Severity float64 `json:"severity"`
	// false when the series cannot be scored
	// Numbers computed before the failing check are still reported
	Adequate bool `json:"adequate"`
	// So the reader knows what the summary hides
	Detail Detail `json:"detail"`
	// Raw range reference for detail lookup
	Ref Ref `json:"ref"`
	// One line for the review context and the trace
	Summary string `json:"summary"`
}

// Series name for summaries
// Metric then target dimensions in name order
func (o Observation) name() string {
	var b strings.Builder
	b.WriteString(o.Metric)
	for _, k := range slices.Sorted(maps.Keys(o.Target)) {
		b.WriteString(" ")
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(o.Target[k])
	}
	return b.String()
}

type Window struct {
	Start  time.Time `json:"start"`
	End    time.Time `json:"end"`
	Points int       `json:"points"`
}

type Detail struct {
	// coverage_rule stores the last empty slot of the widest gap
	PeakTime time.Time `json:"peak_time"`
	// coverage_rule stores the empty slots of the widest gap
	PeakValue float64 `json:"peak_value"`
	// Points the observation was computed from
	// 1. zscore and coverage_rule: the points of the series
	// 2. proportion_control: the numerator points paired with a denominator point
	// 3. concentration_change: the points of every group
	// 4. change context: the points of the whole event
	Samples int `json:"samples"`
	// 1. coverage_rule: empty window slots
	// 2. proportion_control: points without a partner at the same time in the other metric
	Missing int `json:"missing"`
	// Rate of the newest Recent window points
	// Zero when the spec has no Recent
	RecentRate float64 `json:"recent_rate,omitempty"`
}

// What the analyzers reported for one event
// Scored first and then by severity
type Observations []Observation

func (o Observation) compare(other Observation) int {
	switch {
	case o.Adequate != other.Adequate && o.Adequate:
		return -1
	case o.Adequate != other.Adequate:
		return 1
	case o.Severity > other.Severity:
		return -1
	case o.Severity < other.Severity:
		return 1
	}
	return 0
}

// Distinct metrics in observation order
// 1. never nil so a stored context reads as a list
// 2. the change context observation names no metric and is skipped
func (obs Observations) Metrics() []string {
	out := []string{}
	for _, o := range obs {
		if o.Metric != "" && !slices.Contains(out, o.Metric) {
			out = append(out, o.Metric)
		}
	}
	return out
}

// Metrics whose value moved in an adequate observation
// Coverage observations are excluded because a gap is not a movement of the metric
func (obs Observations) Moved() []string {
	var out []string
	for _, o := range obs {
		if !o.Adequate || o.Rule == RuleCoverage || slices.Contains(out, o.Metric) {
			continue
		}
		out = append(out, o.Metric)
	}
	return out
}

type Ref struct {
	EventID string    `json:"event_id"`
	Start   time.Time `json:"start"`
	End     time.Time `json:"end"`
}

// One method per rule over the event id and the points of one event
// The policy is validated first so an analyzer never meets a spec it cannot run
type analyzer func(spec RuleSpec, eventID string, points series) []Observation

var analyzers = map[Rule]analyzer{
	RuleZScore:        RuleSpec.zscore,
	RuleProportion:    RuleSpec.proportionControl,
	RuleConcentration: RuleSpec.concentrationChange,
	RuleCoverage:      RuleSpec.coverageRule,
}

// Runs every analyzer of the policy and orders the result by severity
// 1. inadequate observations sort last so the review sees scored series first
// 2. a change context that breaks the comparison is flagged once where the first coverage_rule runs
func (p Policy) Analyze(ev evidence.Event) (Observations, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	points := series(ev.Points)
	var out Observations
	unflagged := ev.ChangeContext.Breaks()
	for _, spec := range p.Analyzers {
		if unflagged && spec.Rule == RuleCoverage {
			out = append(out, points.contextObservation(ev.ID, ev.ChangeContext))
			unflagged = false
		}
		out = append(out, analyzers[spec.Rule](spec, ev.ID, points)...)
	}
	slices.SortStableFunc(out, Observation.compare)
	return out, nil
}

func LoadPolicy(b []byte) (Policy, error) {
	var p Policy
	if err := yaml.Unmarshal(b, &p); err != nil {
		return Policy{}, fmt.Errorf("%w: %w", ErrMalformedPolicy, err)
	}
	if p.Version == "" {
		return Policy{}, ErrMissingVersion
	}
	if err := p.validate(); err != nil {
		return Policy{}, err
	}
	return p, nil
}

// Version is left to LoadPolicy because Analyze runs hand built policies too
func (p Policy) validate() error {
	for i, spec := range p.Analyzers {
		if err := spec.validate(); err != nil {
			return fmt.Errorf("%w: analyzer %d rule %q", err, i, spec.Rule)
		}
	}
	return nil
}

func (spec RuleSpec) validate() error {
	if _, ok := analyzers[spec.Rule]; !ok {
		return ErrUnknownRule
	}
	switch {
	case len(spec.Metrics) == 0 || spec.Window <= 0 || spec.Baseline <= 0:
		return ErrIncompleteAnalyzer
	case spec.Rule == RuleProportion && len(spec.Metrics) < 2:
		return ErrMissingDenominator
	case spec.Rule == RuleConcentration && spec.GroupBy == "":
		return ErrMissingGroupBy
	}
	return nil
}

// Policy for the demo data set
// Hourly series of 48 points with the last 12 under test
const DefaultPolicyYAML = `version: demo-1
analyzers:
  - rule: zscore
    metrics: [click_count]
    baseline: 36
    window: 12
    threshold: 3
    min_samples: 12
  - rule: proportion_control
    metrics: [conversion_count, click_count]
    baseline: 36
    window: 12
    threshold: 3
    min_samples: 12
    recent: 4
  - rule: concentration_change
    metrics: [click_count]
    group_by: source
    baseline: 36
    window: 12
    threshold: 0.15
    min_samples: 12
  - rule: coverage_rule
    metrics: [click_count, conversion_count]
    baseline: 36
    window: 12
    threshold: 0.2
    min_samples: 12
`

func DefaultPolicy() Policy {
	p, err := LoadPolicy([]byte(DefaultPolicyYAML))
	if err != nil {
		panic(err)
	}
	return p
}

// Points in time order
// An analyzer receives every point of the event and bySeries splits them into one series per key
type series []evidence.Point

// Points of one metric split by series key in first seen order
func (s series) bySeries(metric string) []series {
	var out []series
	index := map[string]int{}
	for _, p := range s {
		if p.Metric != metric {
			continue
		}
		i, ok := index[p.SeriesKey()]
		if !ok {
			i = len(out)
			index[p.SeriesKey()] = i
			out = append(out, nil)
		}
		out[i] = append(out[i], p)
	}
	return out
}

// The last Window points are the window and the Baseline points before them the baseline
// Short series return what exists so the caller can report inadequacy
func (spec RuleSpec) split(points series) (baseline, window series) {
	if len(points) <= spec.Window {
		return nil, points
	}
	window = points[len(points)-spec.Window:]
	start := max(len(points)-spec.Window-spec.Baseline, 0)
	return points[start : len(points)-spec.Window], window
}

// Severity saturates at twice the threshold so a hit at the limit sits mid scale
// A zero threshold is a hit on any change so it is maximal
func (spec RuleSpec) severity(v float64) float64 {
	if spec.Threshold <= 0 {
		return 1
	}
	return min(math.Abs(v)/(2*spec.Threshold), 1)
}

// Why a baseline of n points cannot be scored
// Empty when it can
func (spec RuleSpec) inadequacy(n int) string {
	if n >= spec.MinSamples {
		return ""
	}
	return fmt.Sprintf("%d baseline points, fewer than %d required", n, spec.MinSamples)
}

func (s series) sum() float64 {
	var sum float64
	for _, p := range s {
		sum += p.Value
	}
	return sum
}

func (s series) mean() float64 {
	if len(s) == 0 {
		return 0
	}
	return s.sum() / float64(len(s))
}

// Population stddev because the baseline is the whole reference and not a sample of one
func (s series) stddev(mean float64) float64 {
	if len(s) == 0 {
		return 0
	}
	var sum float64
	for _, p := range s {
		d := p.Value - mean
		sum += d * d
	}
	return math.Sqrt(sum / float64(len(s)))
}

func (s series) window() Window {
	if len(s) == 0 {
		return Window{}
	}
	return Window{Start: s[0].Time, End: s[len(s)-1].Time, Points: len(s)}
}

func (s series) ref(eventID string) Ref {
	if len(s) == 0 {
		return Ref{EventID: eventID}
	}
	return Ref{EventID: eventID, Start: s[0].Time, End: s[len(s)-1].Time}
}

// Most common gap between consecutive points
// Ties go to the shortest gap
// Zero when fewer than two points exist
func (s series) cadence() time.Duration {
	counts := map[time.Duration]int{}
	for i := 1; i < len(s); i++ {
		counts[s[i].Time.Sub(s[i-1].Time)]++
	}
	var best time.Duration
	for gap, n := range counts {
		if gap <= 0 {
			continue
		}
		if best == 0 || n > counts[best] || (n == counts[best] && gap < best) {
			best = gap
		}
	}
	return best
}

// Four decimals cover rates near one percent while 3900 stays 3900
func formatNumber(v float64) string {
	s := strconv.FormatFloat(v, 'f', 4, 64)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}
