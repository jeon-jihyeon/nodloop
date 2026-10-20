package knowledge_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

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
		{"a folder of fifty items under the review chars is not full", knowledge.Folder{Chars: 1, Carried: make(knowledge.Set, 49)}, false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Full())
		})
	}
}

func TestFolderAnchor(t *testing.T) {
	t.Parallel()
	broad := &knowledge.RunScope{Producer: "session"}
	narrow := &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"nodloop"}}}
	tcs := []struct {
		name string
		args knowledge.Set
		want string
	}{
		{"a folder of the item alone has no anchor", knowledge.Set{}, ""},
		{"the more specific item comes first as in For", knowledge.Set{{ID: "k-a", Run: broad}, {ID: "k-b", Run: narrow}}, "k-b"},
		{"the id breaks a tie", knowledge.Set{{ID: "k-d", Run: narrow}, {ID: "k-c", Run: narrow}}, "k-c"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, knowledge.Folder{Carried: tc.args}.Anchor())
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
