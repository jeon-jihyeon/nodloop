package knowledge_test

import (
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/jeon-jihyeon/nodloop/internal/evidence"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
)

func TestSetCurrent(t *testing.T) {
	a1 := knowledge.Knowledge{ID: "a", Version: 1, Status: knowledge.StatusCandidate}
	a1Approved := knowledge.Knowledge{ID: "a", Version: 1, Status: knowledge.StatusApproved}
	a1Retired := knowledge.Knowledge{ID: "a", Version: 1, Status: knowledge.StatusRetired}
	a1Superseded := knowledge.Knowledge{ID: "a", Version: 1, Status: knowledge.StatusSuperseded}
	a2 := knowledge.Knowledge{ID: "a", Version: 2, Status: knowledge.StatusCandidate}
	a2Approved := knowledge.Knowledge{ID: "a", Version: 2, Status: knowledge.StatusApproved}
	b1 := knowledge.Knowledge{ID: "b", Version: 1, Status: knowledge.StatusCandidate}
	tcs := []struct {
		name string
		args knowledge.Set
		want knowledge.Set
	}{
		{"no records give nothing", nil, knowledge.Set{}},
		{"latest record of a version decides its status", knowledge.Set{a1Approved, a1}, knowledge.Set{a1Approved}},
		{"approved version beats a newer candidate", knowledge.Set{a2, a1Approved, a1}, knowledge.Set{a1Approved}},
		{
			"newer approved version beats a superseded one",
			knowledge.Set{a2Approved, a1Superseded, a1Approved},
			knowledge.Set{a2Approved},
		},
		{
			"newer approved version beats an older approved one",
			knowledge.Set{a2Approved, a1Approved},
			knowledge.Set{a2Approved},
		},
		{"newest candidate wins among candidates", knowledge.Set{a2, a1}, knowledge.Set{a2}},
		{"candidate beats a retired version", knowledge.Set{a2, a1Retired}, knowledge.Set{a2}},
		{"retired version is never current", knowledge.Set{a1Retired, a1Approved}, knowledge.Set{}},
		{"items come sorted by id", knowledge.Set{b1, a1}, knowledge.Set{a1, b1}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Current())
		})
	}
}

func TestSetVersions(t *testing.T) {
	a1 := knowledge.Knowledge{ID: "a", Version: 1, Status: knowledge.StatusCandidate}
	a1Approved := knowledge.Knowledge{ID: "a", Version: 1, Status: knowledge.StatusApproved}
	a2 := knowledge.Knowledge{ID: "a", Version: 2, Status: knowledge.StatusCandidate}
	b1 := knowledge.Knowledge{ID: "b", Version: 1, Status: knowledge.StatusCandidate}
	tcs := []struct {
		name string
		args knowledge.Set
		want knowledge.Set
	}{
		{"no records give nothing", nil, knowledge.Set{}},
		{"the first listed record of a version stands for it", knowledge.Set{a1Approved, a1}, knowledge.Set{a1Approved}},
		{
			"a candidate beside its approved version stays",
			knowledge.Set{a2, a1Approved, a1, b1},
			knowledge.Set{a2, a1Approved, b1},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Versions())
		})
	}
}

func TestSetCovers(t *testing.T) {
	planned := knowledge.Knowledge{
		ID: "planned", Version: 1, Status: knowledge.StatusApproved,
		Scope: knowledge.Scope{Scope: evidence.Scope{ChangeContexts: []evidence.Context{evidence.ContextPlannedChange}}},
	}
	open := knowledge.Knowledge{
		ID: "open", Version: 1, Status: knowledge.StatusApproved, Exceptions: []evidence.Context{evidence.ContextUnknown},
	}
	candidate := knowledge.Knowledge{ID: "candidate", Version: 1, Status: knowledge.StatusCandidate}
	type args struct {
		set           knowledge.Set
		changeContext evidence.Context
	}
	tcs := []struct {
		name string
		args args
		want bool
	}{
		{"a scoped item covers its context", args{knowledge.Set{planned}, evidence.ContextPlannedChange}, true},
		{"a scoped item leaves another context", args{knowledge.Set{planned}, evidence.ContextNoKnownChange}, false},
		{"an unscoped item covers every context", args{knowledge.Set{open}, evidence.ContextNoKnownChange}, true},
		{"an exception leaves its context", args{knowledge.Set{open}, evidence.ContextUnknown}, false},
		{"a candidate covers nothing", args{knowledge.Set{candidate}, evidence.ContextNoKnownChange}, false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.set.Covers(tc.args.changeContext))
		})
	}
}

func TestSetApproved(t *testing.T) {
	a := knowledge.Knowledge{ID: "a", Version: 1, Status: knowledge.StatusApproved}
	b := knowledge.Knowledge{ID: "b", Version: 1, Status: knowledge.StatusCandidate}
	c := knowledge.Knowledge{ID: "c", Version: 1, Status: knowledge.StatusApproved}
	tcs := []struct {
		name string
		args knowledge.Set
		want knowledge.Set
	}{
		{"candidates only give an empty set", knowledge.Set{b}, knowledge.Set{}},
		{"current approved items come sorted by id", knowledge.Set{c, b, a}, knowledge.Set{a, c}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Approved())
		})
	}
}

func TestSetApplicable(t *testing.T) {
	approved := knowledge.Knowledge{ID: "any", Version: 1, Status: knowledge.StatusApproved}
	planned := knowledge.Knowledge{
		ID: "ctx", Version: 1, Status: knowledge.StatusApproved,
		Scope: knowledge.Scope{Scope: evidence.Scope{ChangeContexts: []evidence.Context{evidence.ContextPlannedChange}}},
	}
	metric := knowledge.Knowledge{
		ID: "metric", Version: 1, Status: knowledge.StatusApproved,
		Scope: knowledge.Scope{Scope: evidence.Scope{Metrics: []string{"conversion_count", "impressions"}}},
	}
	dim := knowledge.Knowledge{
		ID: "dim", Version: 1, Status: knowledge.StatusApproved,
		Scope: knowledge.Scope{Dims: map[string]string{"source": "source-b"}},
	}
	otherDim := knowledge.Knowledge{
		ID: "other-dim", Version: 1, Status: knowledge.StatusApproved,
		Scope: knowledge.Scope{Dims: map[string]string{"source": "source-z"}},
	}
	excepted := knowledge.Knowledge{
		ID: "excepted", Version: 1, Status: knowledge.StatusApproved,
		Exceptions: []evidence.Context{evidence.ContextPlannedChange},
	}
	candidate := knowledge.Knowledge{ID: "candidate", Version: 1, Status: knowledge.StatusCandidate}
	clicksOnB := knowledge.Knowledge{
		ID: "clicks-on-b", Version: 1, Status: knowledge.StatusApproved,
		Scope: knowledge.Scope{Scope: evidence.Scope{Metrics: []string{"click_count"}}, Dims: map[string]string{"source": "source-b"}},
	}
	onA, onB := map[string]string{"source": "source-a"}, map[string]string{"source": "source-b"}
	dims := knowledge.Dims{"source": {"source-a": {}, "source-b": {}, "source-d": {}}, "region": {"eu": {}}}
	change, unknown := evidence.ContextPlannedChange, evidence.ContextUnknown
	type ref = evidence.SeriesRef
	// Undiluted series that also name their metrics
	series := func(refs ...evidence.SeriesRef) knowledge.Moved {
		m := knowledge.Moved{Series: refs}
		for _, ref := range refs {
			if !slices.Contains(m.Metrics, ref.Metric) {
				m.Metrics = append(m.Metrics, ref.Metric)
			}
		}
		return m
	}
	// Only a diluted group of the metric moved
	diluted := knowledge.Moved{Metrics: []string{"click_count"}}
	clicks := knowledge.Knowledge{
		ID: "clicks", Version: 1, Status: knowledge.StatusApproved,
		Scope: knowledge.Scope{Scope: evidence.Scope{
			ChangeContexts: []evidence.Context{evidence.ContextNoKnownChange}, Metrics: []string{"click_count"},
		}},
	}
	clicksOnD := knowledge.Knowledge{
		ID: "clicks-on-d", Version: 1, Status: knowledge.StatusApproved,
		Scope: knowledge.Scope{Scope: evidence.Scope{Metrics: []string{"click_count"}}, Dims: map[string]string{"source": "source-d"}},
	}
	clicksInEU := knowledge.Knowledge{
		ID: "clicks-in-eu", Version: 1, Status: knowledge.StatusApproved,
		Scope: knowledge.Scope{Scope: evidence.Scope{Metrics: []string{"click_count"}}, Dims: map[string]string{"region": "eu"}},
	}
	type args struct {
		item          knowledge.Knowledge
		changeContext evidence.Context
		moved         knowledge.Moved
	}
	tcs := []struct {
		name string
		args args
		want knowledge.Set
	}{
		{"unscoped approved item applies to any event", args{approved, unknown, knowledge.Moved{}}, knowledge.Set{approved}},
		{"candidate never applies", args{candidate, unknown, knowledge.Moved{}}, knowledge.Set{}},
		{
			"moved metric matches a metric scope",
			args{metric, unknown, series(ref{Metric: "click_count"}, ref{Metric: "impressions"})},
			knowledge.Set{metric},
		},
		{
			"metric the event carries but nobody flagged does not match",
			args{metric, unknown, series(ref{Metric: "click_count"})},
			knowledge.Set{},
		},
		{
			"metric and dim value moved on one series match",
			args{clicksOnB, unknown, series(ref{Metric: "click_count", Dims: onB})},
			knowledge.Set{clicksOnB},
		},
		{
			"metric that moved on another dim value does not match although the event carries the value",
			args{clicksOnB, unknown, series(ref{Metric: "click_count", Dims: onA})},
			knowledge.Set{},
		},
		{
			"another metric that moved on the dim value does not match",
			args{clicksOnB, unknown, series(ref{Metric: "click_count", Dims: onA}, ref{Metric: "conversion_count", Dims: onB})},
			knowledge.Set{},
		},
		{
			"series whose target leaves out the dim falls back to the values the event carries",
			args{clicksOnB, unknown, series(ref{Metric: "click_count", Dims: map[string]string{"topic": "shopping"}})},
			knowledge.Set{clicksOnB},
		},
		{
			"dim value without a metric applies when nothing moved",
			args{dim, unknown, knowledge.Moved{}},
			knowledge.Set{dim},
		},
		{
			"dim value without a metric applies whatever moved on another value",
			args{dim, unknown, series(ref{Metric: "click_count", Dims: onA})},
			knowledge.Set{dim},
		},
		{
			"a metric moved only by a diluted group matches a metric scope",
			args{clicks, evidence.ContextNoKnownChange, diluted},
			knowledge.Set{clicks},
		},
		{
			"a diluted group never admits a scope on its own dim value",
			args{clicksOnD, unknown, diluted},
			knowledge.Set{},
		},
		{
			"a diluted movement admits no scope on a dim key of another dimension either",
			args{clicksInEU, unknown, diluted},
			knowledge.Set{},
		},
		{"context scope matches its change context", args{planned, change, knowledge.Moved{}}, knowledge.Set{planned}},
		{"context scope skips another change context", args{planned, unknown, knowledge.Moved{}}, knowledge.Set{}},
		{"exception skips its change context", args{excepted, change, knowledge.Moved{}}, knowledge.Set{}},
		{"exception leaves another change context", args{excepted, unknown, knowledge.Moved{}}, knowledge.Set{excepted}},
		{"dimension value the event carries matches", args{dim, unknown, knowledge.Moved{}}, knowledge.Set{dim}},
		{"dimension value the event lacks does not match", args{otherDim, unknown, knowledge.Moved{}}, knowledge.Set{}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, knowledge.Set{tc.args.item}.Applicable(tc.args.changeContext, tc.args.moved, dims))
		})
	}
}

func TestSetMatching(t *testing.T) {
	approvedAt := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	a := knowledge.Knowledge{
		ID: "a", Version: 1, Kind: knowledge.KindMeaning, Status: knowledge.StatusApproved, ApprovedAt: approvedAt,
	}
	b := knowledge.Knowledge{ID: "b", Version: 1, Kind: knowledge.KindJudgment, Status: knowledge.StatusCandidate}
	c := knowledge.Knowledge{ID: "c", Version: 1, Kind: knowledge.KindMeaning, Status: knowledge.StatusCandidate}
	items := knowledge.Set{c, b, a}
	meaning := []knowledge.Kind{knowledge.KindMeaning}
	tcs := []struct {
		name string
		args knowledge.Filter
		want knowledge.Set
	}{
		{"empty filter keeps every item in order", knowledge.Filter{}, knowledge.Set{c, b, a}},
		{"kind filter keeps items of that kind in order", knowledge.Filter{Kinds: meaning}, knowledge.Set{c, a}},
		{
			"status filter keeps items of any listed status",
			knowledge.Filter{Statuses: []knowledge.Status{knowledge.StatusApproved, knowledge.StatusRetired}},
			knowledge.Set{a},
		},
		{
			"kind and status must both match",
			knowledge.Filter{Kinds: meaning, Statuses: []knowledge.Status{knowledge.StatusCandidate}},
			knowledge.Set{c},
		},
		{
			"a stale time keeps the approved versions past their review deadline",
			knowledge.Filter{StaleAt: approvedAt.AddDate(0, 0, knowledge.ReviewDays)},
			knowledge.Set{a},
		},
		{
			"a stale time before the deadline keeps nothing",
			knowledge.Filter{StaleAt: approvedAt.AddDate(0, 0, knowledge.ReviewDays-1)},
			knowledge.Set{},
		},
		{
			"filter nothing matches gives an empty set",
			knowledge.Filter{Statuses: []knowledge.Status{knowledge.StatusRetired}},
			knowledge.Set{},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, items.Matching(tc.args))
		})
	}
}

func TestSetOverlaps(t *testing.T) {
	xy := knowledge.Knowledge{
		ID: "xy", Version: 1, Kind: knowledge.KindMeaning, Status: knowledge.StatusCandidate,
		Scope: knowledge.Scope{Scope: evidence.Scope{ChangeContexts: []evidence.Context{"x", "y"}, Metrics: []string{"m"}}},
	}
	y := knowledge.Knowledge{
		ID: "y", Version: 1, Kind: knowledge.KindMeaning, Status: knowledge.StatusCandidate,
		Scope: knowledge.Scope{Scope: evidence.Scope{ChangeContexts: []evidence.Context{"y"}}},
	}
	z := knowledge.Knowledge{
		ID: "z", Version: 1, Kind: knowledge.KindMeaning, Status: knowledge.StatusCandidate,
		Scope: knowledge.Scope{Scope: evidence.Scope{ChangeContexts: []evidence.Context{"z"}}},
	}
	n := knowledge.Knowledge{
		ID: "n", Version: 1, Kind: knowledge.KindMeaning, Status: knowledge.StatusCandidate,
		Scope: knowledge.Scope{Scope: evidence.Scope{Metrics: []string{"n"}}},
	}
	judgment := knowledge.Knowledge{
		ID: "judgment", Version: 1, Kind: knowledge.KindJudgment, Status: knowledge.StatusCandidate,
	}
	sourceA := knowledge.Knowledge{
		ID: "source-a", Version: 1, Kind: knowledge.KindJudgment, Status: knowledge.StatusCandidate,
		Scope: knowledge.Scope{Dims: map[string]string{"source": "a"}},
	}
	items := knowledge.Set{xy, y, z, n, judgment, sourceA}
	type args struct {
		id    string
		kind  knowledge.Kind
		scope knowledge.Scope
	}
	tcs := []struct {
		name string
		args args
		want knowledge.Set
	}{
		{"shared context and an unset metric axis overlap", args{xy.ID, xy.Kind, xy.Scope}, knowledge.Set{y}},
		{
			"unset axes overlap every item of the same kind",
			args{"new", knowledge.KindMeaning, knowledge.Scope{}},
			knowledge.Set{n, xy, y, z},
		},
		{
			"other kind never overlaps",
			args{"new", knowledge.KindJudgment, knowledge.Scope{Scope: evidence.Scope{Metrics: []string{"m"}}}},
			knowledge.Set{judgment, sourceA},
		},
		{
			"same dimension value overlaps",
			args{"new", knowledge.KindJudgment, knowledge.Scope{Dims: map[string]string{"source": "a"}}},
			knowledge.Set{judgment, sourceA},
		},
		{
			"other value of the same dimension does not overlap",
			args{"new", knowledge.KindJudgment, knowledge.Scope{Dims: map[string]string{"source": "b"}}},
			knowledge.Set{judgment},
		},
		{
			"dimension set on one side only overlaps",
			args{"new", knowledge.KindJudgment, knowledge.Scope{Dims: map[string]string{"app": "x"}}},
			knowledge.Set{judgment, sourceA},
		},
		{
			"disjoint contexts overlap only items without contexts",
			args{"new", knowledge.KindMeaning, knowledge.Scope{Scope: evidence.Scope{ChangeContexts: []evidence.Context{"w"}}}},
			knowledge.Set{n},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, items.Overlaps(tc.args.id, tc.args.kind, tc.args.scope))
		})
	}
}

func TestSetFind(t *testing.T) {
	t.Parallel()
	set := knowledge.Set{{ID: "k-a", Version: 2}, {ID: "k-b", Version: 1}, {ID: "k-a", Version: 1}}
	type want struct {
		found   bool
		version int
	}
	tcs := []struct {
		name string
		args string
		want want
	}{
		{"the first listed record of the id is found", "k-a", want{true, 2}},
		{"an unknown id is not found", "k-z", want{false, 0}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			k, found := set.Find(tc.args)
			assert.Equal(t, tc.want, want{found, k.Version})
		})
	}
}

func TestSetLineage(t *testing.T) {
	t.Parallel()
	ref := func(id string, version int) knowledge.Ref { return knowledge.Ref{ID: id, Version: version} }
	set := knowledge.Set{
		{ID: "k-c", Version: 2, Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{"fb-1"}}},
		{ID: "k-d", Version: 1, Evidence: knowledge.Evidence{Knowledge: []knowledge.Ref{ref("k-c", 1), ref("k-e", 1)}}},
		{ID: "k-a", Version: 2, Evidence: knowledge.Evidence{Knowledge: []knowledge.Ref{ref("k-a", 1), ref("k-b", 1)}}},
		{ID: "k-c", Version: 1, Evidence: knowledge.Evidence{Knowledge: []knowledge.Ref{ref("k-a", 1), ref("k-b", 1)}}},
		{ID: "k-e", Version: 1},
		{ID: "k-b", Version: 1},
		{ID: "k-a", Version: 1},
	}
	tcs := []struct {
		name string
		args knowledge.Ref
		want []string
	}{
		{"a plain item stands for itself", ref("k-e", 1), []string{"k-e"}},
		{"a compacted item stands for the items it replaced", ref("k-c", 1), []string{"k-c", "k-a", "k-b"}},
		{"a chained compaction reaches every ancestor", ref("k-d", 1), []string{"k-d", "k-c", "k-e", "k-a", "k-b"}},
		{"a revised compacted item keeps the items its earlier version replaced", ref("k-c", 2), []string{"k-c", "k-a", "k-b"}},
		{"a draft that reuses an old id ends", ref("k-a", 2), []string{"k-a", "k-b"}},
		{"an unknown version stands for its own id", ref("k-z", 3), []string{"k-z"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, set.Lineage(tc.args))
		})
	}
}
