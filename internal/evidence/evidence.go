// Package evidence is the read only reference data behind the Evidence Loop: events and runbook paragraphs and labels
package evidence

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

// What was known to have changed around an event
type Context string

const (
	ContextNoKnownChange      Context = "no_known_change"
	ContextPlannedChange      Context = "planned_operational_change"
	ContextMeasurementChanged Context = "measurement_context_changed"
	ContextDataAvailability   Context = "data_availability_issue"
	ContextUnknown            Context = "unknown"
)

var validContexts = map[Context]struct{}{
	ContextNoKnownChange: {}, ContextPlannedChange: {}, ContextMeasurementChanged: {},
	ContextDataAvailability: {}, ContextUnknown: {},
}

func (c Context) Valid() bool {
	_, ok := validContexts[c]
	return ok
}

// A measurement or availability change leaves the baseline comparison untrusted
// A planned change or an unknown context does not because the runbooks read through them
func (c Context) Breaks() bool {
	return c == ContextMeasurementChanged || c == ContextDataAvailability
}

// The shape a label assigns to an event
type EventType string

const (
	TypeNormalVariation     EventType = "normal_variation"
	TypeClickSpike          EventType = "click_spike"
	TypeConversionRateDrop  EventType = "conversion_rate_drop"
	TypeSourceConcentration EventType = "source_concentration"
	TypeAttributionLag      EventType = "attribution_lag"
	TypeHold                EventType = "hold"
)

var validTypes = map[EventType]struct{}{
	TypeNormalVariation: {}, TypeClickSpike: {}, TypeConversionRateDrop: {},
	TypeSourceConcentration: {}, TypeAttributionLag: {}, TypeHold: {},
}

func (t EventType) Valid() bool {
	_, ok := validTypes[t]
	return ok
}

type EventRef struct {
	ID    string
	Start time.Time
	End   time.Time
}

type Event struct {
	ID string
	// ContextUnknown when the source has no context for the event
	ChangeContext Context
	// Sorted by Time
	Points []Point
}

type Point struct {
	Time   time.Time
	Metric string
	Value  float64
	// Dimension values keyed by dimension name such as source and topic
	Dims map[string]string
}

func (p Point) SeriesKey() string {
	return SeriesRef{Metric: p.Metric, Dims: p.Dims}.Key()
}

// Time order because an event holds its points sorted by time
func (p Point) Compare(q Point) int {
	return p.Time.Compare(q.Time)
}

// Every dimension value the event carries keyed by dimension name
func (e Event) Dims() map[string]map[string]struct{} {
	dims := map[string]map[string]struct{}{}
	for _, p := range e.Points {
		for k, v := range p.Dims {
			if dims[k] == nil {
				dims[k] = map[string]struct{}{}
			}
			dims[k][v] = struct{}{}
		}
	}
	return dims
}

// Point count per series keyed by the series key
func (e Event) SeriesCounts() map[string]int {
	counts := map[string]int{}
	for _, p := range e.Points {
		counts[p.SeriesKey()]++
	}
	return counts
}

// `runbook slug#heading path#index`
// Derived from position only so editing text keeps the id
type ParagraphID string

// A hash or slash inside the slug or a heading becomes a hyphen because Runbook and Section split on them
// A heading such as A/B test stays a paragraph instead of failing the whole runbook set
func NewParagraphID(slug string, path []string, index int) ParagraphID {
	parts := make([]string, len(path))
	for i, heading := range path {
		parts[i] = separators.Replace(heading)
	}
	return ParagraphID(fmt.Sprintf("%s#%s#%d", separators.Replace(slug), strings.Join(parts, "/"), index))
}

var separators = strings.NewReplacer("#", "-", "/", "-")

// The runbook slug before the first hash
func (id ParagraphID) Runbook() string {
	slug, _, _ := strings.Cut(string(id), "#")
	return slug
}

// The heading path between the hashes
func (id ParagraphID) Section() string {
	_, rest, _ := strings.Cut(string(id), "#")
	path, _, _ := strings.Cut(rest, "#")
	return path
}

// A section below the title that is not Decide
func (id ParagraphID) IsStep() bool {
	return strings.Contains(id.Section(), "/") && !id.IsDecide()
}

// The section a runbook ends with
// It states the decision and never a cause
func (id ParagraphID) IsDecide() bool {
	return strings.HasSuffix(id.Section(), "/Decide")
}

type Paragraph struct {
	ID   ParagraphID
	File string
	// Heading path from H1 downward
	Path []string
	Text string
}

// Ground truth for one event
type Label struct {
	EventID string    `json:"event_id"`
	Type    EventType `json:"type"`
	// Seed events donate feedback to holdout events
	// Holdout events are scored
	Seed bool `json:"seed"`
	// Series that are truly anomalous
	Anomalies []SeriesRef `json:"anomalies"`
	// Paragraphs that justify the cause
	// Empty for a hold
	Paragraphs []ParagraphID `json:"paragraph_ids"`
	// Paragraph ids of checks a good review must include
	RequiredChecks []ParagraphID `json:"required_checks"`
	// Knowledge ids a review of this event should use
	// Empty means any applied knowledge is a misapplication
	Knowledge []string `json:"knowledge,omitempty"`
}

// The correct answer is a hold because no runbook covers the cause or the data cannot be trusted
func (l Label) IsHold() bool {
	return l.Type == TypeHold
}

type SeriesRef struct {
	Metric string            `json:"metric"`
	Dims   map[string]string `json:"dims,omitempty"`
}

// Stable key of a series inside an event
// Metric then dimensions in name order
func (s SeriesRef) Key() string {
	var b strings.Builder
	b.WriteString(s.Metric)
	for _, k := range slices.Sorted(maps.Keys(s.Dims)) {
		b.WriteString("|")
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(s.Dims[k])
	}
	return b.String()
}
