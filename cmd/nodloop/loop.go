package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/loop"
)

// The conversation records joined once for the loop views
func (a app) history(ctx context.Context) (*loop.History, error) {
	traces, err := a.traces()
	if err != nil {
		return nil, err
	}
	verdicts, err := a.feedback()
	if err != nil {
		return nil, err
	}
	outcomes, err := a.outcomes()
	if err != nil {
		return nil, err
	}
	ledger, err := a.ledger()
	if err != nil {
		return nil, err
	}
	return loop.Load(ctx, traces, verdicts, outcomes, ledger)
}

// One JSON row per knowledge version
func (c knowledgeCommand) health(ctx context.Context) error {
	h, err := c.app.history(ctx)
	if err != nil {
		return err
	}
	return json.NewEncoder(c.out).Encode(h.Health(c.app.now()))
}

// One JSON issue per broken reference of a current item
func (c knowledgeCommand) audit(ctx context.Context) error {
	h, err := c.app.history(ctx)
	if err != nil {
		return err
	}
	src, err := c.app.source()
	if err != nil {
		return err
	}
	procedures, err := src.Procedures(ctx)
	if err != nil {
		return err
	}
	metrics, err := src.Metrics(ctx)
	if err != nil {
		return err
	}
	return json.NewEncoder(c.out).Encode(h.BrokenReferences(procedures, metrics))
}

// Without a version the approved version of the id is the one reaffirmed
func (c knowledgeCommand) reaffirm(ctx context.Context, id string, version int, approver string) error {
	if id == "" || approver == "" {
		return fmt.Errorf("reaffirm: an id and --approver %w", errRequired)
	}
	if version == 0 {
		var err error
		if version, err = c.ledger.ApprovedVersion(ctx, id); err != nil {
			return err
		}
	}
	k, err := c.ledger.Reaffirm(ctx, id, version, approver)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "%s\tv%d\treaffirmed\t%s\n", k.ID, k.Version, k.Approver)
	return nil
}

// Proposes the next version without the change contexts where its conversation reviews were refuted
// Prints the candidate like propose so the person sees what approval would change
func (c knowledgeCommand) narrow(ctx context.Context, id string, version int, author string) error {
	if id == "" || version <= 0 {
		return fmt.Errorf("narrow: an id and --version %w", errRequired)
	}
	h, err := c.app.history(ctx)
	if err != nil {
		return err
	}
	k, overlaps, err := c.ledger.Narrow(ctx, id, version, h.RefutedContexts(id, version), h.RefutedTraces(id, version), author)
	if errors.Is(err, knowledge.ErrNarrowExhausted) {
		return fmt.Errorf("%w. Narrowing cannot help because every change context of the version was refuted. "+
			"Keep the version or retire it by name with nodloop knowledge retire %s --version %d --approver <name>",
			err, id, version)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "%s\tv%d\t%s\tscope %s\texceptions %v\n", k.ID, k.Version, k.Status, k.Scope, k.Exceptions)
	for _, o := range overlaps {
		fmt.Fprintf(c.out, "overlaps\t%s\tv%d\t%s\n", o.ID, o.Version, o.Status)
	}
	return c.folder(ctx, k.ID, k.Version)
}

// The queue and the online report print JSON
type loopCommand struct {
	app app
	out io.Writer
}

func (c loopCommand) queue(ctx context.Context, opts loop.QueueOptions) error {
	h, err := c.app.history(ctx)
	if err != nil {
		return err
	}
	items, err := h.Queue(opts)
	if err != nil {
		return err
	}
	return json.NewEncoder(c.out).Encode(map[string]any{"seed": opts.Seed, "items": items})
}

func (c loopCommand) online(ctx context.Context, since time.Time) error {
	h, err := c.app.history(ctx)
	if err != nil {
		return err
	}
	return json.NewEncoder(c.out).Encode(h.Report(since))
}

func runQueue(args []string, getenv func(string) string, now func() time.Time, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("queue", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var data dataFlags
	data.bind(fs)
	opts := loop.QueueOptions{}
	fs.IntVar(&opts.Limit, "limit", loop.QueueLimit, "reviews to list including audit samples. 0 lists every review")
	fs.Float64Var(&opts.AuditRate, "audit-rate", loop.QueueAuditRate, "share of the limit drawn at random from the rest of the order")
	fs.Int64Var(&opts.Seed, "seed", now().UnixNano(), "the same seed draws the same audit samples")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() != 0 {
		return fail(stderr, "queue", fmt.Errorf("%w %q", errUnknownAction, fs.Arg(0)))
	}
	a, err := data.app(getenv, now)
	if err != nil {
		return fail(stderr, "queue", err)
	}
	if err := (loopCommand{app: a, out: stdout}).queue(context.Background(), opts); err != nil {
		return fail(stderr, "queue", err)
	}
	return 0
}

func runReport(args []string, getenv func(string) string, now func() time.Time, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return fail(stderr, "report", errNoAction)
	}
	if args[0] != "online" {
		return fail(stderr, "report", fmt.Errorf("%w %q", errUnknownAction, args[0]))
	}
	fs := flag.NewFlagSet("report online", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var data dataFlags
	data.bind(fs)
	since := fs.String("since", "", "only verdicts and outcomes from this RFC3339 time on")
	if err := fs.Parse(args[1:]); err != nil {
		return 1
	}
	if fs.NArg() != 0 {
		return fail(stderr, "report", fmt.Errorf("%w %q", errUnknownAction, fs.Arg(0)))
	}
	var start time.Time
	if *since != "" {
		var err error
		if start, err = time.Parse(time.RFC3339, *since); err != nil {
			return fail(stderr, "report", err)
		}
	}
	a, err := data.app(getenv, now)
	if err != nil {
		return fail(stderr, "report", err)
	}
	if err := (loopCommand{app: a, out: stdout}).online(context.Background(), start); err != nil {
		return fail(stderr, "report", err)
	}
	return 0
}
