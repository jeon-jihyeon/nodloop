package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"

	"github.com/jeon-jihyeon/nodloop/internal/compact"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/llm"
)

// The compaction actions of the knowledge command
// They review and so open the whole pipeline while the other knowledge actions read the records alone
func (f knowledgeFlags) runCompaction(
	ctx context.Context, action, id string, a app, client llm.Client, stdout, stderr io.Writer,
) error {
	if id == "" {
		return fmt.Errorf("%s: an id %w", action, errRequired)
	}
	p, err := a.pipeline()
	if err != nil {
		return err
	}
	cmd := compactionCommand{
		pipeline: p, compactor: p.compactor(), client: client, out: stdout, log: stderr,
		records: a.knowledgeCommand(p.ledger, stdout),
	}
	switch action {
	case "compact":
		return cmd.compact(ctx, id, f.model, f.author)
	case "compaction":
		return cmd.show(ctx, id)
	case "replay":
		return cmd.replay(ctx, id, compact.ReplayOptions{Events: f.events, Parallel: f.parallel, Model: f.model, Log: stderr})
	default:
		return cmd.approve(ctx, id, f.approver)
	}
}

// Results on out and replay progress on log
type compactionCommand struct {
	pipeline  pipeline
	compactor *compact.Compactor
	client    llm.Client
	records   knowledgeCommand
	out, log  io.Writer
}

// Drafts through the model and proposes the draft
// The folder and the replay size print first so the person sees what the replay will cost before running it
func (c compactionCommand) compact(ctx context.Context, anchor, model, author string) error {
	f, err := c.compactor.Folder(ctx, anchor)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "folder\t%s\t%d items\t%d replay events\t%d unverifiable events\n",
		anchor, len(f.Items), len(f.Replay), len(f.Unverifiable))
	for _, k := range f.Excluded {
		fmt.Fprintf(c.out, "excluded\t%s\tv%d\tcites only procedure paragraphs so no event can replay it\n", k.ID, k.Version)
	}
	proposed, err := c.compactor.Draft(ctx, c.client, f, model, author)
	if err != nil {
		return err
	}
	c.printCompaction(proposed)
	fmt.Fprintf(c.out, "next\tnodloop knowledge replay %s reviews %d events with one or two model calls each\n",
		proposed.ID, len(f.Replay))
	return nil
}

func (c compactionCommand) printCompaction(compaction knowledge.Compaction) {
	fmt.Fprintf(c.out, "compaction\t%s\n", compaction.ID)
	for _, k := range compaction.Items {
		fmt.Fprintf(c.out, "new\t%s\tv%d\t%s\t%s\t%s\n", k.ID, k.Version, k.Status, k.Kind, k.Content)
	}
	for _, k := range compaction.Replaced {
		fmt.Fprintf(c.out, "old\t%s\tv%d\t%s\t%s\t%s\n", k.ID, k.Version, k.Status, k.Kind, k.Content)
	}
}

// The new and old items and the replay result as it reads now
func (c compactionCommand) show(ctx context.Context, id string) error {
	compaction, err := c.pipeline.ledger.Compaction(ctx, id)
	if err != nil {
		return err
	}
	c.printCompaction(compaction)
	r, err := c.compactor.Result(ctx, id)
	if err != nil {
		return err
	}
	c.printReplay(r)
	return nil
}

func (c compactionCommand) printReplay(r knowledge.Replay) {
	for _, e := range r.Events {
		fmt.Fprintf(c.out, "event\t%s\texpected %s\tgot %s\t%s\n", e.EventID, e.Expected, cmp.Or(string(e.Got), "-"), cmp.Or(e.TraceID, "-"))
	}
	fmt.Fprintf(c.out, "replay\tpassed=%t\n", r.Passed())
}

// The event count prints before any model call
func (c compactionCommand) replay(ctx context.Context, id string, opts compact.ReplayOptions) error {
	expected, err := c.compactor.Expectations(ctx, id)
	if err != nil {
		return err
	}
	count := len(expected)
	if len(opts.Events) > 0 {
		count = len(opts.Events)
	}
	fmt.Fprintf(c.out, "replay\t%s\t%d events\n", id, count)
	d, err := c.pipeline.replayDiagnoser(ctx, c.client, id)
	if err != nil {
		return err
	}
	r, err := c.compactor.Replay(ctx, d, id, opts)
	if err != nil {
		return err
	}
	c.printReplay(r)
	return nil
}

// A failed veto export still prints the records because they are written and the error follows
func (c compactionCommand) approve(ctx context.Context, id, approver string) error {
	if approver == "" {
		return fmt.Errorf("approve-compaction: --approver %w", errRequired)
	}
	before, err := c.pipeline.ledger.All(ctx)
	if err != nil {
		return err
	}
	compaction, err := c.compactor.Approve(ctx, id, approver)
	if err != nil && !errors.Is(err, knowledge.ErrVetoExport) {
		return err
	}
	c.printCompaction(compaction)
	if err != nil {
		return err
	}
	after, err := c.pipeline.ledger.All(ctx)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(before.Vetoes(), after.Vetoes()) {
		c.records.vetoLine(len(after.Vetoes()))
	}
	return nil
}

// Comma separated ids of a repeatable flag
// Empty entries are dropped so a trailing comma names no empty id
type idList []string

func (l *idList) String() string { return strings.Join(*l, ",") }

func (l *idList) Set(v string) error {
	for _, id := range strings.Split(v, ",") {
		if id != "" {
			*l = append(*l, id)
		}
	}
	return nil
}
