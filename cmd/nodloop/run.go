package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// Repeatable key=value flag of run labels
type labelFlag trace.Labels

func (l *labelFlag) String() string { return fmt.Sprint(map[string][]string(*l)) }

func (l *labelFlag) Set(v string) error {
	key, value, ok := strings.Cut(v, "=")
	if !ok {
		return fmt.Errorf("%w: %q is not key=value", errLabelFlag, v)
	}
	if *l == nil {
		*l = labelFlag{}
	}
	(*l)[key] = append((*l)[key], value)
	return nil
}

// Repeatable id:version flag of the knowledge a run applied
type appliedFlag []knowledge.Ref

func (a *appliedFlag) String() string { return fmt.Sprint([]knowledge.Ref(*a)) }

func (a *appliedFlag) Set(v string) error {
	id, version, ok := strings.Cut(v, ":")
	n, err := strconv.Atoi(version)
	if !ok || id == "" || err != nil || n < 1 {
		return fmt.Errorf("%w: %q is not id:version", errAppliedFlag, v)
	}
	*a = append(*a, knowledge.Ref{ID: id, Version: n})
	return nil
}

func runRun(args []string, getenv func(string) string, now func() time.Time, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return fail(stderr, "run", errNoAction)
	}
	fs := flag.NewFlagSet("run "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	var records recordFlags
	records.bind(fs)
	producer := fs.String("producer", "", "record: the producer that made the output such as session")
	subject := fs.String("subject", "", "record: what the run was about in a few words")
	output := fs.String("output", "", "record: file holding the output, JSON or text")
	var labels labelFlag
	fs.Var(&labels, "label", "record: key=value of the situation. Repeatable")
	var applied appliedFlag
	fs.Var(&applied, "applied", "record: id:version of a knowledge item the run applied. Repeatable")
	if err := fs.Parse(args[1:]); err != nil {
		return 1
	}
	a, err := records.app(getenv, now)
	if err != nil {
		return fail(stderr, "run", err)
	}
	cmd := runCommand{app: a, out: stdout}
	switch args[0] {
	case "record":
		err = cmd.record(context.Background(), *producer, *subject, trace.Labels(labels), applied, *output)
	default:
		err = fmt.Errorf("%w %q", errUnknownAction, args[0])
	}
	if err != nil {
		return fail(stderr, "run", err)
	}
	return 0
}

type runCommand struct {
	app app
	out io.Writer
}

// Prints the id a nod on the run cites
func (c runCommand) record(
	ctx context.Context, producer, subject string, labels trace.Labels, applied []knowledge.Ref, outputPath string,
) error {
	if outputPath == "" {
		return fmt.Errorf("record: --output %w", errRequired)
	}
	output, err := os.ReadFile(outputPath)
	if err != nil {
		return fmt.Errorf("--output: %w", err)
	}
	input, err := json.Marshal(runInput{Applied: applied})
	if err != nil {
		return err
	}
	tr, err := trace.NewRun(producer, subject, labels, input, []byte(feedback.Redact(string(output))), c.app.now())
	if err != nil {
		return err
	}
	store, err := c.app.traces()
	if err != nil {
		return err
	}
	if err := store.Append(ctx, tr); err != nil {
		return err
	}
	fmt.Fprintln(c.out, tr.ID)
	return nil
}

// What a run trace keeps as its input
type runInput struct {
	Applied []knowledge.Ref `json:"applied"`
	// The plugin version a conversation hook ran under so a report can compare releases
	// Empty for a run recorded through run record
	Plugin string `json:"plugin,omitempty"`
}
