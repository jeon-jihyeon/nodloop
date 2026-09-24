package llm

import "errors"

var (
	ErrSchemaRequired = errors.New("llm: schema is required")
	ErrWorkDir        = errors.New("llm: work dir")
	ErrClaudeFailed   = errors.New("llm: claude failed")
	ErrNotResult      = errors.New("llm: claude output is not result json")
	ErrNoOutput       = errors.New("llm: claude result has no structured_output")
)
