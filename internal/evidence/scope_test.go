package evidence_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jeon-jihyeon/nodloop/internal/evidence"
)

func TestScopeMatches(t *testing.T) {
	type args struct {
		scope         evidence.Scope
		changeContext evidence.Context
		metrics       []string
	}
	planned := []evidence.Context{evidence.ContextPlannedChange}
	tcs := []struct {
		name string
		args args
		want bool
	}{
		{"an empty scope matches every event", args{evidence.Scope{}, evidence.ContextUnknown, nil}, true},
		{"a context scope matches its context", args{evidence.Scope{ChangeContexts: planned}, evidence.ContextPlannedChange, nil}, true},
		{"a context scope misses another context", args{evidence.Scope{ChangeContexts: planned}, evidence.ContextNoKnownChange, nil}, false},
		{"a metric scope matches one shared metric", args{evidence.Scope{Metrics: []string{"m", "n"}}, "", []string{"x", "n"}}, true},
		{"a metric scope misses an event that observes none of them", args{evidence.Scope{Metrics: []string{"m"}}, "", []string{"x"}}, false},
		{"a metric scope misses an event that observes nothing", args{evidence.Scope{Metrics: []string{"m"}}, "", nil}, false},
		{
			"both axes must match",
			args{evidence.Scope{ChangeContexts: planned, Metrics: []string{"m"}}, evidence.ContextNoKnownChange, []string{"m"}},
			false,
		},
		{
			"both axes matching match",
			args{evidence.Scope{ChangeContexts: planned, Metrics: []string{"m"}}, evidence.ContextPlannedChange, []string{"m"}},
			true,
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.scope.Matches(tc.args.changeContext, tc.args.metrics))
		})
	}
}

func TestScopeIntersects(t *testing.T) {
	type args struct {
		ours, theirs evidence.Scope
	}
	planned := []evidence.Context{evidence.ContextPlannedChange}
	measured := []evidence.Context{evidence.ContextMeasurementChanged}
	tcs := []struct {
		name string
		args args
		want bool
	}{
		{"two empty scopes intersect", args{evidence.Scope{}, evidence.Scope{}}, true},
		{"an empty side intersects a set one", args{evidence.Scope{}, evidence.Scope{ChangeContexts: planned, Metrics: []string{"m"}}}, true},
		{"shared contexts intersect", args{evidence.Scope{ChangeContexts: planned}, evidence.Scope{ChangeContexts: planned}}, true},
		{"disjoint contexts do not intersect", args{evidence.Scope{ChangeContexts: planned}, evidence.Scope{ChangeContexts: measured}}, false},
		{"disjoint metrics do not intersect", args{evidence.Scope{Metrics: []string{"m"}}, evidence.Scope{Metrics: []string{"n"}}}, false},
		{
			"shared contexts with disjoint metrics do not intersect",
			args{evidence.Scope{ChangeContexts: planned, Metrics: []string{"m"}}, evidence.Scope{ChangeContexts: planned, Metrics: []string{"n"}}},
			false,
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.ours.Intersects(tc.args.theirs))
		})
	}
}

func TestScopeString(t *testing.T) {
	tcs := []struct {
		name string
		args evidence.Scope
		want string
	}{
		{"an empty scope reads as any event", evidence.Scope{}, "any event"},
		{
			"contexts and metrics are joined with or",
			evidence.Scope{ChangeContexts: []evidence.Context{"a", "b"}, Metrics: []string{"m", "n"}},
			"change contexts a or b. metrics m or n",
		},
		{"metrics alone", evidence.Scope{Metrics: []string{"m"}}, "metrics m"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.String())
			assert.Equal(t, tc.want == "any event", tc.args.Empty())
		})
	}
}

func TestProceduresApplicable(t *testing.T) {
	type args struct {
		changeContext evidence.Context
		observed      []string
	}
	type want struct {
		slugs      []string
		paragraphs []evidence.ParagraphID
	}
	procedures := evidence.Procedures{
		{Slug: "a", Paragraphs: []evidence.Paragraph{{ID: "a#A#1"}, {ID: "a#A#2"}}},
		{Slug: "b", Scope: evidence.Scope{Metrics: []string{"clicks"}}, Paragraphs: []evidence.Paragraph{{ID: "b#B#1"}}},
		{
			Slug:       "c",
			Scope:      evidence.Scope{ChangeContexts: []evidence.Context{evidence.ContextPlannedChange}},
			Paragraphs: []evidence.Paragraph{{ID: "c#C#1"}},
		},
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"an event that observes nothing keeps only the unscoped procedure",
			args{evidence.ContextNoKnownChange, nil},
			want{[]string{"a"}, []evidence.ParagraphID{"a#A#1", "a#A#2"}},
		},
		{
			"scoped procedures that fit are kept in their order",
			args{evidence.ContextPlannedChange, []string{"clicks"}},
			want{[]string{"a", "b", "c"}, []evidence.ParagraphID{"a#A#1", "a#A#2", "b#B#1", "c#C#1"}},
		},
		{
			"a metric scope follows the observed metrics",
			args{evidence.ContextNoKnownChange, []string{"conversions", "clicks"}},
			want{[]string{"a", "b"}, []evidence.ParagraphID{"a#A#1", "a#A#2", "b#B#1"}},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := procedures.Applicable(tc.args.changeContext, tc.args.observed)
			var ids []evidence.ParagraphID
			for _, p := range got.Paragraphs() {
				ids = append(ids, p.ID)
			}
			assert.Equal(t, tc.want.slugs, got.Slugs())
			assert.Equal(t, tc.want.paragraphs, ids)
		})
	}
}
