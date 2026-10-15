package main

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/otel"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

const (
	// The endpoint and headers of the OpenTelemetry exporter environment
	// Read when the flags leave them out
	envOTLPTracesEndpoint = "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"
	envOTLPEndpoint       = "OTEL_EXPORTER_OTLP_ENDPOINT"
	envOTLPHeaders        = "OTEL_EXPORTER_OTLP_HEADERS"
	// Time one export request may take before the collector counts as down
	exportTimeout = 30 * time.Second
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

// The headers of OTEL_EXPORTER_OTLP_HEADERS a --header flag left out
// The variable holds comma separated name=value pairs with URL encoded values
func (h *headerFlag) fill(env string) error {
	for pair := range strings.SplitSeq(env, ",") {
		if strings.TrimSpace(pair) == "" {
			continue
		}
		name, value, ok := strings.Cut(pair, "=")
		if !ok {
			return fmt.Errorf("%w: %s holds %q, which is not name=value", errHeaderFlag, envOTLPHeaders, pair)
		}
		decoded, err := url.QueryUnescape(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("%w: %s: %w", errHeaderFlag, envOTLPHeaders, err)
		}
		if _, set := (*h)[strings.TrimSpace(name)]; set {
			continue
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
	fs.Var(&headers, "header", "Name=value of a header to send, repeatable, such as Authorization=Basic <token>. "+envOTLPHeaders+" adds the names it leaves out")
	since := fs.String("since", "", "only runs from this RFC3339 time on")
	output := fs.Bool("with-output", false, "send the output of each run as output.value")
	if err := fs.Parse(args[1:]); err != nil {
		return parseFailed(err)
	}
	if err := headers.fill(getenv(envOTLPHeaders)); err != nil {
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
	exporter := otel.New(target, headers, buildVersion(), &http.Client{Timeout: exportTimeout})
	if err := (exportCommand{app: a, out: stdout}).otel(context.Background(), exporter, start, *output); err != nil {
		return fail(stderr, "export", err)
	}
	return 0
}

type exportCommand struct {
	app app
	out io.Writer
}

// Sends every run since the time with the verdicts and outcomes given since then
// No verdict or outcome on such a run comes before the run so these are all of them
func (c exportCommand) otel(ctx context.Context, exporter *otel.Exporter, since time.Time, output bool) error {
	traces, err := c.app.traces()
	if err != nil {
		return err
	}
	runs, err := traces.List(ctx, record.TraceFilter{Name: string(trace.NameRun), Since: since})
	if err != nil {
		return err
	}
	store, err := c.app.feedback()
	if err != nil {
		return err
	}
	verdicts, err := store.List(ctx, record.FeedbackFilter{Since: since})
	if err != nil {
		return err
	}
	checks, err := c.app.outcomes()
	if err != nil {
		return err
	}
	all, err := checks.List(ctx, "")
	if err != nil {
		return err
	}
	outcomes := slices.DeleteFunc(feedback.RestoreOutcomes(all), func(o feedback.Outcome) bool { return o.Time.Before(since) })
	sent, err := exporter.Export(ctx, exported(trace.RestoreAll(runs), feedback.RestoreAll(verdicts), outcomes), output)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "exported %d runs and %d verdicts and %d outcomes\n", len(runs), len(verdicts), sent)
	return nil
}

// The runs as the exporter sends them with the newest verdict and the newest outcome of each
// 1. a run whose newest verdict withdraws gets no verdict
// 2. an input that does not decode names no item so the run has no arm
func exported(runs trace.Traces, verdicts feedback.Records, outcomes feedback.Outcomes) []otel.Run {
	latest := map[string]feedback.Feedback{}
	for _, v := range verdicts.Latest() {
		latest[v.TraceID] = v
	}
	checked := map[string]feedback.Outcome{}
	for _, o := range outcomes.Latest() {
		checked[o.TraceID] = o
	}
	out := make([]otel.Run, 0, len(runs))
	for _, r := range runs {
		var in knowledge.RunInput
		if json.Unmarshal(r.Input, &in) != nil {
			in = knowledge.RunInput{}
		}
		run := otel.Run{
			ID: r.ID, Time: r.Time, Producer: r.Producer, SessionID: r.SessionID, Subject: r.Subject, Labels: r.Labels,
			Applied: otelRefs(in.Applied), Withheld: otelRefs(in.Withheld), Drawn: in.Drawn, Output: r.Output,
		}
		if v, ok := latest[r.ID]; ok {
			run.Verdict = &otel.Verdict{
				Time: v.Time, Verdict: string(v.Verdict), ReasonCode: string(v.ReasonCode), Reason: v.Reason, Reviewer: v.Reviewer,
			}
		}
		if o, ok := checked[r.ID]; ok {
			run.Outcome = &otel.Outcome{
				Time: o.Time, Result: string(o.Result), ConfirmedCause: o.ConfirmedCause, Note: o.Note, Reviewer: o.Reviewer,
			}
		}
		out = append(out, run)
	}
	return out
}

func otelRefs(refs []knowledge.Ref) []otel.Ref {
	out := make([]otel.Ref, 0, len(refs))
	for _, r := range refs {
		out = append(out, otel.Ref(r))
	}
	return out
}
