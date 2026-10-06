package main

import (
	"context"
	"fmt"
	"io"

	"github.com/jeon-jihyeon/nodloop/internal/extract"
	"github.com/jeon-jihyeon/nodloop/internal/llm"
)

// Drafts the lesson of a corrected run through the model, has a second call criticize it and proposes an add or an update
func (f knowledgeFlags) runExtract(ctx context.Context, a app, client llm.Client, getenv func(string) string, stdout io.Writer) error {
	if f.from == "" {
		return fmt.Errorf("extract: --from %w", errRequired)
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
	critic, err := a.critic(extract.NewClaudeCritic(client, f.model), getenv)
	if err != nil {
		return err
	}
	res, err := extract.New(ledger, traces, verdicts).Extract(ctx, extract.NewClaudeDrafter(client, f.model), critic, f.from, f.author)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "relation\t%s\nlesson\t%s\n", res.Relation, res.Content)
	if res.Related != nil {
		fmt.Fprintf(stdout, "related\t%s\tv%d\t%s\n", res.Related.ID, res.Related.Version, res.Related.Content)
	}
	if res.Candidate != nil {
		k := res.Candidate
		fmt.Fprintf(stdout, "candidate\t%s\tv%d\t%s\nscope\t%s\n", k.ID, k.Version, k.Kind, k.Run)
		for _, o := range res.Overlaps {
			fmt.Fprintf(stdout, "overlap\t%s\tv%d\t%s\n", o.ID, o.Version, o.Content)
		}
	}
	fmt.Fprintf(stdout, "next\t%s\n", extractNext[res.Relation])
	return nil
}

// What the person decides after each relation
var extractNext = map[extract.Relation]string{
	extract.RelationAdd:       "nodloop knowledge approve <id> --version <n> --approver <name> once you agree",
	extract.RelationUpdate:    "compare it with the related item, then nodloop knowledge approve <id> --version <n> --approver <name>",
	extract.RelationDuplicate: "nothing proposed. The related item already says it. nodloop knowledge reaffirm <id> --approver <name> restarts its review deadline",
	extract.RelationConflict:  "nothing proposed. The related item says the opposite. nodloop knowledge narrow or retire decide which stands",
}
