package otel

import "errors"

// A collector answered a status outside 2xx
var ErrStatus = errors.New("otel: collector refused the spans")
