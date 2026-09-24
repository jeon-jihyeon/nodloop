package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/analysis"
)

func runAnalysis(args []string, getenv func(string) string, now func() time.Time, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return fail(stderr, "analysis", errNoAction)
	}
	fs := flag.NewFlagSet("analysis "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	var data dataFlags
	data.bind(fs)
	eventID := fs.String("event", "", "event id for observe")
	if err := fs.Parse(args[1:]); err != nil {
		return 1
	}
	a, err := data.app(getenv, now)
	if err != nil {
		return fail(stderr, "analysis", err)
	}
	policy, _, err := a.policy()
	if err != nil {
		return fail(stderr, "analysis", err)
	}
	cmd := analysisCommand{app: a, policy: policy, out: stdout}
	switch args[0] {
	case "policy":
		err = cmd.print()
	case "observe":
		err = cmd.observe(context.Background(), *eventID)
	default:
		err = fmt.Errorf("%w %q", errUnknownAction, args[0])
	}
	if err != nil {
		return fail(stderr, "analysis", err)
	}
	return 0
}

// The source is opened only for observe so policy prints without reading the data directory
type analysisCommand struct {
	app    app
	policy analysis.Policy
	out    io.Writer
}

func (c analysisCommand) print() error {
	b, err := json.MarshalIndent(c.policy, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(c.out, string(b))
	return nil
}

func (c analysisCommand) observe(ctx context.Context, eventID string) error {
	if eventID == "" {
		return fmt.Errorf("--event %w", errRequired)
	}
	src, err := c.app.source()
	if err != nil {
		return err
	}
	ev, err := src.Event(ctx, eventID)
	if err != nil {
		return err
	}
	obs, err := c.policy.Analyze(ev)
	if err != nil {
		return err
	}
	for _, o := range obs {
		fmt.Fprintf(c.out, "%s\t%s\tseverity=%.2f\tadequate=%t\t%s\n", o.Rule, o.Metric, o.Severity, o.Adequate, o.Summary)
	}
	return nil
}
