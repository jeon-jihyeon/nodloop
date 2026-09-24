package knowledge

import "errors"

var (
	ErrNotFound          = errors.New("knowledge: not found")
	ErrIDRequired        = errors.New("knowledge: id is required")
	ErrVersionInvalid    = errors.New("knowledge: version must be positive")
	ErrKindUnknown       = errors.New("knowledge: unknown kind")
	ErrContentRequired   = errors.New("knowledge: content is required")
	ErrEvidenceRequired  = errors.New("knowledge: at least one evidence reference is required")
	ErrBasisUnknown      = errors.New("knowledge: unknown basis")
	ErrStatusUnknown     = errors.New("knowledge: unknown status")
	ErrApproverRequired  = errors.New("knowledge: an approver is required")
	ErrAuthorRequired    = errors.New("knowledge: author is required")
	ErrTransitionInvalid = errors.New("knowledge: invalid status transition")
	ErrVersionUnapproved = errors.New("knowledge: version is not approved")
)
