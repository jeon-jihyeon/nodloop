package knowledge

import (
	"fmt"
	"strings"
)

// The approved knowledge a review may carry together with one item
// A folder may outgrow the review when its text passes the budget and a person then splits it or replaces an item
type Folder struct {
	// Length of the item text and the texts of Items
	Chars  int
	Budget int
	// The other approved items with an intersecting folder
	Items Set
}

func (f Folder) Full() bool {
	return f.Chars > f.Budget
}

// Every other item with the size of its text for the person who picks what to retire or replace
func (f Folder) String() string {
	parts := make([]string, 0, len(f.Items))
	for _, k := range f.Items {
		parts = append(parts, fmt.Sprintf("%s v%d %d chars", k.ID, k.Version, len(k.Text())))
	}
	if len(parts) == 0 {
		return "no other item"
	}
	return strings.Join(parts, ", ")
}
