package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/diagnose"
	"github.com/jeon-jihyeon/nodloop/internal/llm"
)

func runDiagnose(
	args []string, getenv func(string) string, client llm.Client, now func() time.Time, stdout, stderr io.Writer,
) int {
	fs := flag.NewFlagSet("diagnose", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var data dataFlags
	data.bind(fs)
	eventID := fs.String("event", "", "event id")
	var opts diagnose.BatchOptions
	fs.IntVar(&opts.Examples, "examples", defaultExamples, "past edit and reject feedback injected as examples. 0 or less means none")
	knowledgeMode := fs.String("knowledge", string(diagnose.KnowledgeSelected), "none, selected or all approved knowledge in the context")
	fs.StringVar(&opts.Model, "model", "", "model alias or name. Empty means the llm default")
	fs.StringVar(&opts.Session.ID, "session", "", "session id copied to the traces")
	var tags listFlag
	fs.Var(&tags, "tag", "tag copied to the traces. Repeatable")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *eventID == "" {
		return fail(stderr, "diagnose", fmt.Errorf("--event %w", errRequired))
	}
	opts.Knowledge = diagnose.KnowledgeMode(*knowledgeMode)
	opts.Session.Tags = tags
	a, err := data.app(getenv, now)
	if err != nil {
		return fail(stderr, "diagnose", err)
	}
	d, err := a.diagnoser(client)
	if err != nil {
		return fail(stderr, "diagnose", err)
	}
	cmd := diagnoseCommand{diagnoser: d, out: stdout, log: stderr}
	if err := cmd.run(context.Background(), *eventID, opts); err != nil {
		return fail(stderr, "diagnose", err)
	}
	return 0
}

// JSON on stdout and the trace id on stderr so the review pipes on its own
type diagnoseCommand struct {
	diagnoser *diagnose.Diagnoser
	out, log  io.Writer
}

func (c diagnoseCommand) run(ctx context.Context, eventID string, opts diagnose.BatchOptions) error {
	res, err := c.diagnoser.Run(ctx, eventID, opts)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(res.Diagnosis, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(c.out, string(b))
	fmt.Fprintf(c.log, "trace %s\n", res.TraceID)
	return nil
}
