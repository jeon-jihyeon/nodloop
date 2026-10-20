package main

import (
	"context"
	"fmt"
	"io"

	"github.com/jeon-jihyeon/nodloop/internal/consolidate"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/llm"
)

// Waiting candidates the ledger holds before the stop hook starts a daily consolidate
// Fewer have nothing to group
const consolidateMin = 2

// Records groups of waiting candidates that may say the same and prints one line per group
// 1. the mark is claimed before the calls so a run that fails waits a day
// 2. --daily runs only when the mark is a day old and prints nothing since the stop hook starts it with its output in hook.log
// 3. without --daily the run claims a fresh mark too so the next daily run waits a day
// The lock is held only while the mark is written so a run beside another one only records a second trace
func (f knowledgeFlags) runConsolidate(ctx context.Context, a app, client llm.Client, stdout io.Writer) error {
	dir, err := a.makeRecordDir()
	if err != nil {
		return err
	}
	every := consolidate.Interval
	if !f.daily {
		every = 0
	}
	claimed, err := consolidate.NewMarker(dir).Claim(a.now(), every)
	if err != nil || (!claimed && f.daily) {
		return err
	}
	if !claimed {
		return errConsolidateRunning
	}
	ledger, err := a.ledger()
	if err != nil {
		return err
	}
	traces, err := a.traces()
	if err != nil {
		return err
	}
	results, err := consolidate.New(ledger, traces, client, f.model, a.now).Run(ctx)
	if f.daily {
		return err
	}
	for _, res := range results {
		for _, g := range res.Groups {
			fmt.Fprintf(stdout, "group\t%s\t%s\t%s\n", res.Producer, g, g.Why)
		}
	}
	return err
}

// Starts knowledge consolidate --daily in a process of its own when its mark is a day old
// 1. only a mode that infers starts it since manual leaves every candidate to the person
// 2. the mark is read here without its lock so most turns start nothing and the started process claims it
// 3. fewer than consolidateMin waiting candidates start nothing since no pair can say the same
// The record directory exists once the stop read the ledger whose candidates it counts
func (c hookCommand) consolidate(all knowledge.Set) error {
	if !c.mode.infers() || len(all.AllWaiting()) < consolidateMin {
		return nil
	}
	due, err := consolidate.NewMarker(c.app.cfg.recordDir).Due(c.app.now())
	if err != nil || !due {
		return err
	}
	return c.detach([]string{"knowledge", "consolidate", "--daily"})
}
