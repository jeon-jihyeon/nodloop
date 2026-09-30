package file

import (
	"fmt"
	"path/filepath"

	"github.com/jeon-jihyeon/nodloop/internal/veto"
)

// One veto file that loaded
type Source struct {
	Path   string
	Vetoes veto.Vetoes
}

// An approved file whose first line names a relative record directory
// Its vetoes still apply and only a check reports it because no write can tell which ledger it belongs to
func (s Source) Orphan() error {
	if !approvedName(filepath.Base(s.Path)) {
		return nil
	}
	recordDir := approvedRecordDir(s.Path)
	if recordDir == "" || filepath.IsAbs(recordDir) {
		return nil
	}
	return fmt.Errorf("%s: record directory %q: %w", s.Path, recordDir, ErrOrphan)
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
