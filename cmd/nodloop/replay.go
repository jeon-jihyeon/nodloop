package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/jeon-jihyeon/nodloop/internal/llm"
	"github.com/jeon-jihyeon/nodloop/internal/replay"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// Judges a version against the outputs it should and should not catch
// One line per case, then a summary line
func (f knowledgeFlags) runReplay(ctx context.Context, id string, a app, client llm.Client, stdout io.Writer) error {
	if id == "" {
		return fmt.Errorf("replay: an id %w", errRequired)
	}
	ledger, err := a.ledger()
	if err != nil {
		return err
	}
	traces, err := a.traces()
	if err != nil {
		return err
	}
	verdicts, err := a.feedback()
	if err != nil {
		return err
	}
	res, err := replay.New(ledger, traces, verdicts, client, f.model, a.now).Replay(ctx, id, f.version)
	if err != nil {
		return err
	}
	corrected, approved := 0, 0
	for _, c := range res.Cases {
		mark := "ok"
		if !c.Passed() {
			mark = "wrong"
		}
		if c.Expect == replay.ExpectBreaks {
			corrected++
		} else {
			approved++
		}
		fmt.Fprintf(stdout, "%s\t%s\tbreaks %t\t%s\t%s\n", c.Run, c.Expect, c.Breaks, mark, c.Why)
	}
	fmt.Fprintf(stdout, "replay\t%s\tv%d\t%s\tmissed %d of %d\toverreach %d of %d\n",
		res.ID, res.Version, outcome(res), res.Missed, corrected, res.Overreach, approved)
	return nil
}

func outcome(r replay.Result) string {
	if r.Passed() {
		return "passed"
	}
	return "failed"
}

// The newest replay of the version as passed or failed, or none when it was never replayed
func (c knowledgeCommand) lastReplay(ctx context.Context, id string, version int) (string, error) {
	traces, err := c.app.traces()
	if err != nil {
		return "", err
	}
	replays, err := traces.List(ctx, trace.Filter{Name: trace.NameReplay, Subject: id})
	if err != nil {
		return "", err
	}
	for _, tr := range replays {
		var res replay.Result
		if json.Unmarshal(tr.Output, &res) == nil && res.Version == version {
			return outcome(res), nil
		}
	}
	return "none", nil
}
