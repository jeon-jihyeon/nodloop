// Package otel sends runs and their verdicts to an OpenTelemetry collector as GenAI spans in OTLP JSON over HTTP
package otel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// Spans in one request
// Collectors refuse bodies past a few megabytes and a run carries its output only on request
const batch = 200

// A collector that takes OTLP traces over HTTP such as an OpenTelemetry Collector or Langfuse
type Exporter struct {
	// The traces endpoint such as http://localhost:4318/v1/traces
	url     string
	headers map[string]string
	version string
	client  *http.Client
}

func New(url string, headers map[string]string, version string, client *http.Client) *Exporter {
	return &Exporter{url: url, headers: headers, version: version, client: client}
}

// Sends one span per run with one evaluation event for the verdict in effect on it
// 1. the span and trace ids derive from the run id so sending a run again replaces it in a collector that keys on them
// 2. the output goes along only when output is true since it may hold what a person wrote
// 3. only the newest verdict of a run counts and a run whose newest verdict withdraws gets no event
func (e *Exporter) Export(ctx context.Context, runs trace.Traces, verdicts []feedback.Feedback, output bool) error {
	byRun := map[string][]feedback.Feedback{}
	for _, v := range feedback.Records(verdicts).Latest() {
		byRun[v.TraceID] = append(byRun[v.TraceID], v)
	}
	for chunk := range slices.Chunk(runs, batch) {
		spans := make([]span, 0, len(chunk))
		for _, r := range chunk {
			spans = append(spans, newSpan(r, byRun[r.ID], output))
		}
		if err := e.send(ctx, spans); err != nil {
			return err
		}
	}
	return nil
}

func (e *Exporter) send(ctx context.Context, spans []span) error {
	body, err := json.Marshal(request{ResourceSpans: []resourceSpans{{
		Resource:   resource{Attributes: []attribute{str("service.name", "nodloop")}},
		ScopeSpans: []scopeSpans{{Scope: scope{Name: "nodloop", Version: e.version}, Spans: spans}},
	}}})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range e.headers {
		req.Header.Set(k, v)
	}
	res, err := e.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode/100 != 2 {
		snippet, _ := io.ReadAll(io.LimitReader(res.Body, 200))
		return fmt.Errorf("%w: %s answered %d: %s", ErrStatus, e.url, res.StatusCode, strings.TrimSpace(string(snippet)))
	}
	return nil
}

// The verdict as the score of an evaluation
// approve is 1 and a correction 0
var scores = map[feedback.Verdict]float64{
	feedback.VerdictApprove: 1,
	feedback.VerdictEdit:    0,
	feedback.VerdictReject:  0,
}

// The run as an invoke_agent span of the GenAI conventions
func newSpan(r trace.Trace, verdicts []feedback.Feedback, output bool) span {
	sum := sha256.Sum256([]byte("nodloop:" + r.ID))
	at := nanos(r.Time)
	s := span{
		TraceID: hex.EncodeToString(sum[:16]), SpanID: hex.EncodeToString(sum[16:24]),
		Name: "invoke_agent " + r.Producer, Kind: spanKindInternal, Start: at, End: at,
		Attributes: []attribute{
			str("gen_ai.operation.name", "invoke_agent"),
			str("gen_ai.agent.name", r.Producer),
			str("nodloop.run.id", r.ID),
		},
	}
	if r.SessionID != "" {
		s.Attributes = append(s.Attributes, str("session.id", r.SessionID))
	}
	if r.Subject != "" {
		s.Attributes = append(s.Attributes, str("nodloop.subject", r.Subject))
	}
	for _, k := range slices.Sorted(maps.Keys(r.Labels)) {
		s.Attributes = append(s.Attributes, strs("nodloop.label."+k, r.Labels[k]))
	}
	if output && len(r.Output) > 0 {
		s.Attributes = append(s.Attributes, str("output.value", text(r.Output)))
	}
	for _, v := range verdicts {
		attrs := []attribute{str("gen_ai.evaluation.name", "nodloop.verdict"), str("gen_ai.evaluation.score.label", string(v.Verdict))}
		if score, ok := scores[v.Verdict]; ok {
			attrs = append(attrs, double("gen_ai.evaluation.score.value", score))
		}
		if v.Reason != "" {
			attrs = append(attrs, str("gen_ai.evaluation.explanation", v.Reason))
		}
		if v.ReasonCode != "" {
			attrs = append(attrs, str("nodloop.reason_code", string(v.ReasonCode)))
		}
		attrs = append(attrs, str("nodloop.reviewer", v.Reviewer))
		s.Events = append(s.Events, event{Time: nanos(v.Time), Name: "gen_ai.evaluation.result", Attributes: attrs})
	}
	return s
}

// A JSON string output as its text and any other JSON as written
func text(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

// OTLP JSON encodes 64 bit integers as strings
func nanos(t time.Time) string {
	return strconv.FormatInt(t.UnixNano(), 10)
}

// The OTLP JSON of an ExportTraceServiceRequest with only the fields nodloop fills
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

const spanKindInternal = 1

type span struct {
	// Hex as the OTLP JSON mapping writes ids
	TraceID    string      `json:"traceId"`
	SpanID     string      `json:"spanId"`
	Name       string      `json:"name"`
	Kind       int         `json:"kind"`
	Start      string      `json:"startTimeUnixNano"`
	End        string      `json:"endTimeUnixNano"`
	Attributes []attribute `json:"attributes"`
	Events     []event     `json:"events,omitempty"`
}

type event struct {
	Time       string      `json:"timeUnixNano"`
	Name       string      `json:"name"`
	Attributes []attribute `json:"attributes"`
}

type attribute struct {
	Key   string `json:"key"`
	Value value  `json:"value"`
}

// One of the fields holds the value
type value struct {
	String *string  `json:"stringValue,omitempty"`
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
