package veto

import "errors"

var (
	ErrYAMLInvalid   = errors.New("failed to parse yaml")
	ErrIDMissing     = errors.New("missing id")
	ErrIDDuplicate   = errors.New("duplicate id")
	ErrToolMissing   = errors.New("missing tool")
	ErrWhenMissing   = errors.New("missing when")
	ErrReasonMissing = errors.New("missing reason")
	ErrFieldMissing  = errors.New("missing field")
	ErrMatchMissing  = errors.New("missing match")
	ErrMatchInvalid  = errors.New("invalid match regexp")
	ErrUnlessInvalid = errors.New("invalid unless regexp")
)
