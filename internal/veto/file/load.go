package file

import (
	"fmt"
	"os"

	"github.com/jeon-jihyeon/nodloop/internal/veto"
)

// Read and parse one veto file
// Errors name the path and a missing file still matches os.ErrNotExist
func Load(path string) (veto.Vetoes, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w: %w", path, ErrRead, err)
	}
	vetoes, err := veto.Parse(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return vetoes, nil
}
