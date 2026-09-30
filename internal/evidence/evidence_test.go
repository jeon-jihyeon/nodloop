package evidence_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

// The declared constants are read from the source so a constant left out of its list fails here
func TestEnumLists(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "evidence.go", nil, 0)
	require.NoError(t, err)
	tcs := []struct {
		name string
		// Type name of the constants
		args string
		want []string
	}{
		{"contexts list every declared context", "Context", enumValues(evidence.Contexts())},
		{"statuses list every declared status", "Status", enumValues(evidence.Statuses())},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var declared []string
			for _, decl := range f.Decls {
				gen, ok := decl.(*ast.GenDecl)
				if !ok || gen.Tok != token.CONST {
					continue
				}
				for _, spec := range gen.Specs {
					vs := spec.(*ast.ValueSpec)
					if typ, ok := vs.Type.(*ast.Ident); !ok || typ.Name != tc.args {
						continue
					}
					value, err := strconv.Unquote(vs.Values[0].(*ast.BasicLit).Value)
					require.NoError(t, err)
					declared = append(declared, value)
				}
			}
			require.NotEmpty(t, declared)
			assert.ElementsMatch(t, declared, tc.want)
		})
	}
}

func enumValues[T ~string](values []T) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		out = append(out, string(v))
	}
	return out
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
		{"a named type is valid", "spike", true},
		{"empty type is invalid", "", false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Valid())
		})
	}
}

func TestStatusValid(t *testing.T) {
	tcs := []struct {
		name string
		args evidence.Status
		want bool
	}{
		{"no action is valid", evidence.StatusNoAction, true},
		{"ready for review is valid", evidence.StatusReadyForReview, true},
		{"hold is valid", evidence.StatusHold, true},
		{"empty status is invalid", "", false},
		{"unlisted status is invalid", "bogus", false},
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

func TestEventRef(t *testing.T) {
	at := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	tcs := []struct {
		name string
		args evidence.Event
		want evidence.EventRef
	}{
		{
			name: "range spans the points and each dimension lists its distinct values in name order",
			args: evidence.Event{ID: "e", Points: []evidence.Point{
				{Time: at, Dims: map[string]string{"source": "b", "topic": "t"}},
				{Time: at.Add(time.Hour), Dims: map[string]string{"source": "a", "topic": "t"}},
				{Time: at.Add(2 * time.Hour), Dims: map[string]string{"source": "b", "topic": "t"}},
			}},
			want: evidence.EventRef{
				ID: "e", Start: at, End: at.Add(2 * time.Hour),
				Dims: map[string][]string{"source": {"a", "b"}, "topic": {"t"}},
			},
		},
		{
			name: "points without dims give an empty dimension map",
			args: evidence.Event{ID: "e", Points: []evidence.Point{{Time: at}}},
			want: evidence.EventRef{ID: "e", Start: at, End: at, Dims: map[string][]string{}},
		},
		{
			name: "an event without points has a zero range instead of panicking",
			args: evidence.Event{ID: "e"},
			want: evidence.EventRef{ID: "e", Dims: map[string][]string{}},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Ref())
		})
	}
}

func TestEventRefCarries(t *testing.T) {
	sources := make([]string, 12)
	for i := range sources {
		sources[i] = fmt.Sprintf("source-%02d", i)
	}
	ref := evidence.EventRef{ID: "e", Dims: map[string][]string{"source": sources, "topic": {"shopping"}}}
	tcs := []struct {
		name string
		args map[string]string
		want bool
	}{
		{name: "a value among the first ten", args: map[string]string{"source": "source-03"}, want: true},
		{name: "a value past the first ten", args: map[string]string{"source": "source-11"}, want: true},
		{name: "a value the event lacks", args: map[string]string{"source": "source-12"}},
		{name: "a dimension the event lacks", args: map[string]string{"region": "eu"}},
		{name: "two dimensions that both hold their value", args: map[string]string{"source": "source-10", "topic": "shopping"}, want: true},
		{name: "two dimensions where one misses", args: map[string]string{"source": "source-10", "topic": "finance"}},
		{name: "no dimension named carries", args: nil, want: true},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, ref.Carries(tc.args))
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
		procedure string
		section   string
		step      bool
		decide    bool
	}
	tcs := []struct {
		name string
		args evidence.ParagraphID
		want want
	}{
		{"a section below the title is a step", "r#T/A#1", want{procedure: "r", section: "T/A", step: true}},
		{"the Decide section is not a step", "r#T/Decide#1", want{procedure: "r", section: "T/Decide", decide: true}},
		{"the title section is neither", "r#T#1", want{procedure: "r", section: "T"}},
		{"an empty id has empty parts", "", want{}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := want{tc.args.Procedure(), tc.args.Section(), tc.args.IsStep(), tc.args.IsDecide()}
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
		{"a label expecting a hold holds", evidence.Label{Type: "spike", Expected: evidence.StatusHold}, true},
		{
			"a label expecting another status does not hold",
			evidence.Label{Type: "hold", Expected: evidence.StatusNoAction},
			false,
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.IsHold())
		})
	}
}

func TestLabelValidate(t *testing.T) {
	type want struct {
		// The whole message or <nil>
		text string
		err  error
	}
	tcs := []struct {
		name string
		args evidence.Label
		want want
	}{
		{
			"a complete label is valid",
			evidence.Label{EventID: "e1", Type: "spike", Expected: evidence.StatusHold},
			want{text: "<nil>"},
		},
		{
			"a label without an event id is malformed",
			evidence.Label{Type: "spike", Expected: evidence.StatusHold},
			want{"evidence: malformed data: label without event id", evidence.ErrMalformed},
		},
		{
			"a label without a type is malformed",
			evidence.Label{EventID: "e1", Expected: evidence.StatusHold},
			want{"evidence: malformed data: label e1 without type", evidence.ErrMalformed},
		},
		{
			"a label with an unknown expected status is malformed",
			evidence.Label{EventID: "e1", Type: "spike", Expected: "maybe"},
			want{"evidence: malformed data: label e1 expects unknown status \"maybe\"", evidence.ErrMalformed},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.args.Validate()
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.text, fmt.Sprint(err))
		})
	}
}
