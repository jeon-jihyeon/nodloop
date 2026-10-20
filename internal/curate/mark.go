package curate

import (
	"fmt"
	"path/filepath"

	"github.com/jeon-jihyeon/nodloop/internal/markfile"
)

// The mark of the last curate pass in the record directory
const MarkFile = "curate.json"

// The last pass as curate.json keeps it
type Mark struct {
	Knobs Knobs `json:"knobs"`
}

// The mark of the record directory read without its lock as the hooks read it
// A missing mark and one that does not decode read as the zero Mark so a hook draws nothing no pass set
func ReadMark(dir string) (Mark, error) {
	m, err := markfile.Read[Mark](filepath.Join(dir, MarkFile))
	if err != nil {
		return Mark{}, fmt.Errorf("%w: %w", ErrMark, err)
	}
	return m, nil
}
