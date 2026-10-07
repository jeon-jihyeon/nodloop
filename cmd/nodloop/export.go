package main

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/otel"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

const (
	// The endpoint and headers of the OpenTelemetry exporter environment, read when the flags leave them out
	envOTLPTracesEndpoint = "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"
	envOTLPEndpoint       = "OTEL_EXPORTER_OTLP_ENDPOINT"
	envOTLPHeaders        = "OTEL_EXPORTER_OTLP_HEADERS"
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

// The headers of OTEL_EXPORTER_OTLP_HEADERS: comma separated name=value pairs with URL encoded values
func (h *headerFlag) setAll(env string) error {
	for pair := range strings.SplitSeq(env, ",") {
		if strings.TrimSpace(pair) == "" {
			continue
		}
		name, value, _ := strings.Cut(pair, "=")
		decoded, err := url.QueryUnescape(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("%w: %s: %w", errHeaderFlag, envOTLPHeaders, err)
		}
		if err := h.Set(strings.TrimSpace(name) + "=" + decoded); err != nil {
			return err
		}
	}
	return nil
}

// export otel sends the runs and verdicts of the records to an OTLP collector
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
	endpoint := fs.String("endpoint", "", "the OTLP traces URL such as http://localhost:4318/v1/traces. "+envOTLPTracesEndpoint+" or "+envOTLPEndpoint+" when empty")
	var headers headerFlag
	fs.Var(&headers, "header", "Name=value of a header to send, repeatable, such as Authorization=Basic <token>. "+envOTLPHeaders+" adds more")
	since := fs.String("since", "", "only runs from this RFC3339 time on")
	output := fs.Bool("with-output", false, "send the output of each run as output.value")
	if err := fs.Parse(args[1:]); err != nil {
		return parseFailed(err)
	}
	if err := headers.setAll(getenv(envOTLPHeaders)); err != nil {
		return fail(stderr, "export", err)
	}
	target := cmp.Or(*endpoint, getenv(envOTLPTracesEndpoint))
	if base := getenv(envOTLPEndpoint); target == "" && base != "" {
		target = strings.TrimSuffix(base, "/") + "/v1/traces"
	}
	if target == "" {
		return fail(stderr, "export", fmt.Errorf("--endpoint %w", errRequired))
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
	exporter := otel.New(target, headers, buildVersion(), &http.Client{Timeout: 30 * time.Second})
	if err := (exportCommand{app: a, out: stdout}).otel(context.Background(), exporter, start, *output); err != nil {
		return fail(stderr, "export", err)
	}
	return 0
}

type exportCommand struct {
	app app
	out io.Writer
}

// Sends every run since the time with the verdicts given since then, which are all the verdicts those runs have
func (c exportCommand) otel(ctx context.Context, exporter *otel.Exporter, since time.Time, output bool) error {
	traces, err := c.app.traces()
	if err != nil {
		return err
	}
	runs, err := traces.List(ctx, trace.Filter{Name: trace.NameRun, Since: since})
	if err != nil {
		return err
	}
	store, err := c.app.feedback()
	if err != nil {
		return err
	}
	verdicts, err := store.List(ctx, feedback.Filter{Since: since})
	if err != nil {
		return err
	}
	if err := exporter.Export(ctx, runs, verdicts, output); err != nil {
		return err
	}
	fmt.Fprintf(c.out, "exported %d runs and %d verdicts\n", len(runs), len(verdicts))
	return nil
}
