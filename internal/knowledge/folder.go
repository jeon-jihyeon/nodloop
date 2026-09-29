package knowledge

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// The approved knowledge a review may carry together with one item
// 1. full when its text passes ReviewChars and a review would be cut
// 2. crowded when a compaction anchored at the item would cover more than FolderItems items
type Folder struct {
	// Runes of the item text and the texts of Items
	Chars int
	// The other approved items with an intersecting folder
	Items Set
	// Items a compaction anchored at the item would cover
	// Zero unless the item is approved and an event can replay it because only such an anchor can be compacted
	Compactable int
}

func (f Folder) Full() bool {
	return f.Chars > ReviewChars
}

// Crowded never refuses an approval
func (f Folder) Crowded() bool {
	return f.Compactable > FolderItems
}

// Every other item with the size of its text for the person who picks what to retire or replace
func (f Folder) String() string {
	parts := make([]string, 0, len(f.Items))
	for _, k := range f.Items {
		parts = append(parts, fmt.Sprintf("%s v%d %d chars", k.ID, k.Version, utf8.RuneCountInString(k.Text())))
	}
	if len(parts) == 0 {
		return "no other item"
	}
	return strings.Join(parts, ", ")
}
