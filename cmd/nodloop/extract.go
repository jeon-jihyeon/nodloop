package main

import (
	"context"
	"fmt"
	"io"

	"github.com/jeon-jihyeon/nodloop/internal/extract"
	"github.com/jeon-jihyeon/nodloop/internal/llm"
)

// Drafts the lesson of a corrected run through the model and proposes an add or an update
// The critic is the claude critic or the classifiers the user set up for the critic point
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
	// One meter under the drafter and the claude critic so the extract trace sums both
	meter := llm.NewMeter(client)
	critic, err := a.critic(extract.NewClaudeCritic(meter, f.model), getenv)
	if err != nil {
		return err
	}
	res, err := extract.New(ledger, traces, verdicts, a.now).Extract(ctx, extract.NewClaudeDrafter(meter, f.model), critic, f.from, f.author)
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
	next := extractNext[res.Relation]
	if res.Corroborated() {
		next = extractCorroborated
	}
	fmt.Fprintf(stdout, "next\t%s\n", next)
	return nil
}

// What happens after a duplicate of a waiting candidate
const extractCorroborated = "nothing new proposed. The waiting candidate already says it and this run joined its evidence, so it waits for the person as before"

// What happens after each relation
// No line names a command that decides
// A model that read one decided without asking in 4 headless runs
var extractNext = map[extract.Relation]string{
	extract.RelationAdd:       "the candidate waits for the person, who is asked about it once unless their earlier decisions settle it",
	extract.RelationUpdate:    "compare it with the related item. The candidate waits for the person, who is asked about it once unless their earlier decisions settle it",
	extract.RelationDuplicate: "nothing proposed. The related item already says it",
	extract.RelationConflict:  "nothing proposed. The related item or candidate says the opposite, and the person decides which stands",
}
