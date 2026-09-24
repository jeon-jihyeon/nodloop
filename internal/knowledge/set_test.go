package knowledge_test

import (
	"testing"

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
		Scope: knowledge.Scope{ChangeContexts: []evidence.Context{evidence.ContextPlannedChange}},
	}
	metric := knowledge.Knowledge{
		ID: "metric", Version: 1, Status: knowledge.StatusApproved,
		Scope: knowledge.Scope{Metrics: []string{"conversion_count", "impressions"}},
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
	dims := knowledge.Dims{"source": {"source-a": {}, "source-b": {}}}
	change, unknown := evidence.ContextPlannedChange, evidence.ContextUnknown
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
		{"unscoped approved item applies to any event", args{approved, unknown, nil}, knowledge.Set{approved}},
		{"candidate never applies", args{candidate, unknown, nil}, knowledge.Set{}},
		{
			"moved metric matches a metric scope",
			args{metric, unknown, knowledge.Moved{"click_count", "impressions"}},
			knowledge.Set{metric},
		},
		{
			"metric the event carries but nobody flagged does not match",
			args{metric, unknown, knowledge.Moved{"click_count"}},
			knowledge.Set{},
		},
		{"context scope matches its change context", args{planned, change, nil}, knowledge.Set{planned}},
		{"context scope skips another change context", args{planned, unknown, nil}, knowledge.Set{}},
		{"exception skips its change context", args{excepted, change, nil}, knowledge.Set{}},
		{"exception leaves another change context", args{excepted, unknown, nil}, knowledge.Set{excepted}},
		{"dimension value the event carries matches", args{dim, unknown, nil}, knowledge.Set{dim}},
		{"dimension value the event lacks does not match", args{otherDim, unknown, nil}, knowledge.Set{}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, knowledge.Set{tc.args.item}.Applicable(tc.args.changeContext, tc.args.moved, dims))
		})
	}
}

func TestSetMatching(t *testing.T) {
	a := knowledge.Knowledge{ID: "a", Version: 1, Kind: knowledge.KindMeaning, Status: knowledge.StatusApproved}
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
		Scope: knowledge.Scope{ChangeContexts: []evidence.Context{"x", "y"}, Metrics: []string{"m"}},
	}
	y := knowledge.Knowledge{
		ID: "y", Version: 1, Kind: knowledge.KindMeaning, Status: knowledge.StatusCandidate,
		Scope: knowledge.Scope{ChangeContexts: []evidence.Context{"y"}},
	}
	z := knowledge.Knowledge{
		ID: "z", Version: 1, Kind: knowledge.KindMeaning, Status: knowledge.StatusCandidate,
		Scope: knowledge.Scope{ChangeContexts: []evidence.Context{"z"}},
	}
	n := knowledge.Knowledge{
		ID: "n", Version: 1, Kind: knowledge.KindMeaning, Status: knowledge.StatusCandidate,
		Scope: knowledge.Scope{Metrics: []string{"n"}},
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
			args{"new", knowledge.KindJudgment, knowledge.Scope{Metrics: []string{"m"}}},
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
			args{"new", knowledge.KindMeaning, knowledge.Scope{ChangeContexts: []evidence.Context{"w"}}},
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
