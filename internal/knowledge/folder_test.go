package knowledge_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
)

func TestFolderFull(t *testing.T) {
	t.Parallel()
	tcs := []struct {
		name string
		args knowledge.Folder
		want bool
	}{
		{"a folder under the review chars is not full", knowledge.Folder{Chars: knowledge.ReviewChars - 1}, false},
		{"a folder at the review chars is not full", knowledge.Folder{Chars: knowledge.ReviewChars}, false},
		{"a folder over the review chars is full", knowledge.Folder{Chars: knowledge.ReviewChars + 1}, true},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Full())
		})
	}
}

// The item itself counts so a folder of n other items holds n plus one
func TestFolderCrowded(t *testing.T) {
	t.Parallel()
	tcs := []struct {
		name string
		args int
		want bool
	}{
		{"the item alone is not crowded", 0, false},
		{"five items with the item are not crowded", 4, false},
		{"six items with the item are crowded", 5, true},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, knowledge.Folder{Items: make(knowledge.Set, tc.args)}.Crowded())
		})
	}
}

func TestFolderString(t *testing.T) {
	t.Parallel()
	tcs := []struct {
		name string
		args knowledge.Folder
		want string
	}{
		{"a folder of the item alone names no other item", knowledge.Folder{Items: knowledge.Set{}}, "no other item"},
		{
			"every other item comes with its version and the runes of its text",
			knowledge.Folder{Items: knowledge.Set{
				{ID: "k-a", Version: 1, Kind: knowledge.KindMeaning, Content: "one"},
				{ID: "k-b", Version: 2, Kind: knowledge.KindJudgment, Content: "두 개 🙂"},
			}},
			"k-a v1 39 chars, k-b v2 42 chars",
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.String())
		})
	}
}
