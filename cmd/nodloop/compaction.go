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
	"github.com/jeon-jihyeon/nodloop/internal/replay"
)

// The compaction actions of the knowledge command
// They read runs and verdicts beside the ledger and compact and check call a model
func (f knowledgeFlags) runCompaction(
	ctx context.Context, action, id string, a app, client llm.Client, stdout io.Writer,
) error {
	if id == "" {
		return fmt.Errorf("%s: an id %w", action, errRequired)
	}
	ledger, err := a.ledger()
	if err != nil {
		return err
	}
	compactor, err := a.compactor(ledger)
	if err != nil {
		return err
	}
	cmd := compactionCommand{ledger: ledger, compactor: compactor, client: client, out: stdout, records: a.knowledgeCommand(ledger, stdout)}
	switch action {
	case "compact":
		return cmd.compact(ctx, id, f.model, f.author)
	case "compaction":
		return cmd.show(ctx, id)
	case "check":
		if err := cmd.check(ctx, id, f.model); err != nil || !f.replay {
			return err
		}
		r, err := a.replayer(ledger, client, f.model)
		if err != nil {
			return err
		}
		return cmd.replay(ctx, r, id)
	default:
		return cmd.approve(ctx, id, f.approver)
	}
}

// Results on out
type compactionCommand struct {
	ledger    *knowledge.Ledger
	compactor *compact.Compactor
	client    llm.Client
	records   knowledgeCommand
	out       io.Writer
}

// Drafts through the model and proposes the draft
func (c compactionCommand) compact(ctx context.Context, anchor, model, author string) error {
	f, err := c.compactor.Folder(ctx, anchor)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "folder\t%s\t%d items\n", anchor, len(f.Items))
	proposed, err := c.compactor.Draft(ctx, c.client, f, model, author)
	if err != nil {
		return err
	}
	c.printCompaction(proposed)
	fmt.Fprintf(c.out, "next\tnodloop knowledge check %s reads every old item against the new ones with one model call\n", proposed.ID)
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

// Replays every new item of the compaction and totals what they miss and where they reach too far
// 1. the evidence of a new item joins the corrected runs of the items it replaces
// 2. one line per item and a total read beside the coverage check and never a condition of the approval
func (c compactionCommand) replay(ctx context.Context, r *replay.Replayer, id string) error {
	compaction, err := c.ledger.Compaction(ctx, id)
	if err != nil {
		return err
	}
	missed, overreach, err := c.replayItems(ctx, r, compaction.Items)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "replay\tcompaction %s\tmissed %d\toverreach %d\n", id, missed, overreach)
	return nil
}

// One line per item replayed with the corrected outputs missed and the approved outputs changed summed over them
// 1. a judgment with a veto is skipped since it acts through the guard and reaches no run output a replay could judge
// 2. an item with no recorded output to judge gets a line saying so and the rest go on
func (c compactionCommand) replayItems(ctx context.Context, r *replay.Replayer, items knowledge.Set) (int, int, error) {
	missed, overreach := 0, 0
	for _, k := range items {
		if k.Veto != nil {
			continue
		}
		res, err := r.Replay(ctx, k.ID, k.Version)
		if errors.Is(err, replay.ErrNoCases) {
			fmt.Fprintf(c.out, "replay\t%s\tv%d\tno recorded output to judge\n", k.ID, k.Version)
			continue
		}
		if err != nil {
			return 0, 0, err
		}
		missed, overreach = missed+res.Missed, overreach+res.Overreach
		fmt.Fprintf(c.out, "replay\t%s\tv%d\t%s\tmissed %d\toverreach %d\n", k.ID, k.Version, res.Outcome(), res.Missed, res.Overreach)
	}
	return missed, overreach, nil
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

// The new and old items and the newest coverage check
func (c compactionCommand) show(ctx context.Context, id string) error {
	compaction, err := c.ledger.Compaction(ctx, id)
	if err != nil {
		return err
	}
	c.printCompaction(compaction)
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
