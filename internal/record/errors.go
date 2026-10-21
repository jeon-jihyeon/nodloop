package record

import "errors"

var (
	// Returned by a trace store Get for an unknown id
	ErrTraceNotFound = errors.New("trace: not found")
	// Returned by a store read in append order for a position it no longer holds or never answered
	// A prune or a repair that removed the record at the position leaves it so
	ErrPositionGone = errors.New("record: the store holds no record at the position")
)
