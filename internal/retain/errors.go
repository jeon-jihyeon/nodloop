package retain

import "errors"

var (
	// A shorter window could remove the latest run of a session that a reader looks back MinDays for
	ErrDaysTooShort = errors.New("retain: retention too short")
	ErrMarker       = errors.New("retain: daily marker failed")
)
