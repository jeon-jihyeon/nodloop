package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/eval"
	"github.com/jeon-jihyeon/nodloop/internal/llm"
)

func runEval(
	args []string, getenv func(string) string, client llm.Client, now func() time.Time, stdout, stderr io.Writer,
) int {
	if len(args) == 0 {
		return fail(stderr, "eval", errNoAction)
	}
	fs := flag.NewFlagSet("eval "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	var data dataFlags
	data.bind(fs)
	var opts eval.RunOptions
	fs.StringVar(&opts.SessionID, "session", "", "session id that groups the run")
	fs.StringVar(&opts.Model, "model", "", "model alias or name. Empty means the llm default")
	fs.IntVar(&opts.Examples, "examples", defaultExamples, "examples injected in the feedback:on condition. 0 means none")
	conditions := fs.String("conditions", "", "holdout: comma separated subset of feedback:off, feedback:on, knowledge:on, knowledge:all. Empty means all")
	events := fs.String("events", "", "seed and holdout: comma separated event ids to run. Empty means every event of that half")
	fs.IntVar(&opts.Parallel, "parallel", 0, "reviews in flight at once. 0 means 4")
	if err := fs.Parse(args[1:]); err != nil {
		return 1
	}
	if opts.SessionID == "" {
		return fail(stderr, "eval", fmt.Errorf("--session %w", errRequired))
	}
	if *conditions != "" {
		for _, c := range strings.Split(*conditions, ",") {
			opts.Conditions = append(opts.Conditions, eval.Condition(c))
		}
	}
	if *events != "" {
		opts.Events = strings.Split(*events, ",")
	}
	a, err := data.app(getenv, now)
	if err != nil {
		return fail(stderr, "eval", err)
	}
	dir, err := a.makeRecordDir()
	if err != nil {
		return fail(stderr, "eval", err)
	}
	r, err := a.runner(client)
	if err != nil {
		return fail(stderr, "eval", err)
	}
	cmd := evalCommand{runner: r, recordDir: dir, out: stdout, log: stderr}
	ctx := context.Background()
	switch args[0] {
	case "seed":
		err = cmd.seed(ctx, opts)
	case "holdout":
		err = cmd.holdout(ctx, opts)
	case "report":
		err = cmd.report(ctx, opts.SessionID)
	default:
		err = fmt.Errorf("%w %q", errUnknownAction, args[0])
	}
	if err != nil {
		return fail(stderr, "eval", err)
	}
	return 0
}

// Progress and the report path go to log so stdout holds only the result
type evalCommand struct {
	runner    *eval.Runner
	recordDir string
	out, log  io.Writer
}

func (c evalCommand) seed(ctx context.Context, opts eval.RunOptions) error {
	opts.Log = c.log
	traces, err := c.runner.Seed(ctx, opts)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "seed traces %d\n", len(traces))
	return nil
}

func (c evalCommand) holdout(ctx context.Context, opts eval.RunOptions) error {
	opts.Log = c.log
	traces, err := c.runner.Holdout(ctx, opts)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "holdout traces %d\n", len(traces))
	return nil
}

// Prints the table and writes the full report next to the records
func (c evalCommand) report(ctx context.Context, sessionID string) error {
	rep, err := c.runner.Report(ctx, sessionID)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(c.recordDir, "eval-"+sessionID+".json")
	if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		return err
	}
	fmt.Fprint(c.out, rep.Table())
	fmt.Fprintf(c.log, "report %s\n", path)
	return nil
}
