package otel

// The OTLP JSON of an ExportTraceServiceRequest with only the fields nodloop fills
// The protobuf body is written from the same values
type request struct {
	ResourceSpans []resourceSpans `json:"resourceSpans"`
}

type resourceSpans struct {
	Resource   resource     `json:"resource"`
	ScopeSpans []scopeSpans `json:"scopeSpans"`
}

type resource struct {
	Attributes []attribute `json:"attributes"`
}

type scopeSpans struct {
	Scope scope  `json:"scope"`
	Spans []span `json:"spans"`
}

type scope struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

type span struct {
	TraceID    string      `json:"traceId"`
	SpanID     string      `json:"spanId"`
	Parent     string      `json:"parentSpanId,omitempty"`
	Name       string      `json:"name"`
	Kind       int         `json:"kind"`
	Start      string      `json:"startTimeUnixNano"`
	End        string      `json:"endTimeUnixNano"`
	Attributes []attribute `json:"attributes"`
	Links      []link      `json:"links,omitempty"`
	Status     *status     `json:"status,omitempty"`
}

type link struct {
	TraceID string `json:"traceId"`
	SpanID  string `json:"spanId"`
}

type status struct {
	Message string `json:"message,omitempty"`
	Code    int    `json:"code"`
}

// The OTLP JSON of an ExportLogsServiceRequest with only the fields nodloop fills
type logsRequest struct {
	ResourceLogs []resourceLogs `json:"resourceLogs"`
}

type resourceLogs struct {
	Resource  resource    `json:"resource"`
	ScopeLogs []scopeLogs `json:"scopeLogs"`
}

type scopeLogs struct {
	Scope      scope       `json:"scope"`
	LogRecords []logRecord `json:"logRecords"`
}

type logRecord struct {
	Time       string      `json:"timeUnixNano"`
	Severity   int         `json:"severityNumber"`
	EventName  string      `json:"eventName"`
	TraceID    string      `json:"traceId,omitempty"`
	SpanID     string      `json:"spanId,omitempty"`
	Attributes []attribute `json:"attributes"`
}

type attribute struct {
	Key   string `json:"key"`
	Value value  `json:"value"`
}

// One of the fields holds the value
// An integer is a decimal string as OTLP JSON encodes 64 bit integers
type value struct {
	String *string  `json:"stringValue,omitempty"`
	Int    *string  `json:"intValue,omitempty"`
	Double *float64 `json:"doubleValue,omitempty"`
	Array  *array   `json:"arrayValue,omitempty"`
}

type array struct {
	Values []value `json:"values"`
}

func str(key, s string) attribute {
	return attribute{Key: key, Value: value{String: &s}}
}

func double(key string, f float64) attribute {
	return attribute{Key: key, Value: value{Double: &f}}
}

func strs(key string, ss []string) attribute {
	values := make([]value, 0, len(ss))
	for _, s := range ss {
		values = append(values, value{String: &s})
	}
	return attribute{Key: key, Value: value{Array: &array{Values: values}}}
}
