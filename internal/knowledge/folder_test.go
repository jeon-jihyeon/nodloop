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
		{"a folder under the budget is not full", knowledge.Folder{Chars: 199, Budget: 200}, false},
		{"a folder at the budget is not full", knowledge.Folder{Chars: 200, Budget: 200}, false},
		{"a folder over the budget is full", knowledge.Folder{Chars: 201, Budget: 200}, true},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Full())
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
			"every other item comes with its version and the length of its text",
			knowledge.Folder{Items: knowledge.Set{
				{ID: "k-a", Version: 1, Kind: knowledge.KindMeaning, Content: "one"},
				{ID: "k-b", Version: 2, Kind: knowledge.KindJudgment, Content: "two"},
			}},
			"k-a v1 39 chars, k-b v2 40 chars",
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.String())
		})
	}
}
