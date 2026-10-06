package nodloop

import "github.com/jeon-jihyeon/nodloop/internal/knowledge"

var (
	// A proposal from a run whose latest verdict does not correct it
	ErrNotCorrected = knowledge.ErrNotCorrected
	// A scope that names a label value no recorded run carries while NewLabels is off
	ErrScopeUnobserved = knowledge.ErrScopeUnobserved
	// A knowledge id or version that was never recorded
	ErrNotFound = knowledge.ErrNotFound
)
