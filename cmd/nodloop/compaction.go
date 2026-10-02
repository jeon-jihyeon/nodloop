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
	cmd, err := a.compactionCommand(client, stdout, stderr)
	if err != nil {
		return err
	}
	switch action {
	case "compact":
		return cmd.compact(ctx, id, f.model, f.author)
	case "compaction":
		return cmd.show(ctx, id)
	case "check":
		return cmd.check(ctx, id, f.model)
	case "replay":
		return cmd.replay(ctx, id, compact.ReplayOptions{Events: f.events, Parallel: f.parallel, Model: f.model, Log: stderr})
	default:
		return cmd.approve(ctx, id, f.approver)
	}
}

// The command over the whole pipeline with a data dir and over the records alone without one
// Without a data dir only a folder of run items compacts and a replay fails naming the data dir
func (a app) compactionCommand(client llm.Client, stdout, stderr io.Writer) (compactionCommand, error) {
	if a.cfg.dataDir != "" {
		p, err := a.pipeline()
		if err != nil {
			return compactionCommand{}, err
		}
		return compactionCommand{
			pipeline: p, ledger: p.ledger, compactor: p.compactor(), client: client, out: stdout, log: stderr,
			records: a.knowledgeCommand(p.ledger, stdout),
		}, nil
	}
	ledger, err := a.ledger()
	if err != nil {
		return compactionCommand{}, err
	}
	compactor, err := a.compactor(ledger)
	if err != nil {
		return compactionCommand{}, err
	}
	return compactionCommand{
		ledger: ledger, compactor: compactor, client: client, out: stdout, log: stderr, records: a.knowledgeCommand(ledger, stdout),
	}, nil
}

// Results on out and replay progress on log
type compactionCommand struct {
	// Zero without a data dir
	pipeline  pipeline
	ledger    *knowledge.Ledger
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
	if f.Runs {
		fmt.Fprintf(c.out, "next\tnodloop knowledge check %s reads every old item against the new ones with one model call\n", proposed.ID)
		return nil
	}
	fmt.Fprintf(c.out, "next\tnodloop knowledge replay %s reviews %d events with one or two model calls each\n",
		proposed.ID, len(f.Replay))
	return nil
}

// One model call checks that the new items state every old item and the answer is recorded for the approval
func (c compactionCommand) check(ctx context.Context, id, model string) error {
	cov, err := c.compactor.Check(ctx, c.client, id, model)
	if err != nil {
		return err
	}
	c.printCoverage(cov)
	return nil
}

func (c compactionCommand) printCoverage(cov knowledge.Coverage) {
	for _, it := range cov.Items {
		by := make([]string, 0, len(it.CoveredBy))
		for _, ref := range it.CoveredBy {
			by = append(by, fmt.Sprintf("%s v%d", ref.ID, ref.Version))
		}
		fmt.Fprintf(c.out, "covered\t%s v%d\tby %s\tlost %s\n", it.Old.ID, it.Old.Version,
			cmp.Or(strings.Join(by, ", "), "-"), cmp.Or(strings.Join(it.Lost, "; "), "-"))
	}
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
	compaction, err := c.ledger.Compaction(ctx, id)
	if err != nil {
		return err
	}
	c.printCompaction(compaction)
	if compaction.Items[0].Run != nil {
		cov, err := c.compactor.Coverage(ctx, id)
		if errors.Is(err, compact.ErrNoCoverage) {
			fmt.Fprintf(c.out, "coverage\tnone yet. Run nodloop knowledge check %s\n", id)
			return nil
		}
		if err != nil {
			return err
		}
		c.printCoverage(cov)
		return nil
	}
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
	if c.pipeline.src == nil {
		return fmt.Errorf("%w: %s", compact.ErrNoData, id)
	}
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
	before, err := c.ledger.All(ctx)
	if err != nil {
		return err
	}
	compaction, err := c.compactor.Approve(ctx, id, approver)
	if err != nil && !errors.Is(err, knowledge.ErrExport) {
		return err
	}
	c.printCompaction(compaction)
	if err != nil {
		return err
	}
	after, err := c.ledger.All(ctx)
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
