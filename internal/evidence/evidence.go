// Package evidence is the read only reference data behind the Evidence Loop: events and procedure paragraphs and labels
package evidence

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

// What was known to have changed around an event
// A data set declares its own in policy.yaml
type Context string

// The members of the default declaration
const (
	ContextNoKnownChange      Context = "no_known_change"
	ContextPlannedChange      Context = "planned_operational_change"
	ContextMeasurementChanged Context = "measurement_context_changed"
	ContextDataAvailability   Context = "data_availability_issue"
	ContextUnknown            Context = "unknown" // always declared because an event without a context row reads it
)

// One change context a data set declares
type DeclaredContext struct {
	Name Context `json:"name"`
	// The baseline comparison is untrusted around such a change
	BreaksBaseline bool `json:"breaks_baseline"`
}

// The change contexts of one data set in declaration order
type Contexts []DeclaredContext

// A data set that declares none gets these
// 1. a measurement or availability change leaves the baseline comparison untrusted
// 2. a planned change or an unknown context does not because the procedures read through them
func DefaultContexts() Contexts {
	return Contexts{
		{Name: ContextNoKnownChange},
		{Name: ContextPlannedChange},
		{Name: ContextMeasurementChanged, BreaksBaseline: true},
		{Name: ContextDataAvailability, BreaksBaseline: true},
		{Name: ContextUnknown},
	}
}

func (cs Contexts) Valid(c Context) bool {
	_, ok := cs.find(c)
	return ok
}

// An undeclared context never breaks because nothing said it does
func (cs Contexts) Breaks(c Context) bool {
	d, _ := cs.find(c)
	return d.BreaksBaseline
}

func (cs Contexts) find(c Context) (DeclaredContext, bool) {
	for _, d := range cs {
		if d.Name == c {
			return d, true
		}
	}
	return DeclaredContext{}, false
}

func (cs Contexts) Names() []Context {
	out := make([]Context, 0, len(cs))
	for _, d := range cs {
		out = append(out, d.Name)
	}
	return out
}

// The category a label assigns to an event
// The data set names its own categories so any non empty name is valid
type EventType string

func (t EventType) Valid() bool {
	return t != ""
}

// The review status a label expects
type Status string

const (
	StatusNoAction       Status = "no_action"        // nothing to check
	StatusReadyForReview Status = "ready_for_review" // a cause backed by procedure checks
	StatusHold           Status = "hold"             // no procedure covers the cause or the data cannot be trusted
)

var statuses = []Status{StatusNoAction, StatusReadyForReview, StatusHold}

func (s Status) Valid() bool {
	return slices.Contains(statuses, s)
}

// Every review status in a fixed order
func Statuses() []Status {
	return slices.Clone(statuses)
}

type EventRef struct {
	ID    string
	Start time.Time
	End   time.Time
	// Distinct values of each dimension in name order keyed by dimension name
	// So an event can be found by a dimension value the user names
	Dims map[string][]string
}

// Every dimension named holds its value among all the values of the event
// No dimension named carries
func (r EventRef) Carries(dims map[string]string) bool {
	for name, value := range dims {
		if !slices.Contains(r.Dims[name], value) {
			return false
		}
	}
	return true
}

// How a person names the event: its range in UTC and the values of each dimension in name order
// The event id is left out because it is a key of the data set and not something a person recalls
func (r EventRef) Name() string {
	parts := []string{r.Start.UTC().Format(time.RFC3339) + " to " + r.End.UTC().Format(time.RFC3339)}
	for _, name := range slices.Sorted(maps.Keys(r.Dims)) {
		parts = append(parts, name+"="+strings.Join(r.Dims[name], ","))
	}
	return strings.Join(parts, " ")
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

// The range the points span and the values each dimension takes
// An event without points has a zero range
func (e Event) Ref() EventRef {
	ref := EventRef{ID: e.ID, Dims: map[string][]string{}}
	if len(e.Points) > 0 {
		ref.Start, ref.End = e.Points[0].Time, e.Points[len(e.Points)-1].Time
	}
	for name, values := range e.Dims() {
		ref.Dims[name] = slices.Sorted(maps.Keys(values))
	}
	return ref
}

// Point count per series keyed by the series key
func (e Event) SeriesCounts() map[string]int {
	counts := map[string]int{}
	for _, p := range e.Points {
		counts[p.SeriesKey()]++
	}
	return counts
}

// `procedure slug#heading path#index`
// Derived from position only so editing text keeps the id
type ParagraphID string

// A hash or slash inside the slug or a heading becomes a hyphen because Procedure and Section split on them
// A heading such as an AB test with a slash stays a paragraph instead of failing the whole procedure set
func NewParagraphID(slug string, path []string, index int) ParagraphID {
	parts := make([]string, len(path))
	for i, heading := range path {
		parts[i] = separators.Replace(heading)
	}
	return ParagraphID(fmt.Sprintf("%s#%s#%d", separators.Replace(slug), strings.Join(parts, "/"), index))
}

var separators = strings.NewReplacer("#", "-", "/", "-")

// The procedure slug before the first hash
func (id ParagraphID) Procedure() string {
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

// The section a procedure ends with
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
	// Status a correct review answers with
	Expected Status `json:"expected_status"`
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

// The correct answer is a hold because no procedure covers the cause or the data cannot be trusted
func (l Label) IsHold() bool {
	return l.Expected == StatusHold
}

// A label without an event id or a type or a known expected status cannot score a review
func (l Label) Validate() error {
	switch {
	case l.EventID == "":
		return fmt.Errorf("%w: label without event id", ErrMalformed)
	case !l.Type.Valid():
		return fmt.Errorf("%w: label %s without type", ErrMalformed, l.EventID)
	case !l.Expected.Valid():
		return fmt.Errorf("%w: label %s expects unknown status %q", ErrMalformed, l.EventID, l.Expected)
	}
	return nil
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
