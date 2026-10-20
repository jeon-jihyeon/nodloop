package otel

import "errors"

var (
	// A collector answered a status outside 2xx
	ErrStatus = errors.New("otel: collector refused the request")
	ErrMark   = errors.New("otel: export mark failed")
)
