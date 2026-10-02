package knowledge_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

func TestFolderSize(t *testing.T) {
	t.Parallel()
	tcs := []struct {
		name string
		args knowledge.Set
		want int
	}{
		{"a folder of the item alone holds one item", knowledge.Set{}, 1},
		{"a folder counts the item beside its other items", knowledge.Set{{ID: "k-a"}, {ID: "k-b"}}, 3},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, knowledge.Folder{Carried: tc.args}.Size())
		})
	}
}

func TestFolderFull(t *testing.T) {
	t.Parallel()
	tcs := []struct {
		name string
		args knowledge.Folder
		want bool
	}{
		{"a folder under the review chars is not full", knowledge.Folder{Chars: knowledge.RunChars - 1}, false},
		{"a folder at the review chars is not full", knowledge.Folder{Chars: knowledge.RunChars}, false},
		{"a folder over the review chars is full", knowledge.Folder{Chars: knowledge.RunChars + 1}, true},
		{"a folder at the review items is not full", knowledge.Folder{Carried: make(knowledge.Set, knowledge.RunItems-1)}, false},
		{
			"a folder over the review items is full though its text fits",
			knowledge.Folder{Chars: 1, Carried: make(knowledge.Set, knowledge.RunItems)}, true,
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Full())
		})
	}
}

func TestFolderCrowded(t *testing.T) {
	t.Parallel()
	tcs := []struct {
		name string
		args int
		want bool
	}{
		{"a folder no compaction can anchor is not crowded", 0, false},
		{"five items a compaction would cover are not crowded", 5, false},
		{"six items a compaction would cover are crowded", 6, true},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, knowledge.Folder{Compactable: tc.args}.Crowded())
		})
	}
}

func TestFolderString(t *testing.T) {
	t.Parallel()
	run := &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"nodloop"}}}
	tcs := []struct {
		name string
		args knowledge.Folder
		want string
	}{
		{"a folder of the item alone names no other item", knowledge.Folder{Carried: knowledge.Set{}}, "no other item"},
		{
			"every other item comes with its version and the runes of its text",
			knowledge.Folder{Carried: knowledge.Set{
				{ID: "k-a", Version: 1, Kind: knowledge.KindMeaning, Content: "one", Run: run},
				{ID: "k-b", Version: 2, Kind: knowledge.KindJudgment, Content: "두 개 🙂", Run: run},
			}},
			"k-a v1 59 chars, k-b v2 62 chars",
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.String())
		})
	}
}
