package main

import (
	"context"
	"encoding/json"
	"errors"
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
	fs.Var((*idList)(&opts.Events), "events", "seed and holdout: comma separated event ids to run. Empty means every event of that half")
	fs.IntVar(&opts.Parallel, "parallel", 0, "reviews in flight at once. 0 means 4")
	fs.IntVar(&opts.Repeat, "repeat", 1, "seed and holdout: reviews per event and condition. Above 1 every review carries its repeat tag")
	triage := fs.Bool("triage", false, "report: add how many wrong statuses the top 5 of the queue order hold")
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
		if *triage {
			err = cmd.triage(ctx, opts.SessionID)
		} else {
			err = cmd.report(ctx, opts.SessionID)
		}
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
	// No command removes feedback so these records stay unusable for holdout and only a fresh record dir gets past it
	if errors.Is(err, eval.ErrHoldoutFeedback) || errors.Is(err, eval.ErrHoldoutKnowledge) {
		return fmt.Errorf("%w. The records in %s already judge a holdout event. "+
			"Run the eval with --record-dir or %s naming an empty directory", err, c.recordDir, envRecordDir)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "holdout traces %d\n", len(traces))
	return nil
}

func (c evalCommand) report(ctx context.Context, sessionID string) error {
	rep, err := c.runner.Report(ctx, sessionID)
	if err != nil {
		return err
	}
	return c.write(rep)
}

// The report with the triage rows of the queue order
func (c evalCommand) triage(ctx context.Context, sessionID string) error {
	rep, err := c.runner.Report(ctx, sessionID)
	if err != nil {
		return err
	}
	if rep.Triage, err = c.runner.Triage(ctx, sessionID); err != nil {
		return err
	}
	return c.write(rep)
}

// Prints the table and writes the full report next to the records
func (c evalCommand) write(rep eval.Report) error {
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(c.recordDir, "eval-"+rep.SessionID+".json")
	if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		return err
	}
	fmt.Fprint(c.out, rep.Table())
	fmt.Fprintf(c.log, "report %s\n", path)
	return nil
}
