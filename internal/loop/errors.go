package loop

import "errors"

var (
	ErrQueueOptions  = errors.New("loop: limit must not be negative and the audit rate must lie between 0 and 1")
	ErrReportUnknown = errors.New("loop: no such report")
)
