package knowledge_test

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

func TestLedgerCapacityOverlappingAlternatives(t *testing.T) {
	t.Parallel()
	l, ctx := runLedger(t), context.Background()
	for i, values := range [][]string{{"x", "y"}, {"y"}, {"x", "z"}, {"z"}} {
		k := runItem(fmt.Sprintf("acme-%c", 'a'+i), &knowledge.RunScope{
			Producer: "session", Labels: trace.Labels{"context": values},
		})
		k.Content = strings.Repeat("x", []int{4000, 1, 4000, 1}[i])
		_, _, err := l.Propose(ctx, k)
		require.NoError(t, err)
		require.NoError(t, testkit.Err(l.Approve(ctx, k.ID, 1, "ann")))
	}
	extra := runItem("acme-extra", &knowledge.RunScope{Producer: "session"})
	extra.Content = strings.Repeat("x", 1500)
	_, _, err := l.Propose(ctx, extra)
	require.NoError(t, err)
	all, err := l.All(ctx)
	require.NoError(t, err)
	folder, fits, err := all.Fits(extra.ID, 1)
	require.NoError(t, err)
	assert.False(t, fits)
	assert.Equal(t, []knowledge.Ref{{ID: "acme-a", Version: 1}, {ID: "acme-c", Version: 1}}, folder.Carried.Refs())
	_, err = l.Approve(ctx, extra.ID, 1, "ann")
	assert.ErrorIs(t, err, knowledge.ErrFolderFull)
	all, err = l.All(ctx)
	require.NoError(t, err)
	at := time.Date(2026, 10, 2, 1, 0, 0, 0, time.UTC)
	assert.LessOrEqual(t, textRunes(all.ForAt("session", trace.Labels{"context": {"x"}}, at)), knowledge.RunChars)
}

func TestSetCapacityMatchesForAt(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	random := rand.New(rand.NewPCG(23, 7))
	for sample := range 256 {
		var all knowledge.Set
		for i := range 9 {
			scope := &knowledge.RunScope{Producer: "session", Labels: trace.Labels{}}
			for _, key := range []string{"context", "task"} {
				mask := random.IntN(8)
				for bit, value := range []string{"x", "y", "z"} {
					if mask&(1<<bit) != 0 {
						scope.Labels[key] = append(scope.Labels[key], value)
					}
				}
			}
			if random.IntN(2) == 0 {
				scope.ValidFrom = at.Add(time.Hour)
			}
			if random.IntN(2) == 0 {
				scope.ValidUntil = at.Add(2 * time.Hour)
			}
			k := runItem(fmt.Sprintf("acme-%d", i), scope)
			k.Version, k.Status, k.Time, k.ApprovedAt = 1, knowledge.StatusApproved, at, at
			k.Content = strings.Repeat("x", random.IntN(2500)+1)
			all = append(all, k)
		}
		candidate := all[len(all)-1]
		before := slices.Clone(all[:len(all)-1])
		wantChars, wantFits := enumeratedCapacity(before, candidate, at)
		candidate.Status, candidate.ApprovedAt = knowledge.StatusCandidate, time.Time{}
		all[len(all)-1] = candidate
		random.Shuffle(len(all), func(i, j int) { all[i], all[j] = all[j], all[i] })
		folder, fits, err := all.Fits(candidate.ID, candidate.Version)
		require.NoError(t, err)
		assert.Equal(t, wantChars, folder.Chars, "sample %d", sample)
		assert.Equal(t, wantFits, fits, "sample %d", sample)
		slices.Reverse(all)
		reversed, reversedFits, err := all.Fits(candidate.ID, candidate.Version)
		require.NoError(t, err)
		assert.Equal(t, folder, reversed, "input order in sample %d", sample)
		assert.Equal(t, fits, reversedFits, "input order in sample %d", sample)
	}
}

func enumeratedCapacity(before knowledge.Set, candidate knowledge.Knowledge, at time.Time) (int, bool) {
	after := append(slices.Clone(before), candidate)
	best, failed := 0, 0
	for _, offset := range []time.Duration{0, time.Hour, 2 * time.Hour} {
		now := at.Add(offset)
		if !candidate.Run.At(now) {
			continue
		}
		for _, contextValue := range []string{"", "x", "y", "z"} {
			for _, task := range []string{"", "x", "y", "z"} {
				labels := trace.Labels{}
				if contextValue != "" {
					labels["context"] = []string{contextValue}
				}
				if task != "" {
					labels["task"] = []string{task}
				}
				if candidate.Run.Admits("session", labels) {
					previous := textRunes(before.ForAt("session", labels, now))
					next := textRunes(after.ForAt("session", labels, now))
					best = max(best, next)
					if next > knowledge.RunChars && next > previous {
						failed = max(failed, next)
					}
				}
			}
		}
	}
	if failed != 0 {
		return failed, false
	}
	return best, true
}

func textRunes(items knowledge.Set) int {
	chars := 0
	for _, k := range items {
		chars += utf8.RuneCountInString(k.Text())
	}
	return chars
}

func TestSetCapacityExceptConservative(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	a := runItem("acme-a", &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"context": {"x"}}})
	a.Content = strings.Repeat("x", 6000)
	a.Version, a.Status, a.Time, a.ApprovedAt = 1, knowledge.StatusApproved, at, at
	b := runItem("acme-b", &knowledge.RunScope{Producer: "session", Except: trace.Labels{"context": {"x"}}})
	b.Content = strings.Repeat("x", 4000)
	b.Version, b.Status, b.Time = 1, knowledge.StatusCandidate, at
	folder, fits, err := (knowledge.Set{a, b}).Fits(b.ID, b.Version)
	require.NoError(t, err)
	assert.False(t, fits)
	assert.Greater(t, folder.Chars, knowledge.RunChars)
	b.Status, b.ApprovedAt = knowledge.StatusApproved, at
	for _, labels := range []trace.Labels{nil, {"context": {"x"}}, {"context": {"y"}}} {
		assert.LessOrEqual(t, textRunes((knowledge.Set{a, b}).ForAt("session", labels, at)), folder.Chars)
	}
}

func TestSetCapacityTie(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	var all knowledge.Set
	for i, value := range []string{"x", "y", "y"} {
		k := runItem(fmt.Sprintf("acme-%c", 'a'+i), &knowledge.RunScope{
			Producer: "session", Labels: trace.Labels{"context": {value}},
		})
		k.Version, k.Status, k.Time, k.ApprovedAt = 1, knowledge.StatusApproved, at, at
		k.Content = ""
		k.Content = strings.Repeat("x", []int{1000, 500, 500}[i]-utf8.RuneCountInString(k.Text()))
		all = append(all, k)
	}
	extra := runItem("acme-extra", &knowledge.RunScope{Producer: "session"})
	extra.Version, extra.Status, extra.Time = 1, knowledge.StatusCandidate, at
	all = append(all, extra)
	for range 2 {
		folder, fits, err := all.Fits(extra.ID, extra.Version)
		require.NoError(t, err)
		assert.True(t, fits)
		assert.Equal(t, []knowledge.Ref{{ID: "acme-b", Version: 1}, {ID: "acme-c", Version: 1}}, folder.Carried.Refs())
		slices.Reverse(all)
	}
}

func capacityGroups(n, groups int) knowledge.Set {
	at := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	var all knowledge.Set
	for i := range n {
		k := runItem(fmt.Sprintf("acme-%04d", i), &knowledge.RunScope{
			Producer: "session", Labels: trace.Labels{"context": {fmt.Sprintf("acme-%04d", i%groups)}},
		})
		k.Version, k.Status, k.Time, k.ApprovedAt = 1, knowledge.StatusApproved, at, at
		all = append(all, k)
	}
	extra := runItem("acme-extra", &knowledge.RunScope{Producer: "session"})
	extra.Version, extra.Status, extra.Time = 1, knowledge.StatusCandidate, at
	return append(all, extra)
}

func TestSetCapacityLargeGroups(t *testing.T) {
	for _, groups := range []int{1, 64, 512} {
		t.Run(fmt.Sprintf("groups-%d", groups), func(t *testing.T) {
			t.Parallel()
			all := capacityGroups(512, groups)
			folder, fits, err := all.Fits("acme-extra", 1)
			require.NoError(t, err)
			assert.Len(t, folder.Carried, 512/groups)
			assert.Equal(t, folder.Chars <= knowledge.RunChars, fits)
		})
	}
}

func TestSetCapacityIndependentLabels(t *testing.T) {
	tcs := []struct {
		name  string
		chars int
		want  bool
	}{
		{"pointwise decrease", 99, true},
		{"pointwise increase", 101, false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			at := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
			all := knowledge.Set{}
			for i := range 512 {
				k := runItem(fmt.Sprintf("acme-%04d", i), &knowledge.RunScope{Producer: "session",
					Labels: trace.Labels{fmt.Sprintf("context-%04d", i): {"x"}}})
				k.Content, k.Version, k.Status = strings.Repeat("x", 100), 1, knowledge.StatusApproved
				k.Time, k.ApprovedAt = at, at
				all = append(all, k)
			}
			next := all[0]
			next.Content, next.Version, next.Status = strings.Repeat("x", tc.chars), 2, knowledge.StatusCandidate
			next.ApprovedAt = time.Time{}
			all = append(knowledge.Set{next}, all...)
			folder, fits, err := all.Fits(next.ID, next.Version)
			require.NoError(t, err)
			assert.Equal(t, tc.want, fits)
			assert.Len(t, folder.Carried, 511)
		})
	}
}

func BenchmarkSetCapacityLabels(b *testing.B) {
	for _, groups := range []int{1, 64, 512} {
		b.Run(fmt.Sprintf("groups-%d", groups), func(b *testing.B) {
			all := capacityGroups(512, groups)
			b.ReportAllocs()
			for b.Loop() {
				folder, _, err := all.Fits("acme-extra", 1)
				if err != nil || len(folder.Carried) != 512/groups {
					b.Fatalf("folder size %d and error %v", len(folder.Carried), err)
				}
			}
		})
	}
}

func BenchmarkSetCapacityMixedOverfull(b *testing.B) {
	all := capacityGroups(512, 64)
	for i := range len(all) - 1 {
		all[i].Content = strings.Repeat("x", 1300)
	}
	b.ReportAllocs()
	for b.Loop() {
		folder, fits, err := all.Fits("acme-extra", 1)
		if err != nil || fits || folder.Chars <= knowledge.RunChars {
			b.Fatalf("capacity accepted overfull growth: fits %t and error %v", fits, err)
		}
	}
}
