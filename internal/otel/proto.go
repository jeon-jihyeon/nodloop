package otel

import (
	"encoding/binary"
	"encoding/hex"
	"math"
	"strconv"
)

// The protobuf wire types OTLP uses
const (
	wireVarint  = 0
	wireFixed64 = 1
	wireBytes   = 2
)

// A protobuf message written field by field in the order of the OTLP proto files
// nodloop writes the few messages it sends itself so the binary every hook starts takes no protobuf module
type pb []byte

func (b *pb) tag(field, wire int) {
	b.varint(uint64(field)<<3 | uint64(wire))
}

func (b *pb) varint(v uint64) {
	*b = binary.AppendUvarint(*b, v)
}

func (b *pb) bytes(field int, v []byte) {
	if len(v) == 0 {
		return
	}
	b.tag(field, wireBytes)
	b.varint(uint64(len(v)))
	*b = append(*b, v...)
}

func (b *pb) string(field int, s string) {
	b.bytes(field, []byte(s))
}

func (b *pb) uint(field int, v uint64) {
	if v == 0 {
		return
	}
	b.tag(field, wireVarint)
	b.varint(v)
}

func (b *pb) fixed64(field int, v uint64) {
	b.tag(field, wireFixed64)
	*b = binary.LittleEndian.AppendUint64(*b, v)
}

// A nested message with its length in front
// An empty message is still written since its presence sets a oneof
func (b *pb) message(field int, write func(*pb)) {
	var inner pb
	write(&inner)
	b.tag(field, wireBytes)
	b.varint(uint64(len(inner)))
	*b = append(*b, inner...)
}

// An id in hex as the bytes the proto field holds
func (b *pb) id(field int, s string) {
	raw, _ := hex.DecodeString(s)
	b.bytes(field, raw)
}

// A time in decimal unix nanoseconds as its fixed64 field
func (b *pb) time(field int, s string) {
	n, _ := strconv.ParseUint(s, 10, 64)
	b.fixed64(field, n)
}

// The ExportTraceServiceRequest
func (r request) proto() []byte {
	var b pb
	for _, rs := range r.ResourceSpans {
		b.message(1, func(b *pb) {
			b.message(1, rs.Resource.proto)
			for _, ss := range rs.ScopeSpans {
				b.message(2, func(b *pb) {
					b.message(1, ss.Scope.proto)
					for _, s := range ss.Spans {
						b.message(2, s.proto)
					}
				})
			}
		})
	}
	return b
}

// The ExportLogsServiceRequest
func (r logsRequest) proto() []byte {
	var b pb
	for _, rl := range r.ResourceLogs {
		b.message(1, func(b *pb) {
			b.message(1, rl.Resource.proto)
			for _, sl := range rl.ScopeLogs {
				b.message(2, func(b *pb) {
					b.message(1, sl.Scope.proto)
					for _, l := range sl.LogRecords {
						b.message(2, l.proto)
					}
				})
			}
		})
	}
	return b
}

func (r resource) proto(b *pb) {
	for _, a := range r.Attributes {
		b.message(1, a.proto)
	}
}

func (s scope) proto(b *pb) {
	b.string(1, s.Name)
	b.string(2, s.Version)
}

func (s span) proto(b *pb) {
	b.id(1, s.TraceID)
	b.id(2, s.SpanID)
	b.id(4, s.Parent)
	b.string(5, s.Name)
	b.uint(6, uint64(s.Kind))
	b.time(7, s.Start)
	b.time(8, s.End)
	for _, a := range s.Attributes {
		b.message(9, a.proto)
	}
	for _, l := range s.Links {
		b.message(13, func(b *pb) {
			b.id(1, l.TraceID)
			b.id(2, l.SpanID)
		})
	}
	if s.Status != nil {
		b.message(15, func(b *pb) {
			b.string(2, s.Status.Message)
			b.uint(3, uint64(s.Status.Code))
		})
	}
}

func (l logRecord) proto(b *pb) {
	b.time(1, l.Time)
	b.uint(2, uint64(l.Severity))
	for _, a := range l.Attributes {
		b.message(6, a.proto)
	}
	b.id(9, l.TraceID)
	b.id(10, l.SpanID)
	b.string(12, l.EventName)
}

func (a attribute) proto(b *pb) {
	b.string(1, a.Key)
	b.message(2, a.Value.proto)
}

// The AnyValue with the one field that is set
// An empty string is still written since the oneof would read as no value without it
func (v value) proto(b *pb) {
	switch {
	case v.String != nil:
		b.tag(1, wireBytes)
		b.varint(uint64(len(*v.String)))
		*b = append(*b, *v.String...)
	case v.Int != nil:
		n, _ := strconv.ParseInt(*v.Int, 10, 64)
		b.tag(3, wireVarint)
		b.varint(uint64(n))
	case v.Double != nil:
		b.fixed64(4, math.Float64bits(*v.Double))
	case v.Array != nil:
		b.message(5, func(b *pb) {
			for _, item := range v.Array.Values {
				b.message(1, item.proto)
			}
		})
	}
}
