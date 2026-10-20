package otel

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"
)

// The trace and span id of one span in hex as the OTLP JSON mapping writes them
// Every id derives from the record alone so an export that runs again sends the same ids
type SpanRef struct {
	TraceID string
	SpanID  string
}

func (s SpanRef) IsZero() bool {
	return s.SpanID == ""
}

// The first 16 bytes of SHA-256 of nodloop: and the key are the trace id and the next 8 the span id
func spanOf(key string) SpanRef {
	sum := sha256.Sum256([]byte("nodloop:" + key))
	return SpanRef{TraceID: hex.EncodeToString(sum[:16]), SpanID: hex.EncodeToString(sum[16:24])}
}

// The span of the turn of a run
func RunSpan(runID string) SpanRef {
	return spanOf(runID)
}

// The carrier span of a verdict on a run
func VerdictSpan(runID string, at time.Time) SpanRef {
	return spanOf("verdict:" + runID + ":" + nanos(at))
}

// The carrier span of an outcome of a run
func OutcomeSpan(runID string, at time.Time) SpanRef {
	return spanOf("outcome:" + runID + ":" + nanos(at))
}

// The log record uid of an evaluation from the ids of its carrier
func (s SpanRef) uid() string {
	return s.TraceID
}

// OTLP JSON encodes 64 bit integers as strings
func nanos(t time.Time) string {
	return strconv.FormatInt(t.UnixNano(), 10)
}
