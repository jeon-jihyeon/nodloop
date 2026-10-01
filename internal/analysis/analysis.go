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
	RuleCommand       Rule = "command"
)

type Policy struct {
	Version string `yaml:"version" json:"version"`
	// Loaded by LoadContexts from the contexts key
	// A hand built policy without any flags no change context
	Contexts  evidence.Contexts `yaml:"-" json:"-"`
	Analyzers []RuleSpec        `yaml:"analyzers" json:"analyzers"`
	// Names an analyzer reads that no event of the data set it was bound to carries
	absent []absence
	// Starts the command analyzers
	run Runner
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
	// command only
	// The name leads every summary and is unique among the command analyzers of a policy
	Name string `yaml:"name,omitempty" json:"name,omitempty"`
	// command only
	// The argv started in the data directory
	Command []string `yaml:"command,omitempty" json:"command,omitempty"`
	// command only
	// Zero means 10 seconds
	Timeout time.Duration `yaml:"timeout,omitempty" json:"timeout,omitempty"`
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
	// Set when no event of the data set carries the metric or the group dimension
	// The row then reports the data set and not the event
	Absent bool `json:"absent,omitempty"`
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
	// concentration_change only
	// 1. its own level change at a fixed total makes less than half of its share delta
	// 2. the rest of the delta comes from the total that the other groups moved
	Diluted bool `json:"diluted,omitempty"`
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

// Metrics like Metrics without the absent rows
// Every event of a data set gets the same absent rows so they never tell one event from another
func (obs Observations) Measured() []string {
	var kept Observations
	for _, o := range obs {
		if !o.Absent {
			kept = append(kept, o)
		}
	}
	return kept.Metrics()
}

// Whether the observation reports a movement of its metric
// A coverage gap is not a movement of the metric
func (o Observation) moved() bool {
	return o.Adequate && o.Rule != RuleCoverage
}

// Metrics whose value moved in an adequate observation
// A diluted group still counts because the other groups of its metric moved
func (obs Observations) Moved() []string {
	var out []string
	for _, o := range obs {
		if o.moved() && !slices.Contains(out, o.Metric) {
			out = append(out, o.Metric)
		}
	}
	return out
}

// Moved series with the dimension values each observation targets
// 1. a concentration target names only its group dimension
// 2. a diluted group is left out because only the other groups moved it
// So it drops from the group value axis while its metric stays in Moved
func (obs Observations) MovedSeries() []evidence.SeriesRef {
	var out []evidence.SeriesRef
	for _, o := range obs {
		if o.moved() && !o.Detail.Diluted {
			out = append(out, evidence.SeriesRef{Metric: o.Metric, Dims: o.Target})
		}
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
// 3. a name absent from the whole data set is reported inadequate unless this event carries it by now
// 4. a command analyzer runs through the runner of the policy
func (p Policy) Analyze(ev evidence.Event) (Observations, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	points := series(ev.Points)
	var out Observations
	unflagged := p.Contexts.Breaks(ev.ChangeContext)
	for _, spec := range p.Analyzers {
		if unflagged && spec.Rule == RuleCoverage {
			out = append(out, points.contextObservation(ev.ID, ev.ChangeContext))
			unflagged = false
		}
		if spec.Rule == RuleCommand {
			out = append(out, p.command(spec, ev)...)
			continue
		}
		out = append(out, analyzers[spec.Rule](spec, ev.ID, points)...)
	}
	for _, a := range p.absent {
		if !points.carries(a) {
			out = append(out, points.absenceObservation(ev.ID, a))
		}
	}
	slices.SortStableFunc(out, Observation.compare)
	return out, nil
}

// A limits section is refused so a file written for the configurable caps never loses them without a word
// run starts the command analyzers and only a policy without one may pass nil
func LoadPolicy(b []byte, run Runner) (Policy, error) {
	var file struct {
		Policy `yaml:",inline"`
		Limits yaml.Node `yaml:"limits"`
	}
	if err := yaml.Unmarshal(b, &file); err != nil {
		return Policy{}, fmt.Errorf("%w: %w", ErrMalformedPolicy, err)
	}
	if file.Limits.Kind != 0 {
		return Policy{}, ErrLimitsSection
	}
	p := file.Policy
	p.run = run
	if p.Version == "" {
		return Policy{}, ErrMissingVersion
	}
	if err := p.validate(); err != nil {
		return Policy{}, err
	}
	contexts, err := LoadContexts(b)
	if err != nil {
		return Policy{}, err
	}
	p.Contexts = contexts
	return p, nil
}

// The change contexts a policy file declares and nothing else of it
// So a command that never analyzes reads them from a file whose analyzers are broken
// 1. a file that declares none gets the default five
// 2. unknown is appended when left out because an event without a context row reads it
// 3. an empty name or a repeated one or an unknown that breaks fails
func LoadContexts(b []byte) (evidence.Contexts, error) {
	var file struct {
		Contexts []struct {
			Name           evidence.Context `yaml:"name"`
			BreaksBaseline bool             `yaml:"breaks_baseline"`
		} `yaml:"contexts"`
	}
	if err := yaml.Unmarshal(b, &file); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMalformedPolicy, err)
	}
	if len(file.Contexts) == 0 {
		return evidence.DefaultContexts(), nil
	}
	var out evidence.Contexts
	for _, d := range file.Contexts {
		switch {
		case d.Name == "":
			return nil, fmt.Errorf("%w: a context without a name", ErrUnknownContextDecl)
		case out.Valid(d.Name):
			return nil, fmt.Errorf("%w: %q declared twice", ErrUnknownContextDecl, d.Name)
		case d.Name == evidence.ContextUnknown && d.BreaksBaseline:
			return nil, fmt.Errorf("%w: %q never breaks the baseline", ErrUnknownContextDecl, d.Name)
		}
		out = append(out, evidence.DeclaredContext{Name: d.Name, BreaksBaseline: d.BreaksBaseline})
	}
	if !out.Valid(evidence.ContextUnknown) {
		out = append(out, evidence.DeclaredContext{Name: evidence.ContextUnknown})
	}
	return out, nil
}

// Version is left to LoadPolicy because Analyze runs hand built policies too
// Command analyzers need the runner and one name each
func (p Policy) validate() error {
	names := map[string]bool{}
	for i, spec := range p.Analyzers {
		if err := spec.validate(); err != nil {
			return fmt.Errorf("%w: analyzer %d rule %q", err, i, spec.Rule)
		}
		if spec.Rule != RuleCommand {
			continue
		}
		if names[spec.Name] {
			return fmt.Errorf("%w: analyzer %d name %q", ErrCommandName, i, spec.Name)
		}
		if p.run == nil {
			return fmt.Errorf("%w: analyzer %d name %q", ErrCommandRunner, i, spec.Name)
		}
		names[spec.Name] = true
	}
	return nil
}

func (spec RuleSpec) validate() error {
	if spec.Rule == RuleCommand {
		if spec.Name == "" || len(spec.Command) == 0 || spec.Command[0] == "" {
			return ErrIncompleteCommand
		}
		return nil
	}
	if _, ok := analyzers[spec.Rule]; !ok {
		return ErrUnknownRule
	}
	switch {
	case len(spec.Metrics) == 0 || spec.Window <= 0 || spec.Baseline <= 0:
		return ErrIncompleteAnalyzer
	case spec.Rule == RuleProportion && len(spec.Metrics) != 2:
		return ErrProportionMetrics
	case spec.Rule == RuleConcentration && spec.GroupBy == "":
		return ErrMissingGroupBy
	}
	return nil
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

// Distinct points in time
// Several groups share one time so a total over them divides by this and not by the point count
func (s series) times() int {
	seen := map[int64]struct{}{}
	for _, p := range s {
		seen[p.Time.UnixNano()] = struct{}{}
	}
	return len(seen)
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
