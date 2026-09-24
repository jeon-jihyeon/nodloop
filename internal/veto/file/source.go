package file

import "github.com/jeon-jihyeon/nodloop/internal/veto"

// One veto file that loaded
type Source struct {
	Path   string
	Vetoes veto.Vetoes
}

// Loaded veto files in priority order
type Sources []Source

// Earlier sources win and later duplicates of an id are dropped
func (s Sources) Vetoes() veto.Vetoes {
	var merged veto.Vetoes
	for _, source := range s {
		for _, v := range source.Vetoes {
			if merged.Has(v.ID()) {
				continue
			}
			merged = append(merged, v)
		}
	}
	return merged
}
