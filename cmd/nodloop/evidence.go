package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	evidencefile "github.com/jeon-jihyeon/nodloop/internal/evidence/file"
)

func runEvidence(args []string, getenv func(string) string, now func() time.Time, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return fail(stderr, "evidence", errNoAction)
	}
	fs := flag.NewFlagSet("evidence "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	var data dataFlags
	data.bind(fs)
	eventID := fs.String("id", "", "event id for event")
	if err := fs.Parse(args[1:]); err != nil {
		return 1
	}
	a, err := data.app(getenv, now)
	if err != nil {
		return fail(stderr, "evidence", err)
	}
	src, err := a.source()
	if err != nil {
		return fail(stderr, "evidence", err)
	}
	cmd := evidenceCommand{src: src, out: stdout}
	ctx := context.Background()
	switch args[0] {
	case "events":
		err = cmd.events(ctx)
	case "event":
		err = cmd.event(ctx, *eventID)
	case "paragraphs":
		err = cmd.paragraphs(ctx)
	case "labels":
		err = cmd.labels(ctx)
	default:
		err = fmt.Errorf("%w %q", errUnknownAction, args[0])
	}
	if err != nil {
		return fail(stderr, "evidence", err)
	}
	return 0
}

type evidenceCommand struct {
	src *evidencefile.Source
	out io.Writer
}

func (c evidenceCommand) events(ctx context.Context) error {
	refs, err := c.src.Events(ctx)
	if err != nil {
		return err
	}
	for _, r := range refs {
		fmt.Fprintf(c.out, "%s\t%s\t%s\n", r.ID, r.Start.Format(time.RFC3339), r.End.Format(time.RFC3339))
	}
	return nil
}

// The change context and one line per series with its point count
func (c evidenceCommand) event(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("--id %w", errRequired)
	}
	ev, err := c.src.Event(ctx, id)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "%s\tcontext=%s\n", ev.ID, ev.ChangeContext)
	counts := ev.SeriesCounts()
	for _, k := range slices.Sorted(maps.Keys(counts)) {
		fmt.Fprintf(c.out, "%s\tpoints=%d\n", k, counts[k])
	}
	return nil
}

func (c evidenceCommand) paragraphs(ctx context.Context) error {
	ps, err := c.src.Paragraphs(ctx)
	if err != nil {
		return err
	}
	for _, p := range ps {
		fmt.Fprintln(c.out, p.ID)
	}
	return nil
}

func (c evidenceCommand) labels(ctx context.Context) error {
	labels, err := c.src.Labels(ctx)
	if err != nil {
		return err
	}
	for _, l := range labels {
		paragraphs := make([]string, len(l.Paragraphs))
		for i, id := range l.Paragraphs {
			paragraphs[i] = string(id)
		}
		fmt.Fprintf(c.out, "%s\t%s\tseed=%t\thold=%t\tanomalies=%d\tparagraphs=%s\n",
			l.EventID, l.Type, l.Seed, l.IsHold(), len(l.Anomalies), strings.Join(paragraphs, ","))
	}
	return nil
}
