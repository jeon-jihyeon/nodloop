package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/trace"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
)

func runTrace(args []string, getenv func(string) string, now func() time.Time, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return fail(stderr, "trace", errNoAction)
	}
	fs := flag.NewFlagSet("trace "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	var data dataFlags
	data.bind(fs)
	var f trace.Filter
	name := fs.String("name", "", "pipeline name such as diagnose")
	fs.StringVar(&f.SessionID, "session", "", "session id")
	fs.StringVar(&f.Subject, "subject", "", "subject such as an event id")
	fs.IntVar(&f.Limit, "limit", 0, "newest n traces. 0 means all")
	id, err := parseID(fs, args[1:])
	if err != nil {
		return 1
	}
	f.Name = trace.Name(*name)
	if *name != "" && !f.Name.Valid() {
		names := make([]string, 0, len(trace.Names()))
		for _, n := range trace.Names() {
			names = append(names, string(n))
		}
		err := fmt.Errorf("%w %q. Use one of %s", errUnknownTraceName, *name, strings.Join(names, ", "))
		return fail(stderr, "trace", err)
	}
	a, err := data.app(getenv, now)
	if err != nil {
		return fail(stderr, "trace", err)
	}
	store, err := a.traces()
	if err != nil {
		return fail(stderr, "trace", err)
	}
	cmd := traceCommand{store: store, out: stdout}
	ctx := context.Background()
	switch args[0] {
	case "list":
		err = cmd.list(ctx, f)
	case "pending":
		err = cmd.pending(ctx)
	case "show":
		err = cmd.show(ctx, id)
	default:
		err = fmt.Errorf("%w %q", errUnknownAction, args[0])
	}
	if err != nil {
		return fail(stderr, "trace", err)
	}
	return 0
}

type traceCommand struct {
	store *tracefile.Store
	out   io.Writer
}

func (c traceCommand) list(ctx context.Context, f trace.Filter) error {
	traces, err := c.store.List(ctx, f)
	if err != nil {
		return err
	}
	for _, t := range traces {
		fmt.Fprintf(c.out, "%s\t%s\t%s\t%s\t$%.4f\n", t.ID, t.Time.Format(time.RFC3339), t.Name, t.Subject, t.Usage.CostUSD)
	}
	return nil
}

func (c traceCommand) pending(ctx context.Context) error {
	all, err := c.store.List(ctx, trace.Filter{})
	if err != nil {
		return err
	}
	for _, t := range all.Pending() {
		fmt.Fprintf(c.out, "%s\t%s\t%s\n", t.ID, t.Time.Format(time.RFC3339), t.Subject)
	}
	return nil
}

func (c traceCommand) show(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("an id %w", errRequired)
	}
	t, err := c.store.Get(ctx, id)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(c.out, string(b))
	return nil
}
