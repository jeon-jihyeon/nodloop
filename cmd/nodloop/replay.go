package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/llm"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/replay"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// Judges a version against the outputs it should and should not catch
// One line per case and then a summary line
func (f knowledgeFlags) runReplay(ctx context.Context, id string, a app, client llm.Client, stdout io.Writer) error {
	if id == "" {
		return fmt.Errorf("replay: an id %w", errRequired)
	}
	ledger, err := a.ledger()
	if err != nil {
		return err
	}
	r, err := a.replayer(ledger, client, f.model)
	if err != nil {
		return err
	}
	res, err := r.Replay(ctx, id, f.version)
	if err != nil {
		return err
	}
	for _, c := range res.Cases {
		mark := "ok"
		if !c.Passed() {
			mark = "wrong"
		}
		fmt.Fprintf(stdout, "%s\t%s\tbreaks %t\t%s\t%s\n", c.Run, c.Expect, c.Breaks, mark, c.Why)
	}
	fmt.Fprintf(stdout, "replay\t%s\tv%d\t%s\n", res.ID, res.Version, replayOutcome(res, "\t"))
	return nil
}

// The outcome with the cases each axis missed of those judged
// An axis that judged no case reads 0 of 0 so the person sees what partial left untested
func replayOutcome(res replay.Result, sep string) string {
	return strings.Join([]string{
		string(res.Outcome()), fmt.Sprintf("missed %d of %d", res.Missed, res.Corrected()),
		fmt.Sprintf("overreach %d of %d", res.Overreach, res.Approved()),
	}, sep)
}

// The summary of the newest replay of each version
type lastReplays map[knowledge.Ref]string

// Reads the replay traces once for every waiting item
// 1. a replay that failed records its error and no result so it is skipped as no judgment
// 2. a result that does not decode fails since a waiting item would otherwise show none for a replay it had
func (c knowledgeCommand) lastReplays(ctx context.Context) (lastReplays, error) {
	traces, err := c.app.traces()
	if err != nil {
		return nil, err
	}
	replays, err := traces.List(ctx, record.TraceFilter{Name: string(trace.NameReplay)})
	if err != nil {
		return nil, err
	}
	out := lastReplays{}
	for _, tr := range trace.RestoreAll(replays) {
		if tr.Error != "" {
			continue
		}
		var res replay.Result
		if err := json.Unmarshal(tr.Output, &res); err != nil {
			return nil, fmt.Errorf("%w: %s: %w", errReplayUndecodable, tr.ID, err)
		}
		ref := knowledge.Ref{ID: res.ID, Version: res.Version}
		if _, newer := out[ref]; !newer {
			out[ref] = replayOutcome(res, " ")
		}
	}
	return out, nil
}

// The outcome and counts of the newest replay of the version and none when it was never replayed
func (l lastReplays) of(id string, version int) string {
	if summary, ok := l[knowledge.Ref{ID: id, Version: version}]; ok {
		return summary
	}
	return "none"
}
