// Package otel sends runs with their verdicts and outcomes to an OpenTelemetry collector as GenAI spans in OTLP JSON over HTTP
package otel

import (
	"bytes"
	"cmp"
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

// Sends one span per run with one evaluation event for its verdict and one for its outcome
// 1. the span and trace ids derive from the run id so sending a run again replaces it in a collector that keys on them
// 2. the output goes along only when output is true since it may hold what a person wrote
// 3. the holdout arm and the items of the run go along so a backend can split the evaluations by arm
// Returns how many outcome events it sent until the first failure
func (e *Exporter) Export(ctx context.Context, runs []Run, output bool) (int, error) {
	sent := 0
	for chunk := range slices.Chunk(runs, batch) {
		spans := make([]span, 0, len(chunk))
		events := 0
		for _, r := range chunk {
			s := newSpan(r, output)
			if r.Outcome != nil {
				s.Events = append(s.Events, r.Outcome.event())
				events++
			}
			spans = append(spans, s)
		}
		if err := e.send(ctx, spans); err != nil {
			return sent, err
		}
		sent += events
	}
	return sent, nil
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
var scores = map[string]float64{
	"approve": 1,
	"edit":    0,
	"reject":  0,
}

// An outcome as the score of an evaluation
// Inconclusive is left out since a check that settled nothing has no score
var outcomeScores = map[string]float64{
	"confirmed": 1,
	"refuted":   0,
}

func (o Outcome) event() event {
	attrs := []attribute{str("gen_ai.evaluation.name", "nodloop.outcome"), str("gen_ai.evaluation.score.label", o.Result)}
	if score, ok := outcomeScores[o.Result]; ok {
		attrs = append(attrs, double("gen_ai.evaluation.score.value", score))
	}
	if why := cmp.Or(o.ConfirmedCause, o.Note); why != "" {
		attrs = append(attrs, str("gen_ai.evaluation.explanation", why))
	}
	attrs = append(attrs, str("nodloop.reviewer", o.Reviewer))
	return event{Time: nanos(o.Time), Name: "gen_ai.evaluation.result", Attributes: attrs}
}

func (v Verdict) event() event {
	attrs := []attribute{str("gen_ai.evaluation.name", "nodloop.verdict"), str("gen_ai.evaluation.score.label", v.Verdict)}
	if score, ok := scores[v.Verdict]; ok {
		attrs = append(attrs, double("gen_ai.evaluation.score.value", score))
	}
	if v.Reason != "" {
		attrs = append(attrs, str("gen_ai.evaluation.explanation", v.Reason))
	}
	if v.ReasonCode != "" {
		attrs = append(attrs, str("nodloop.reason_code", v.ReasonCode))
	}
	attrs = append(attrs, str("nodloop.reviewer", v.Reviewer))
	return event{Time: nanos(v.Time), Name: "gen_ai.evaluation.result", Attributes: attrs}
}

// The holdout arm and the items of a run as span attributes
// 1. the GenAI conventions name no experiment arm so the keys stay under nodloop
// 2. a run with no item to receive has no arm and a run with items that went through no draw is undrawn as report effect counts it
func (r Run) holdout() []attribute {
	var out []attribute
	if len(r.Applied) > 0 {
		out = append(out, strs("nodloop.knowledge.applied", refs(r.Applied)))
	}
	if len(r.Withheld) > 0 {
		out = append(out, strs("nodloop.knowledge.withheld", refs(r.Withheld)))
	}
	switch {
	case len(out) == 0:
		return nil
	case !r.Drawn:
		return append(out, str("nodloop.holdout.arm", "undrawn"))
	case len(r.Applied) > 0:
		return append(out, str("nodloop.holdout.arm", "applied"))
	}
	return append(out, str("nodloop.holdout.arm", "withheld"))
}

// Each ref as id:version
func refs(rs []Ref) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.ID+":"+strconv.Itoa(r.Version))
	}
	return out
}

// The run as an invoke_agent span of the GenAI conventions
func newSpan(r Run, output bool) span {
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
	s.Attributes = append(s.Attributes, r.holdout()...)
	if output && len(r.Output) > 0 {
		s.Attributes = append(s.Attributes, str("output.value", text(r.Output)))
	}
	if r.Verdict != nil {
		s.Events = append(s.Events, r.Verdict.event())
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
