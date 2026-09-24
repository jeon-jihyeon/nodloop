package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/llm"
)

const probeSchema = `{"type":"object","properties":{"answer":{"type":"integer"}},"required":["answer"]}`

func runLLM(args []string, client llm.Client, now func() time.Time, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return fail(stderr, "llm", errNoAction)
	}
	if args[0] != "probe" {
		return fail(stderr, "llm", fmt.Errorf("%w %q", errUnknownAction, args[0]))
	}
	fs := flag.NewFlagSet("llm probe", flag.ContinueOnError)
	fs.SetOutput(stderr)
	// The cheapest model because the probe only checks login and structured output
	model := fs.String("model", "haiku", "model alias or name")
	if err := fs.Parse(args[1:]); err != nil {
		return 1
	}
	cmd := llmCommand{client: client, now: now, out: stdout}
	if err := cmd.probe(context.Background(), *model); err != nil {
		return fail(stderr, "llm probe", err)
	}
	return 0
}

type llmCommand struct {
	client llm.Client
	now    func() time.Time
	out    io.Writer
}

// Minimal real request that checks login and structured output and cost in one call
func (c llmCommand) probe(ctx context.Context, model string) error {
	start := c.now()
	res, err := c.client.Complete(ctx, llm.Request{
		Prompt: "Return the number 42 as the field answer.",
		Schema: json.RawMessage(probeSchema),
		Model:  model,
	})
	if err != nil {
		return err
	}
	var out struct {
		Answer int `json:"answer"`
	}
	if err := json.Unmarshal(res.Output, &out); err != nil {
		return fmt.Errorf("%w: %s: %w", errUnexpectedOutput, res.Output, err)
	}
	if out.Answer != 42 {
		return fmt.Errorf("%w: %d in %s", errWrongAnswer, out.Answer, res.Output)
	}
	fmt.Fprintf(c.out, "output %s\ncost $%.4f\ntokens in %d out %d cache_read %d cache_create %d\nduration %s (wall %s)\n",
		res.Output, res.CostUSD, res.InputTokens, res.OutputTokens, res.CacheRead, res.CacheCreate,
		res.Duration.Round(time.Millisecond), c.now().Sub(start).Round(time.Millisecond))
	return nil
}
