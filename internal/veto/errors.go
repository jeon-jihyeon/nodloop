package veto

import "errors"

var (
	ErrYAMLInvalid   = errors.New("failed to parse yaml")
	ErrEntryInvalid  = errors.New("invalid entry")
	ErrKeyUnknown    = errors.New("unknown key")
	ErrToolUnknown   = errors.New("not an exact Claude Code tool_name such as Bash or mcp__server__tool")
	ErrIDMissing     = errors.New("missing id")
	ErrIDDuplicate   = errors.New("duplicate id")
	ErrToolMissing   = errors.New("missing tool")
	ErrWhenMissing   = errors.New("missing when")
	ErrReasonMissing = errors.New("missing reason")
	ErrFieldMissing  = errors.New("missing field")
	ErrMatchMissing  = errors.New("missing match")
	ErrMatchInvalid  = errors.New("invalid match regexp")
	ErrUnlessInvalid = errors.New("invalid unless regexp")
	ErrActionUnknown = errors.New("action must be block or ask")
)
