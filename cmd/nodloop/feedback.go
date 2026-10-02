package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/diagnose"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	feedbackfile "github.com/jeon-jihyeon/nodloop/internal/feedback/file"
)

// Flags of the feedback subcommands
// Each action reads the ones it needs
type feedbackFlags struct {
	traceID, verdict, reviewer, code, reason, edited, result, cause, note string
	limit                                                                 int
	audit                                                                 bool
}

func (f *feedbackFlags) bind(fs *flag.FlagSet) {
	fs.StringVar(&f.traceID, "trace", "", "trace id")
	fs.StringVar(&f.verdict, "verdict", "", "approve or edit or reject")
	fs.StringVar(&f.reviewer, "reviewer", "", "reviewer such as author")
	fs.IntVar(&f.limit, "limit", 0, "newest n records. 0 means all")
	fs.StringVar(&f.code, "reason-code", "",
		"add: what the corrected review got wrong with edit or reject. status, cause, citation, checks or other")
	fs.StringVar(&f.reason, "reason", "", "add: why the verdict was given")
	fs.StringVar(&f.edited, "edited", "", "add: file holding the corrected output JSON")
	fs.StringVar(&f.result, "result", "", "outcome: confirmed, refuted or inconclusive")
	fs.StringVar(&f.cause, "cause", "", "outcome: the cause that was confirmed")
	fs.StringVar(&f.note, "note", "", "outcome: free text")
	fs.BoolVar(&f.audit, "audit", false, "add: review selected by random audit")
}

func (f feedbackFlags) filter() feedback.Filter {
	out := feedback.Filter{TraceID: f.traceID, Reviewer: f.reviewer, Limit: f.limit}
	if f.verdict != "" {
		out.Verdicts = []feedback.Verdict{feedback.Verdict(f.verdict)}
	}
	return out
}

func runFeedback(args []string, getenv func(string) string, now func() time.Time, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return fail(stderr, "feedback", errNoAction)
	}
	fs := flag.NewFlagSet("feedback "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	var data dataFlags
	data.bind(fs)
	var flags feedbackFlags
	flags.bind(fs)
	if err := fs.Parse(args[1:]); err != nil {
		return 1
	}
	a, err := data.records(getenv, now)
	if err != nil {
		return fail(stderr, "feedback", err)
	}
	cmd := feedbackCommand{app: a, out: stdout, audit: flags.audit}
	ctx := context.Background()
	switch args[0] {
	case "list":
		err = cmd.list(ctx, flags.filter())
	case "add":
		err = cmd.add(ctx, flags.traceID, feedback.Verdict(flags.verdict), feedback.ReasonCode(flags.code), flags.reason, flags.edited, flags.reviewer)
	case "outcome":
		err = cmd.outcome(ctx, flags.traceID, feedback.Result(flags.result), flags.cause, flags.note, flags.reviewer)
	default:
		err = fmt.Errorf("%w %q", errUnknownAction, args[0])
	}
	if err != nil {
		return fail(stderr, "feedback", err)
	}
	return 0
}

// Each action opens only the stores it reads or appends to
type feedbackCommand struct {
	app   app
	out   io.Writer
	audit bool
}

func (c feedbackCommand) list(ctx context.Context, f feedback.Filter) error {
	store, err := c.app.feedback()
	if err != nil {
		return err
	}
	records, err := store.List(ctx, f)
	if err != nil {
		return err
	}
	// A record without a code prints a dash so the reason stays in the last column
	for _, fb := range records {
		fmt.Fprintf(c.out, "%s\t%s\t%s\t%s\t%s\t%s\n",
			fb.TraceID, fb.Time.Format(time.RFC3339), fb.Verdict, cmp.Or(string(fb.ReasonCode), "-"), fb.Reviewer, fb.Reason)
	}
	return nil
}

func (c feedbackCommand) add(
	ctx context.Context, traceID string, verdict feedback.Verdict, code feedback.ReasonCode, reason, editedPath, reviewer string,
) error {
	if traceID == "" || verdict == "" {
		return fmt.Errorf("add: --trace and a --verdict of approve or edit or reject %w", errRequired)
	}
	var edited json.RawMessage
	if editedPath != "" {
		b, err := os.ReadFile(editedPath)
		if err != nil {
			return fmt.Errorf("--edited: %w", err)
		}
		edited = b
	}
	fb, err := feedback.New(traceID, verdict, code, reason, edited, reviewer, c.app.now())
	if err != nil {
		return err
	}
	fb.Audit = c.audit
	store, err := c.checked(ctx, fb.TraceID, fb.Edited)
	if err != nil {
		return err
	}
	if err := store.Append(ctx, fb); err != nil {
		return err
	}
	fmt.Fprintf(c.out, "%s\t%s\t%s\n", fb.TraceID, fb.Verdict, fb.Reviewer)
	return nil
}

// The store a verdict goes to once the review or run it cites passed its checks
// An edit of a review is checked against the current procedures that apply to the event of the context trace the review was built on
// An edit of a run is any JSON and needs no data dir
// A verdict reads neither the policy nor the events
// A procedures folder that cannot be read counts as empty
// 1. an edit citing a paragraph is refused and the read error joins the refusal to say why
// 2. an edit without citations such as a hold still passes
func (c feedbackCommand) checked(ctx context.Context, traceID string, edited json.RawMessage) (*feedbackfile.Store, error) {
	if err := c.app.checkRuns(ctx, traceID); err != nil {
		return nil, err
	}
	if len(edited) == 0 {
		return c.app.feedback()
	}
	traces, err := c.app.traces()
	if err != nil {
		return nil, err
	}
	review, err := traces.Get(ctx, traceID)
	if err != nil {
		return nil, err
	}
	if !review.IsReview() {
		return c.app.feedback()
	}
	built, err := traces.Get(ctx, review.Ref)
	if err != nil {
		return nil, err
	}
	procedures, readErr := c.app.procedures(ctx)
	if err := diagnose.CheckEdit(built, procedures, edited); err != nil {
		return nil, errors.Join(err, readErr)
	}
	return c.app.feedback()
}

func (c feedbackCommand) outcome(
	ctx context.Context, traceID string, result feedback.Result, cause, note, reviewer string,
) error {
	if traceID == "" || result == "" {
		return fmt.Errorf("outcome: --trace and a --result of confirmed, refuted or inconclusive %w", errRequired)
	}
	o, err := feedback.NewOutcome(traceID, result, cause, note, reviewer, c.app.now())
	if err != nil {
		return err
	}
	if err := c.app.checkRuns(ctx, o.TraceID); err != nil {
		return err
	}
	store, err := c.app.outcomes()
	if err != nil {
		return err
	}
	if err := store.Append(ctx, o); err != nil {
		return err
	}
	fmt.Fprintf(c.out, "%s\t%s\t%s\n", o.TraceID, o.Result, o.Reviewer)
	return nil
}
