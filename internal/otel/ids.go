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

// The span inside the trace of base whose span id derives from the key
func (s SpanRef) child(key string) SpanRef {
	return SpanRef{TraceID: s.TraceID, SpanID: spanOf(key).SpanID}
}

// The span of the turn of a run
func RunSpan(runID string) SpanRef {
	return spanOf(runID)
}

// The span of a model step trace
func CallSpan(traceID string) SpanRef {
	return spanOf(traceID)
}

// The span of a review question
// 1. inside the trace of the run of its turn when the records hold one
// 2. else the root of a trace of its own keyed by the session
func ReviewSpan(runID, sessionID string, candidate Ref, at time.Time) SpanRef {
	key := ":review:" + candidate.ID + ":" + strconv.Itoa(candidate.Version) + ":" + nanos(at)
	if runID == "" {
		return spanOf(sessionID + key)
	}
	return RunSpan(runID).child(runID + key)
}

// The carrier span of a verdict on a run
func VerdictSpan(runID string, at time.Time) SpanRef {
	return spanOf("verdict:" + runID + ":" + nanos(at))
}

// The carrier span of an outcome of a run
func OutcomeSpan(runID string, at time.Time) SpanRef {
	return spanOf("outcome:" + runID + ":" + nanos(at))
}

// The carrier span of a decision on a candidate
func DecisionSpan(candidate Ref, status string, at time.Time) SpanRef {
	return spanOf("decision:" + candidate.ID + ":" + strconv.Itoa(candidate.Version) + ":" + status + ":" + nanos(at))
}

// The inference child of a model step
func (s SpanRef) chat(traceID string) SpanRef {
	return s.child(traceID + ":chat")
}

// The log record uid of an evaluation from the ids of its carrier
func (s SpanRef) uid() string {
	return s.TraceID
}

// OTLP JSON encodes 64 bit integers as strings
func nanos(t time.Time) string {
	return strconv.FormatInt(t.UnixNano(), 10)
}
