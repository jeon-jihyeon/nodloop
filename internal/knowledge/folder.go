package knowledge

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// The approved knowledge one run may carry together with one item
// 1. full when its text passes ReviewChars or its items pass ReviewItems and a run would be cut
// 2. crowded when one run would carry more than FolderItems items a compaction anchored at the item covers
type Folder struct {
	// Runes of the item text and the texts of Carried
	Chars int
	// The other approved items a review of Context carries with the item
	Carried Set
	// Replayable approved items the heaviest review carries with the item counting the item
	// Zero unless the item is approved and an event can replay it because only such an anchor can be compacted
	Compactable int
	// The producer whose runs carry an item with a run scope
	// Empty for an item of the data review
	Producer string
}

// The item itself counts beside Carried
func (f Folder) Size() int {
	return len(f.Carried) + 1
}

func (f Folder) Full() bool {
	return f.Chars > ReviewChars || f.Size() > ReviewItems
}

// Both caps the folder is held to for the person who picks the way out
func (f Folder) load() string {
	load := fmt.Sprintf("%d of %d chars %d of %d items", f.Chars, ReviewChars, f.Size(), ReviewItems)
	if f.Producer == "" {
		return load
	}
	return load + " in runs of " + f.Producer
}

// Crowded never refuses an approval
func (f Folder) Crowded() bool {
	return f.Compactable > FolderItems
}

// Every carried item with the size of its text for the person who picks what to retire or replace
func (f Folder) String() string {
	parts := make([]string, 0, len(f.Carried))
	for _, k := range f.Carried {
		parts = append(parts, fmt.Sprintf("%s v%d %d chars", k.ID, k.Version, utf8.RuneCountInString(k.Text())))
	}
	if len(parts) == 0 {
		return "no other item"
	}
	return strings.Join(parts, ", ")
}
