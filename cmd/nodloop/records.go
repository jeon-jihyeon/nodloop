package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/retain"
)

func runRecords(args []string, getenv func(string) string, now func() time.Time, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return fail(stderr, "records", errNoAction)
	}
	fs := newFlagSet("records "+args[0], stderr)
	var records recordFlags
	records.bind(fs)
	dryRun := fs.Bool("dry-run", false, "print what a prune would remove and remove nothing")
	daily := fs.Bool("daily", false, "prune only when the last prune of the directory is a day old and print nothing, as the stop hook starts it")
	if err := fs.Parse(args[1:]); err != nil {
		return parseFailed(err)
	}
	a, err := records.app(getenv, now)
	if err != nil {
		return fail(stderr, "records", err)
	}
	cmd := recordsCommand{app: a, out: stdout}
	ctx := context.Background()
	switch {
	case args[0] == "prune" && *daily && *dryRun:
		err = errPruneFlags
	case args[0] == "prune" && *daily:
		err = a.dailyPrune(ctx)
	case args[0] == "prune" && *dryRun:
		err = cmd.plan(ctx)
	case args[0] == "prune":
		err = cmd.prune(ctx)
	default:
		err = fmt.Errorf("%w %q", errUnknownAction, args[0])
	}
	if err != nil {
		return fail(stderr, "records", err)
	}
	return 0
}

type recordsCommand struct {
	app app
	out io.Writer
}

// The retainer and the cutoff of the retention set in config.json
func (c recordsCommand) retention() (*retain.Retainer, time.Time, error) {
	days := c.app.cfg.retention
	if days == 0 {
		return nil, time.Time{}, errRetentionUnset
	}
	if err := days.Check(); err != nil {
		return nil, time.Time{}, err
	}
	retainer, err := c.app.retainer()
	if err != nil {
		return nil, time.Time{}, err
	}
	return retainer, days.Cutoff(c.app.now()), nil
}

// Prints what a prune would remove and keep and removes nothing
func (c recordsCommand) plan(ctx context.Context) error {
	retainer, cutoff, err := c.retention()
	if err != nil {
		return err
	}
	plan, err := retainer.Plan(ctx, cutoff)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "would roll up %d closed cycles and prune %d runs, %d classify traces and %d turn, cue, calls, instructions and capture traces older than %s, "+
		"kept %d judged, cited or first or last to use an item\n",
		len(plan.Rollups), len(plan.Runs), len(plan.Classify), len(plan.Session)+len(plan.Calls), plan.Cutoff.Format(time.RFC3339), plan.Kept)
	return nil
}

// Removes the runs past retention whatever the daily marker says and prints how many traces the store removed
// 1. the marker keeps the cutoff of the retention so the reports over the directory read from it on
// 2. the prune keeps to the start of the open cycle when that is earlier so it never removes a run after the mark
// 3. the marker is claimed only once the plan read every store so a plan that fails leaves the window as it was
// 4. a marker another process holds means a prune runs there and this one removes nothing
func (c recordsCommand) prune(ctx context.Context) error {
	retainer, cutoff, err := c.retention()
	if err != nil {
		return err
	}
	plan, err := retainer.Plan(ctx, cutoff)
	if err != nil {
		return err
	}
	// The record directory exists once the retainer opened its stores
	claimed, err := retain.NewMarker(c.app.cfg.recordDir).Claim(ctx, c.app.now(), cutoff, 0)
	if err != nil {
		return err
	}
	if !claimed {
		return errPruneRunning
	}
	removed, err := retainer.Apply(ctx, plan)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "pruned %d traces older than %s\n", removed, plan.Cutoff.Format(time.RFC3339))
	return nil
}
