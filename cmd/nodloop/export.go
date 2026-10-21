package main

import (
	"cmp"
	"context"
	"errors"
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
// 1. the variable holds comma separated name=value pairs with URL encoded values
// 2. an error names the variable and never a part of a value since a value holds a token and a detached export writes its error to hook.log
func (h *headerFlag) fill(name, env string) error {
	if *h == nil {
		*h = headerFlag{}
	}
	for pair := range strings.SplitSeq(env, ",") {
		if strings.TrimSpace(pair) == "" {
			continue
		}
		key, value, ok := strings.Cut(pair, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return fmt.Errorf("%w: %s holds a pair that is not name=value", errHeaderFlag, name)
		}
		decoded, err := url.QueryUnescape(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("%w: %s holds a value that is not URL encoded", errHeaderFlag, name)
		}
		if _, set := (*h)[key]; !set {
			(*h)[key] = decoded
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
// 2. else the otel key of config.json with the headers of the variable it names
// 3. else the OTEL_EXPORTER_OTLP variables with their headers since a person or an SDK producer may run it outside Claude Code
// 4. the protocol is --protocol, then the config or the environment, then http/protobuf
func (f exportFlags) target(saved *otelConfig, getenv func(string) string) (otel.Config, error) {
	headers := headerFlag{}
	maps.Copy(headers, f.headers)
	cfg := otel.Config{Content: f.content}
	var protocol string
	switch {
	case f.endpoint != "":
		cfg.Traces = f.endpoint
	case saved != nil:
		cfg.Traces, cfg.Logs = saved.endpoints()
		protocol, cfg.Content = string(saved.Protocol), cfg.Content || saved.Content
		if saved.HeadersEnv != "" {
			if err := headers.fill(saved.HeadersEnv, getenv(saved.HeadersEnv)); err != nil {
				return otel.Config{}, err
			}
		}
	default:
		cfg.Traces, cfg.Logs = otlpEndpoints(getenv)
		protocol = cmp.Or(getenv(envOTLPTracesProtocol), getenv(envOTLPProtocol))
		if err := headers.fill(envOTLPHeaders, getenv(envOTLPHeaders)); err != nil {
			return otel.Config{}, err
		}
	}
	if cfg.Traces == "" {
		return otel.Config{}, fmt.Errorf("--endpoint %w, or save one with nodloop otel set", errRequired)
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

// export otel sends the records to an OTLP collector and export package writes them as a Data Package folder
func runExport(args []string, getenv func(string) string, now func() time.Time, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return fail(stderr, "export", errNoAction)
	}
	if args[0] == "package" {
		return runPackage(args[1:], getenv, now, stdout, stderr)
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
	pending := fs.Bool("pending", false, "only what was recorded after the mark of the last export, then move the mark")
	fs.BoolVar(&f.content, "with-content", false, "also send outputs, reasons, notes and candidate text")
	fs.BoolVar(&f.content, "with-output", false, "the earlier name of --with-content")
	if err := fs.Parse(args[1:]); err != nil {
		return parseFailed(err)
	}
	if *since != "" && *pending {
		return fail(stderr, "export", errExportFlags)
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
	cfg, err := f.target(a.cfg.otel, getenv)
	if err != nil {
		return fail(stderr, "export", err)
	}
	// A redirect fails as its status since following it would carry the records and the headers to an endpoint nobody named
	cfg.Client = &http.Client{Timeout: exportTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	cfg.Version = buildVersion()
	c, err := a.exportCommand(otel.New(cfg), stdout)
	if err != nil {
		return fail(stderr, "export", err)
	}
	if *pending {
		err = c.pending(context.Background())
	} else {
		err = c.since(context.Background(), start)
	}
	if err != nil {
		return fail(stderr, "export", err)
	}
	return 0
}

type exportCommand struct {
	// The record directory whose mark --pending moves
	dir      string
	stores   recordStores
	out      io.Writer
	exporter *otel.Exporter
}

// The export over the stores of the record directory each opened once
func (a app) exportCommand(exporter *otel.Exporter, out io.Writer) (exportCommand, error) {
	stores, err := a.stores()
	if err != nil {
		return exportCommand{}, err
	}
	// The record directory exists once the stores above opened
	return exportCommand{dir: a.cfg.recordDir, stores: stores, out: out, exporter: exporter}, nil
}

// Sends every record stamped at the time or later
// Every store is read whole since a read that stops at the first record older than the time would end at one a slower process appended late
func (c exportCommand) since(ctx context.Context, since time.Time) error {
	read, err := c.stores.appended(ctx, otel.Mark{})
	if err != nil {
		return err
	}
	return c.send(ctx, read.records.stamped(since))
}

// Sends the records each store appended after the mark and then moves the mark
// 1. another export holding the mark ends this one at once since it sends the same records
// 2. a retryable failure keeps the cursors and terminal receipts exclude records the collector must not receive again
// 3. the mark keeps the position each store read up to so a stop reads only what was appended since
// A record a slower process appends later with an earlier time still goes next time
func (c exportCommand) pending(ctx context.Context) (err error) {
	held, err := otel.NewMarker(c.dir).Hold()
	if err != nil || held == nil {
		return err
	}
	defer func() { err = errors.Join(err, held.Release()) }()
	read, err := c.stores.appended(ctx, held.Mark)
	if err != nil {
		return err
	}
	fresh := read.records.sent()
	c.exporter = c.exporter.Checkpoint(held.Completed, func(done []string) error {
		mark := held.Mark
		mark.Completed = done
		return held.Move(mark)
	})
	err = c.send(ctx, fresh)
	if err != nil && (!errors.Is(err, otel.ErrRejected) || errors.Is(err, otel.ErrRetryable)) {
		return err
	}
	return errors.Join(err, held.Move(otel.Mark{At: fresh.newest(held.At), After: read.positions}))
}

// Sends the records with what they name of earlier records and prints what went out
func (c exportCommand) send(ctx context.Context, recs exportRecords) error {
	b, err := newLookup(ctx, c.stores, recs).batch(recs)
	if err != nil {
		return err
	}
	sent, err := c.exporter.Export(ctx, b)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "exported %d runs, %d verdicts, %d outcomes, %d model calls, %d questions, %d decisions and %d judgments\n",
		sent.Runs, sent.Verdicts, sent.Outcomes, sent.Calls, sent.Questions, sent.Decisions, sent.Judgments)
	return nil
}
