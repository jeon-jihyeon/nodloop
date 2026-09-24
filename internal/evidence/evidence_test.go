package evidence_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/jeon-jihyeon/nodloop/internal/evidence"
)

func TestContextValid(t *testing.T) {
	tcs := []struct {
		name string
		args evidence.Context
		want bool
	}{
		{"no known change is valid", evidence.ContextNoKnownChange, true},
		{"planned change is valid", evidence.ContextPlannedChange, true},
		{"measurement change is valid", evidence.ContextMeasurementChanged, true},
		{"data availability issue is valid", evidence.ContextDataAvailability, true},
		{"unknown is valid", evidence.ContextUnknown, true},
		{"empty context is invalid", "", false},
		{"unlisted context is invalid", "bogus", false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Valid())
		})
	}
}

func TestContextBreaks(t *testing.T) {
	tcs := []struct {
		name string
		args evidence.Context
		want bool
	}{
		{"measurement change breaks the comparison", evidence.ContextMeasurementChanged, true},
		{"data availability issue breaks the comparison", evidence.ContextDataAvailability, true},
		{"planned change keeps the comparison", evidence.ContextPlannedChange, false},
		{"no known change keeps the comparison", evidence.ContextNoKnownChange, false},
		{"unknown context keeps the comparison", evidence.ContextUnknown, false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Breaks())
		})
	}
}

func TestEventTypeValid(t *testing.T) {
	tcs := []struct {
		name string
		args evidence.EventType
		want bool
	}{
		{"normal variation is valid", evidence.TypeNormalVariation, true},
		{"click spike is valid", evidence.TypeClickSpike, true},
		{"conversion rate drop is valid", evidence.TypeConversionRateDrop, true},
		{"source concentration is valid", evidence.TypeSourceConcentration, true},
		{"attribution lag is valid", evidence.TypeAttributionLag, true},
		{"hold is valid", evidence.TypeHold, true},
		{"empty type is invalid", "", false},
		{"unlisted type is invalid", "bogus", false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Valid())
		})
	}
}

func TestSeriesRefKey(t *testing.T) {
	tcs := []struct {
		name string
		args evidence.SeriesRef
		want string
	}{
		{
			name: "dims are sorted by name",
			args: evidence.SeriesRef{Metric: "clicks", Dims: map[string]string{"topic": "t", "source": "a"}},
			want: "clicks|source=a|topic=t",
		},
		{
			name: "dims in another order give the same key",
			args: evidence.SeriesRef{Metric: "clicks", Dims: map[string]string{"source": "a", "topic": "t"}},
			want: "clicks|source=a|topic=t",
		},
		{name: "no dims give the bare metric", args: evidence.SeriesRef{Metric: "clicks"}, want: "clicks"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Key())
		})
	}
}

func TestPointSeriesKey(t *testing.T) {
	tcs := []struct {
		name string
		args evidence.Point
		want string
	}{
		{
			name: "a point keys by its metric and dims",
			args: evidence.Point{Metric: "clicks", Value: 3, Dims: map[string]string{"topic": "t", "source": "a"}},
			want: "clicks|source=a|topic=t",
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.SeriesKey())
		})
	}
}

func TestPointCompare(t *testing.T) {
	at := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	type args struct {
		p, q evidence.Point
	}
	tcs := []struct {
		name string
		args args
		want int
	}{
		{"an earlier point sorts first", args{evidence.Point{Time: at}, evidence.Point{Time: at.Add(time.Hour)}}, -1},
		{"the same time is equal whatever the value", args{evidence.Point{Time: at, Value: 1}, evidence.Point{Time: at}}, 0},
		{"a later point sorts last", args{evidence.Point{Time: at.Add(time.Hour)}, evidence.Point{Time: at}}, 1},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.p.Compare(tc.args.q))
		})
	}
}

func TestEventDims(t *testing.T) {
	tcs := []struct {
		name string
		args evidence.Event
		want map[string]map[string]struct{}
	}{
		{
			name: "values of every point are collected per dimension",
			args: evidence.Event{Points: []evidence.Point{
				{Dims: map[string]string{"source": "a", "topic": "t"}},
				{Dims: map[string]string{"source": "b", "topic": "t"}},
			}},
			want: map[string]map[string]struct{}{"source": {"a": {}, "b": {}}, "topic": {"t": {}}},
		},
		{
			name: "points without dims give no dimension",
			args: evidence.Event{Points: []evidence.Point{{Metric: "clicks"}}},
			want: map[string]map[string]struct{}{},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Dims())
		})
	}
}

func TestEventSeriesCounts(t *testing.T) {
	tcs := []struct {
		name string
		args evidence.Event
		want map[string]int
	}{
		{
			name: "points are counted per metric and dimension values",
			args: evidence.Event{Points: []evidence.Point{
				{Metric: "clicks", Dims: map[string]string{"source": "a"}},
				{Metric: "clicks", Dims: map[string]string{"source": "a"}},
				{Metric: "clicks", Dims: map[string]string{"source": "b"}},
				{Metric: "impressions"},
			}},
			want: map[string]int{"clicks|source=a": 2, "clicks|source=b": 1, "impressions": 1},
		},
		{name: "event without points has no series", args: evidence.Event{}, want: map[string]int{}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.SeriesCounts())
		})
	}
}

func TestNewParagraphID(t *testing.T) {
	type args struct {
		slug  string
		path  []string
		index int
	}
	tcs := []struct {
		name string
		args args
		want evidence.ParagraphID
	}{
		{"slug and path and index join with hashes", args{"r", []string{"T", "A"}, 2}, "r#T/A#2"},
		{"an empty path gives an empty section", args{"r", nil, 1}, "r##1"},
		{"a hash in the slug becomes a hyphen", args{"r#1", []string{"T"}, 1}, "r-1#T#1"},
		{"a slash in a heading becomes a hyphen", args{"r", []string{"T", "A/B test"}, 1}, "r#T/A-B test#1"},
		{"a hash in a heading becomes a hyphen", args{"r", []string{"T#1"}, 1}, "r#T-1#1"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := evidence.NewParagraphID(tc.args.slug, tc.args.path, tc.args.index)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestParagraphIDParts(t *testing.T) {
	type want struct {
		runbook string
		section string
		step    bool
		decide  bool
	}
	tcs := []struct {
		name string
		args evidence.ParagraphID
		want want
	}{
		{"a section below the title is a step", "r#T/A#1", want{runbook: "r", section: "T/A", step: true}},
		{"the Decide section is not a step", "r#T/Decide#1", want{runbook: "r", section: "T/Decide", decide: true}},
		{"the title section is neither", "r#T#1", want{runbook: "r", section: "T"}},
		{"an empty id has empty parts", "", want{}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := want{tc.args.Runbook(), tc.args.Section(), tc.args.IsStep(), tc.args.IsDecide()}
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestLabelIsHold(t *testing.T) {
	tcs := []struct {
		name string
		args evidence.Label
		want bool
	}{
		{"a hold label holds", evidence.Label{Type: evidence.TypeHold}, true},
		{"any other type does not hold", evidence.Label{Type: evidence.TypeClickSpike}, false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.IsHold())
		})
	}
}
