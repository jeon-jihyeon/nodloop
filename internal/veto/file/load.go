package file

import (
	"errors"
	"fmt"

	"github.com/jeon-jihyeon/nodloop/internal/atomicfile"
	"github.com/jeon-jihyeon/nodloop/internal/veto"
)

// Bytes a veto file may hold
// A project file may link to a device such as /dev/zero and the guard reads it on every tool call
const sizeLimit = 1 << 20

// Read and parse one veto file
// 1. errors name the path and a missing file still matches os.ErrNotExist
// 2. a file that cannot be read as a whole fails with ErrRead so a caller tells it from a broken entry
// So does a path that names no regular file or one past sizeLimit
// 3. the entries that built come back even when others in the file failed
func Load(path string) (veto.Vetoes, error) {
	b, err := atomicfile.Read(path, sizeLimit)
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
