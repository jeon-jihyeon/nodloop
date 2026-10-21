package otel

import "errors"

var (
	// A collector answered a status other than 200
	ErrStatus    = errors.New("otel: collector refused the request")
	ErrMark      = errors.New("otel: export mark failed")
	ErrRejected  = errors.New("otel: collector dropped records")
	ErrRetryable = errors.New("otel: export incomplete")
	ErrResponse  = errors.New("otel: invalid collector response")
)
