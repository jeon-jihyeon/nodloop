package main

import (
	"cmp"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/jeon-jihyeon/nodloop/internal/otel"
)

const (
	// The endpoint, headers and protocol of the OpenTelemetry exporter environment
	// Read only when neither a flag nor config.json names an endpoint
	envOTLPTracesEndpoint = "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"
	envOTLPLogsEndpoint   = "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT"
	envOTLPEndpoint       = "OTEL_EXPORTER_OTLP_ENDPOINT"
	envOTLPHeaders        = "OTEL_EXPORTER_OTLP_HEADERS"
	envOTLPProtocol       = "OTEL_EXPORTER_OTLP_PROTOCOL"
	envOTLPTracesProtocol = "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL"
)

// The otel key of config.json
// Claude Code removes every OTEL_ variable from the environment of hooks and MCP servers so the export reads its target here
type otelConfig struct {
	// The base URL the signal paths join such as http://localhost:4318
	Endpoint string `json:"endpoint"`
	// http/protobuf when empty
	Protocol otel.Protocol `json:"protocol,omitempty"`
	// The variable that holds the headers in the OTEL_EXPORTER_OTLP_HEADERS format
	// A header holds a token so its value is never saved
	HeadersEnv string `json:"headers_env,omitempty"`
	// Sends traces only for a backend that ingests no OTLP logs
	NoLogs bool `json:"no_logs,omitempty"`
	// Sends the text a person or a model wrote
	Content bool `json:"content,omitempty"`
}

// An endpoint must be an http URL without a user, a password or a query since the config file is no secret store
// The signal paths are joined after the base so a query would also cut them off
func (c otelConfig) check() error {
	u, err := url.Parse(c.Endpoint)
	switch {
	case err != nil:
		return fmt.Errorf("%w: %w", errOTelEndpoint, err)
	case u.Scheme != "http" && u.Scheme != "https" || u.Host == "":
		return fmt.Errorf("%w: %q is not an http or https URL", errOTelEndpoint, c.Endpoint)
	case u.User != nil, u.RawQuery != "" || u.ForceQuery, u.Fragment != "":
		return fmt.Errorf("%w: put the credentials in the variable of --headers-env and not in the URL", errOTelEndpoint)
	case c.Protocol != "" && !c.Protocol.Valid():
		return fmt.Errorf("%w %q. Use %s or %s", errOTelProtocol, c.Protocol, otel.ProtocolProtobuf, otel.ProtocolJSON)
	}
	return nil
}

// The traces and logs endpoints of the base URL
func (c otelConfig) endpoints() (string, string) {
	base := strings.TrimSuffix(c.Endpoint, "/")
	if c.NoLogs {
		return base + "/v1/traces", ""
	}
	return base + "/v1/traces", base + "/v1/logs"
}

// otel prints the saved export target
// set saves one and unset removes it
func runOTel(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	h := homeDir(getenv("HOME"))
	if h == "" {
		return fail(stderr, "otel", errHomeUnknown)
	}
	cmd := otelCommand{home: h, out: stdout}
	if len(args) == 0 {
		if err := cmd.show(); err != nil {
			return fail(stderr, "otel", err)
		}
		return 0
	}
	var err error
	switch args[0] {
	case "set":
		fs := newFlagSet("otel set", stderr)
		var c otelConfig
		fs.StringVar(&c.Endpoint, "endpoint", "", "the base URL of the collector such as http://127.0.0.1:4318. /v1/traces and /v1/logs are joined to it")
		protocol := fs.String("protocol", "", "http/protobuf, the default, or http/json")
		fs.StringVar(&c.HeadersEnv, "headers-env", "", "the variable that holds the headers as name=value pairs such as NODLOOP_OTLP_HEADERS")
		fs.BoolVar(&c.NoLogs, "no-logs", false, "send traces only, for Langfuse, Phoenix or Datadog LLM Observability")
		fs.BoolVar(&c.Content, "content", false, "also send outputs, reasons, notes and candidate text")
		if err := fs.Parse(args[1:]); err != nil {
			return parseFailed(err)
		}
		c.Protocol = otel.Protocol(*protocol)
		err = cmd.set(c)
	case "unset":
		err = cmd.unset()
	default:
		err = fmt.Errorf("%w %q", errUnknownAction, args[0])
	}
	if err != nil {
		return fail(stderr, "otel "+args[0], err)
	}
	return 0
}

type otelCommand struct {
	home homeDir
	out  io.Writer
}

// Saves the target so every later stop, verdict and model call exports on its own
func (c otelCommand) set(cfg otelConfig) error {
	if cfg.Endpoint == "" {
		return fmt.Errorf("--endpoint %w", errRequired)
	}
	if err := cfg.check(); err != nil {
		return err
	}
	if err := c.home.config().Save(map[string]any{"otel": cfg}); err != nil {
		return err
	}
	return c.print(&cfg)
}

// Nothing is exported on its own again
func (c otelCommand) unset() error {
	if err := c.home.config().Save(map[string]any{"otel": nil}); err != nil {
		return err
	}
	fmt.Fprintln(c.out, "otel\tnone")
	return nil
}

func (c otelCommand) show() error {
	uc, err := c.home.readConfig()
	if err != nil {
		return err
	}
	return c.print(uc.OTel)
}

// One line of the target and never a header value
func (c otelCommand) print(cfg *otelConfig) error {
	if cfg == nil {
		fmt.Fprintln(c.out, "otel\tnone")
		return nil
	}
	traces, logs := cfg.endpoints()
	fmt.Fprintf(c.out, "otel\t%s\t%s", cmp.Or(cfg.Protocol, otel.ProtocolProtobuf), traces)
	if logs != "" {
		fmt.Fprintf(c.out, "\t%s", logs)
	}
	if cfg.HeadersEnv != "" {
		fmt.Fprintf(c.out, "\theaders $%s", cfg.HeadersEnv)
	}
	if cfg.Content {
		fmt.Fprint(c.out, "\tcontent")
	}
	fmt.Fprintln(c.out)
	return nil
}
