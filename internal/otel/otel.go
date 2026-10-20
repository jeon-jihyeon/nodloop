// Package otel sends records as OTLP traces and logs of the GenAI and OpenInference conventions over HTTP in protobuf or JSON
package otel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"slices"
	"strings"
)

// Spans or log records in one request
// Collectors refuse bodies past a few megabytes and a span carries text only with content
const batch = 200

// The encodings of OTLP over HTTP
type Protocol string

const (
	ProtocolProtobuf Protocol = "http/protobuf" // the OTLP default and the only one Phoenix takes
	ProtocolJSON     Protocol = "http/json"     // for a collector that reads JSON alone
)

func (p Protocol) Valid() bool {
	return p == ProtocolProtobuf || p == ProtocolJSON
}

func (p Protocol) contentType() string {
	if p == ProtocolJSON {
		return "application/json"
	}
	return "application/x-protobuf"
}

// Where and how an exporter sends
type Config struct {
	// The traces endpoint such as http://localhost:4318/v1/traces
	Traces string
	// The logs endpoint such as http://localhost:4318/v1/logs
	// Empty sends no log record
	Logs     string
	Protocol Protocol
	Headers  map[string]string
	// The nodloop version in the resource and the scope
	Version string
	Client  *http.Client
	// Text a person or a model wrote goes along only when true
	Content bool
}

// A collector that takes OTLP traces and logs over HTTP such as an OpenTelemetry Collector, Langfuse or Phoenix
type Exporter struct {
	cfg Config
}

func New(cfg Config) *Exporter {
	return &Exporter{cfg: cfg}
}

// How many records of each kind reached the collector
type Sent struct {
	Runs      int
	Questions int
	Verdicts  int
	Outcomes  int
	Decisions int
	// The answers, the interventions and the decides
	Judgments  int
	Calls      int
	LogRecords int
}

// One span with the kind of record it came from
type item struct {
	kind string
	span span
	// The last span of a record counts the record once it is sent
	last bool
}

// Sends every record of the batch as its spans and then every evaluation and judge classify as a log record
// 1. the ids derive from the records so sending a batch again sends the same spans
// 2. the log records go after every span so a refused logs request fails the export with the traces sent
// Returns what was sent until the first failure
func (e *Exporter) Export(ctx context.Context, b Batch) (Sent, error) {
	var sent Sent
	items := b.items(e.cfg.Content)
	for chunk := range slices.Chunk(items, batch) {
		spans := make([]span, 0, len(chunk))
		for _, it := range chunk {
			spans = append(spans, it.span)
		}
		if err := e.send(ctx, e.cfg.Traces, e.spans(spans)); err != nil {
			return sent, err
		}
		for _, it := range chunk {
			if it.last {
				sent.count(it.kind)
			}
		}
	}
	if e.cfg.Logs == "" {
		return sent, nil
	}
	records := make([]logRecord, 0, len(b.Evaluations))
	for _, ev := range b.Evaluations {
		records = append(records, ev.record(e.cfg.Content))
	}
	for _, c := range b.Calls {
		if c.Judge != nil {
			records = append(records, c.judged().record(e.cfg.Content))
		}
	}
	for chunk := range slices.Chunk(records, batch) {
		if err := e.send(ctx, e.cfg.Logs, e.logs(chunk)); err != nil {
			return sent, err
		}
		sent.LogRecords += len(chunk)
	}
	return sent, nil
}

// Every span of the batch in the order runs, questions, evaluations, model steps
func (b Batch) items(content bool) []item {
	var out []item
	for _, r := range b.Runs {
		out = append(out, item{kind: "run", span: r.span(content), last: true})
	}
	for _, q := range b.Questions {
		out = append(out, item{kind: "question", span: q.span(content), last: true})
	}
	for _, ev := range b.Evaluations {
		out = append(out, item{kind: string(ev.Name), span: ev.span(content), last: true})
	}
	for _, c := range b.Calls {
		spans := c.spans(content)
		for i, s := range spans {
			out = append(out, item{kind: "call", span: s, last: i == len(spans)-1})
		}
	}
	return out
}

func (s *Sent) count(kind string) {
	switch kind {
	case "run":
		s.Runs++
	case "question":
		s.Questions++
	case string(EvaluationVerdict):
		s.Verdicts++
	case string(EvaluationOutcome):
		s.Outcomes++
	case string(EvaluationDecision):
		s.Decisions++
	case string(EvaluationJudgment), string(EvaluationIntervention), string(EvaluationCurate):
		s.Judgments++
	case "call":
		s.Calls++
	}
}

// service.name and service.version as the resource and the scope of every request
func (e *Exporter) resource() resource {
	attrs := []attribute{str("service.name", "nodloop")}
	return resource{Attributes: appendStr(attrs, "service.version", e.cfg.Version)}
}

func (e *Exporter) spans(spans []span) request {
	return request{ResourceSpans: []resourceSpans{{
		Resource:   e.resource(),
		ScopeSpans: []scopeSpans{{Scope: scope{Name: "nodloop", Version: e.cfg.Version}, Spans: spans}},
	}}}
}

func (e *Exporter) logs(records []logRecord) logsRequest {
	return logsRequest{ResourceLogs: []resourceLogs{{
		Resource:  e.resource(),
		ScopeLogs: []scopeLogs{{Scope: scope{Name: "nodloop", Version: e.cfg.Version}, LogRecords: records}},
	}}}
}

// A request body in either encoding
type payload interface {
	proto() []byte
}

// Posts the payload to the endpoint in the encoding of the protocol
func (e *Exporter) send(ctx context.Context, url string, p payload) error {
	body := p.proto()
	if e.cfg.Protocol == ProtocolJSON {
		var err error
		if body, err = json.Marshal(p); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return redactedErr(err, url)
	}
	req.Header.Set("Content-Type", e.cfg.Protocol.contentType())
	for k, v := range e.cfg.Headers {
		req.Header.Set(k, v)
	}
	res, err := e.cfg.Client.Do(req)
	if err != nil {
		return redactedErr(err, url)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode/100 != 2 {
		snippet, _ := io.ReadAll(io.LimitReader(res.Body, 200))
		return fmt.Errorf("%w: %s answered %d: %s", ErrStatus, redacted(url), res.StatusCode, strings.TrimSpace(string(snippet)))
	}
	return nil
}

// The endpoint without its password and query as hook.log keeps it
// A collector may take its token in either
func redacted(endpoint string) string {
	u, err := neturl.Parse(endpoint)
	if err != nil {
		return "the endpoint"
	}
	u.RawQuery, u.Fragment = "", ""
	return u.Redacted()
}

// A parse or transport error names the url it failed on
func redactedErr(err error, endpoint string) error {
	if ue := (*neturl.Error)(nil); errors.As(err, &ue) {
		ue.URL = redacted(endpoint)
	}
	return err
}
