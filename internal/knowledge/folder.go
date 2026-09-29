package knowledge

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// The approved knowledge a review may carry together with one item
// 1. full when its text passes ReviewChars and a review would be cut
// 2. crowded when it holds more than FolderItems items and a compaction is due
type Folder struct {
	// Runes of the item text and the texts of Items
	Chars int
	// The other approved items with an intersecting folder
	Items Set
}

func (f Folder) Full() bool {
	return f.Chars > ReviewChars
}

// The item counts with its folder items
// Crowded never refuses an approval
func (f Folder) Crowded() bool {
	return len(f.Items)+1 > FolderItems
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
