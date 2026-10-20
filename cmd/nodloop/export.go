package main

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/otel"
)

// Time one export request may take before the collector counts as down
const exportTimeout = 30 * time.Second

const (
	// The endpoint, headers and protocol of the OpenTelemetry exporter environment
	// Read only when no flag names an endpoint
	envOTLPTracesEndpoint = "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"
	envOTLPLogsEndpoint   = "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT"
	envOTLPEndpoint       = "OTEL_EXPORTER_OTLP_ENDPOINT"
	envOTLPHeaders        = "OTEL_EXPORTER_OTLP_HEADERS"
	envOTLPProtocol       = "OTEL_EXPORTER_OTLP_PROTOCOL"
	envOTLPTracesProtocol = "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL"
)

// Repeatable Name=value flag of HTTP headers
type headerFlag map[string]string

func (h *headerFlag) String() string { return fmt.Sprint(map[string]string(*h)) }

func (h *headerFlag) Set(v string) error {
	name, value, ok := strings.Cut(v, "=")
	if !ok || name == "" {
		return fmt.Errorf("%w: %q is not Name=value", errHeaderFlag, v)
	}
	if *h == nil {
		*h = headerFlag{}
	}
	(*h)[name] = value
	return nil
}

// The headers of a variable in the OTEL_EXPORTER_OTLP_HEADERS format that a --header flag left out
// The variable holds comma separated name=value pairs with URL encoded values
func (h *headerFlag) fill(name, env string) error {
	for pair := range strings.SplitSeq(env, ",") {
		if strings.TrimSpace(pair) == "" {
			continue
		}
		key, value, ok := strings.Cut(pair, "=")
		if !ok {
			return fmt.Errorf("%w: %s holds a pair that is not name=value", errHeaderFlag, name)
		}
		decoded, err := url.QueryUnescape(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("%w: %s: %w", errHeaderFlag, name, err)
		}
		if _, set := (*h)[strings.TrimSpace(key)]; set {
			continue
		}
		if err := h.Set(strings.TrimSpace(key) + "=" + decoded); err != nil {
			return err
		}
	}
	return nil
}

// The flags of export otel that pick the target
type exportFlags struct {
	endpoint string
	protocol string
	headers  headerFlag
	content  bool
}

// Where the export sends
// 1. --endpoint names a traces URL and sends traces only with the --header headers
// 2. else the OTEL_EXPORTER_OTLP variables with their headers
// 3. the protocol is --protocol, then the environment, then http/protobuf
func (f exportFlags) target(getenv func(string) string) (otel.Config, error) {
	headers := headerFlag{}
	maps.Copy(headers, f.headers)
	cfg := otel.Config{Content: f.content}
	var protocol string
	switch {
	case f.endpoint != "":
		cfg.Traces = f.endpoint
	default:
		cfg.Traces, cfg.Logs = otlpEndpoints(getenv)
		protocol = cmp.Or(getenv(envOTLPTracesProtocol), getenv(envOTLPProtocol))
		if err := headers.fill(envOTLPHeaders, getenv(envOTLPHeaders)); err != nil {
			return otel.Config{}, err
		}
	}
	if cfg.Traces == "" {
		return otel.Config{}, fmt.Errorf("--endpoint %w", errRequired)
	}
	cfg.Protocol = otel.Protocol(cmp.Or(f.protocol, protocol, string(otel.ProtocolProtobuf)))
	if !cfg.Protocol.Valid() {
		return otel.Config{}, fmt.Errorf("%w %q. Use %s or %s", errOTelProtocol, cfg.Protocol, otel.ProtocolProtobuf, otel.ProtocolJSON)
	}
	cfg.Headers = headers
	return cfg, nil
}

// The traces and the logs endpoint of the OTLP environment
// 1. traces: the traces variable or else the base variable with /v1/traces
// 2. logs: the logs variable or else the base variable with /v1/logs
func otlpEndpoints(getenv func(string) string) (string, string) {
	traces, logs := getenv(envOTLPTracesEndpoint), getenv(envOTLPLogsEndpoint)
	if base := strings.TrimSuffix(getenv(envOTLPEndpoint), "/"); base != "" {
		traces, logs = cmp.Or(traces, base+"/v1/traces"), cmp.Or(logs, base+"/v1/logs")
	}
	return traces, logs
}

// export otel sends the records to an OTLP collector
func runExport(args []string, getenv func(string) string, now func() time.Time, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return fail(stderr, "export", errNoAction)
	}
	if args[0] != "otel" {
		return fail(stderr, "export", fmt.Errorf("%w %q", errUnknownAction, args[0]))
	}
	fs := newFlagSet("export otel", stderr)
	var records recordFlags
	records.bind(fs)
	var f exportFlags
	fs.StringVar(&f.endpoint, "endpoint", "", "the OTLP traces URL such as http://localhost:4318/v1/traces. Sends traces only")
	fs.StringVar(&f.protocol, "protocol", "", "http/protobuf, the default, or http/json")
	fs.Var(&f.headers, "header", "Name=value of a header to send, repeatable, such as Authorization=Basic <token>")
	since := fs.String("since", "", "only records from this RFC3339 time on")
	fs.BoolVar(&f.content, "with-content", false, "also send outputs, reasons, notes and candidate text")
	fs.BoolVar(&f.content, "with-output", false, "the earlier name of --with-content")
	if err := fs.Parse(args[1:]); err != nil {
		return parseFailed(err)
	}
	var start time.Time
	if *since != "" {
		var err error
		if start, err = time.Parse(time.RFC3339, *since); err != nil {
			return fail(stderr, "export", err)
		}
	}
	a, err := records.app(getenv, now)
	if err != nil {
		return fail(stderr, "export", err)
	}
	cfg, err := f.target(getenv)
	if err != nil {
		return fail(stderr, "export", err)
	}
	// A redirect fails as its status since following it would carry the records and the headers to an endpoint nobody named
	cfg.Client = &http.Client{Timeout: exportTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	cfg.Version = buildVersion()
	c := exportCommand{app: a, out: stdout, exporter: otel.New(cfg)}
	if err := c.since(context.Background(), start); err != nil {
		return fail(stderr, "export", err)
	}
	return 0
}

type exportCommand struct {
	app      app
	out      io.Writer
	exporter *otel.Exporter
}

// Sends every record from the time on
func (c exportCommand) since(ctx context.Context, since time.Time) error {
	recs, err := c.read(ctx, since)
	if err != nil {
		return err
	}
	return c.send(ctx, recs)
}

// Sends the records and prints what went out
func (c exportCommand) send(ctx context.Context, recs exportRecords) error {
	b, err := c.batch(ctx, recs)
	if err != nil {
		return err
	}
	sent, err := c.exporter.Export(ctx, b)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "exported %d runs, %d verdicts and %d outcomes\n", sent.Runs, sent.Verdicts, sent.Outcomes)
	return nil
}
