package knowledge

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// The approved knowledge one run may carry together with one item
// Full when its text passes RunChars and a run would be cut
type Folder struct {
	// Runes of the item text and the texts of Carried
	Chars int
	// The other approved items a run carries with the item
	Carried Set
	// The producer whose runs carry the item
	Producer string
}

func (f Folder) Full() bool {
	return f.Chars > RunChars
}

// The item a compaction that makes room in the folder starts from
// 1. the first carried item in the For order since a run reads it first
// 2. empty when the folder carries no other item
func (f Folder) Anchor() string {
	if len(f.Carried) == 0 {
		return ""
	}
	return slices.MinFunc(f.Carried, Knowledge.rank).ID
}

// The cap the folder is held to for the person who picks the way out
func (f Folder) load() string {
	load := fmt.Sprintf("%d of %d chars", f.Chars, RunChars)
	if f.Producer == "" {
		return load
	}
	return load + " in runs of " + f.Producer
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
