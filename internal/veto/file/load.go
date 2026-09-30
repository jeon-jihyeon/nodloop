package file

import (
	"errors"
	"fmt"
	"os"

	"github.com/jeon-jihyeon/nodloop/internal/veto"
)

// Read and parse one veto file
// 1. errors name the path and a missing file still matches os.ErrNotExist
// 2. a file that cannot be read as a whole fails with ErrRead so a caller tells it from a broken entry
// 3. the entries that built come back even when others in the file failed
func Load(path string) (veto.Vetoes, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w: %w", path, ErrRead, err)
	}
	vetoes, err := veto.Parse(b)
	if errors.Is(err, veto.ErrYAMLInvalid) {
		return nil, fmt.Errorf("%s: %w: %w", path, ErrRead, err)
	}
	if err != nil {
		return vetoes, fmt.Errorf("%s: %w", path, err)
	}
	return vetoes, nil
}
