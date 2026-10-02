package knowledge_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
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

// k-c v1 compacts k-a and k-b and k-d v1 compacts k-c and k-e
// k-a v2 reuses its id to replace k-a v1 and k-b v1
// k-c v2 revises the compacted k-c v1 with evidence of its own
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

// k-c v1 compacts k-a v1 and k-b v1 and k-d v1 compacts k-c v1
// k-e v1 cites k-a v1 as evidence without a compaction
// k-f v1 compacts k-a v1 and narrows it to commit runs
func TestSetInherits(t *testing.T) {
	ref := func(id string, version int) knowledge.Ref { return knowledge.Ref{ID: id, Version: version} }
	repo := &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"nodloop"}}}
	set := knowledge.Set{
		{ID: "k-a", Version: 1, Run: repo},
		{ID: "k-b", Version: 1, Run: repo},
		{
			ID: "k-c", Version: 1, Run: repo, Compaction: "c-1",
			Evidence: knowledge.Evidence{Knowledge: []knowledge.Ref{ref("k-a", 1), ref("k-b", 1)}, OutcomeTraceIDs: []string{"cited"}},
		},
		{ID: "k-d", Version: 1, Run: repo, Compaction: "c-2", Evidence: knowledge.Evidence{Knowledge: []knowledge.Ref{ref("k-c", 1)}}},
		{ID: "k-e", Version: 1, Run: repo, Evidence: knowledge.Evidence{Knowledge: []knowledge.Ref{ref("k-a", 1)}}},
		{
			ID: "k-f", Version: 1, Compaction: "c-3", Evidence: knowledge.Evidence{Knowledge: []knowledge.Ref{ref("k-a", 1)}},
			Run: &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"nodloop"}, "task": {"commit"}}},
		},
		{ID: "k-g", Version: 1, Compaction: "c-4", Evidence: knowledge.Evidence{Knowledge: []knowledge.Ref{ref("k-a", 1)}}},
	}
	type args struct {
		ref      knowledge.Ref
		traceID  string
		applied  []knowledge.Ref
		producer string
		labels   trace.Labels
	}
	inRepo := trace.Labels{"repo": {"nodloop"}}
	tcs := []struct {
		name string
		args args
		want bool
	}{
		{"a run of a merged version passes to the compaction", args{ref("k-c", 1), "r", []knowledge.Ref{ref("k-a", 1)}, "session", inRepo}, true},
		{"a run of a version merged two compactions back passes on", args{ref("k-d", 1), "r", []knowledge.Ref{ref("k-b", 1)}, "session", inRepo}, true},
		{
			"a run that applied the version itself is its own",
			args{ref("k-c", 1), "r", []knowledge.Ref{ref("k-a", 1), ref("k-c", 1)}, "session", inRepo},
			false,
		},
		{"a run the version cites is answered", args{ref("k-c", 1), "cited", []knowledge.Ref{ref("k-a", 1)}, "session", inRepo}, false},
		{"a run a merged version cites is answered", args{ref("k-d", 1), "cited", []knowledge.Ref{ref("k-a", 1)}, "session", inRepo}, false},
		{
			"a run of a label value the version no longer reaches stays behind",
			args{ref("k-c", 1), "r", []knowledge.Ref{ref("k-a", 1)}, "session", trace.Labels{"repo": {"other"}}},
			false,
		},
		{"a run of another producer stays behind", args{ref("k-c", 1), "r", []knowledge.Ref{ref("k-a", 1)}, "ci", inRepo}, false},
		{"evidence without a compaction merges nothing", args{ref("k-e", 1), "r", []knowledge.Ref{ref("k-a", 1)}, "session", inRepo}, false},
		{"an unknown version inherits nothing", args{ref("k-z", 1), "r", []knowledge.Ref{ref("k-a", 1)}, "session", inRepo}, false},
		{"a version without a run scope inherits nothing", args{ref("k-g", 1), "r", []knowledge.Ref{ref("k-a", 1)}, "session", inRepo}, false},
		{
			"a run that carries the label a version was narrowed by passes to it",
			args{ref("k-f", 1), "r", []knowledge.Ref{ref("k-a", 1)}, "session", trace.Labels{"repo": {"nodloop"}, "task": {"commit", "push"}}},
			true,
		},
		{
			"a run of another value of the label a version was narrowed by stays behind",
			args{ref("k-f", 1), "r", []knowledge.Ref{ref("k-a", 1)}, "session", trace.Labels{"repo": {"nodloop"}, "task": {"docs"}}},
			false,
		},
		{"a run without the label a version was narrowed by stays behind", args{ref("k-f", 1), "r", []knowledge.Ref{ref("k-a", 1)}, "session", inRepo}, false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, set.Inherits(tc.args.ref, tc.args.traceID, tc.args.applied, tc.args.producer, tc.args.labels))
		})
	}
}
