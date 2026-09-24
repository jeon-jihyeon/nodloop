package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/jsonl"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
)

// Flags of the knowledge subcommands
// Each action reads the ones it needs
type knowledgeFlags struct {
	id, kind, content, basis, approver, author, status, traceID, file  string
	version                                                            int
	contexts, metrics, exceptions, paragraphs, feedbackIDs, outcomeIDs listFlag
}

func (f *knowledgeFlags) bind(fs *flag.FlagSet) {
	fs.StringVar(&f.id, "id", "", "knowledge id. propose generates one when empty")
	fs.IntVar(&f.version, "version", 0, "version for approve and retire")
	fs.StringVar(&f.kind, "kind", "", "meaning or judgment")
	fs.StringVar(&f.content, "content", "", "the knowledge in one or a few sentences")
	fs.StringVar(&f.basis, "basis", string(knowledge.BasisStated), "stated or verified")
	fs.StringVar(&f.approver, "approver", "", "name of the person approving or retiring")
	fs.StringVar(&f.author, "author", feedback.DefaultReviewer, "who proposed")
	fs.StringVar(&f.status, "status", "", "list: candidate, approved, retired or superseded")
	fs.StringVar(&f.traceID, "trace", "", "propose: the diagnose trace whose feedback is the evidence")
	fs.StringVar(&f.file, "file", "", "import: a jsonl file of knowledge records")
	fs.Var(&f.contexts, "scope-context", "change context the item applies to. Repeatable")
	fs.Var(&f.metrics, "scope-metric", "metric the item applies to. Repeatable")
	fs.Var(&f.exceptions, "exception", "change context or event type where it must not apply. Repeatable")
	fs.Var(&f.paragraphs, "evidence-paragraph", "runbook paragraph id. Repeatable")
	fs.Var(&f.feedbackIDs, "evidence-feedback", "diagnose trace id whose feedback supports it. Repeatable")
	fs.Var(&f.outcomeIDs, "evidence-outcome", "diagnose trace id whose outcome supports it. Repeatable")
}

// The candidate the propose flags describe
func (f knowledgeFlags) draft() knowledge.Knowledge {
	feedbackIDs := []string(f.feedbackIDs)
	if f.traceID != "" {
		feedbackIDs = slices.Concat(feedbackIDs, []string{f.traceID})
	}
	return knowledge.Knowledge{
		ID:         f.id,
		Kind:       knowledge.Kind(f.kind),
		Content:    f.content,
		Scope:      knowledge.Scope{ChangeContexts: f.contexts.contexts(), Metrics: f.metrics},
		Exceptions: f.exceptions.contexts(),
		Evidence:   knowledge.Evidence{FeedbackTraceIDs: feedbackIDs, OutcomeTraceIDs: f.outcomeIDs, ParagraphIDs: f.paragraphs},
		Basis:      knowledge.Basis(f.basis),
		Author:     f.author,
	}
}

func (f knowledgeFlags) filter() knowledge.Filter {
	var out knowledge.Filter
	if f.status != "" {
		out.Statuses = []knowledge.Status{knowledge.Status(f.status)}
	}
	if f.kind != "" {
		out.Kinds = []knowledge.Kind{knowledge.Kind(f.kind)}
	}
	return out
}

func runKnowledge(args []string, getenv func(string) string, now func() time.Time, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return fail(stderr, "knowledge", errNoAction)
	}
	fs := flag.NewFlagSet("knowledge "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	var data dataFlags
	data.bind(fs)
	var flags knowledgeFlags
	flags.bind(fs)
	id, err := parseID(fs, args[1:])
	if err != nil {
		return 1
	}
	a, err := data.app(getenv, now)
	if err != nil {
		return fail(stderr, "knowledge", err)
	}
	ledger, err := a.ledger()
	if err != nil {
		return fail(stderr, "knowledge", err)
	}
	cmd := knowledgeCommand{app: a, ledger: ledger, out: stdout}
	ctx := context.Background()
	switch args[0] {
	case "propose":
		err = cmd.propose(ctx, flags.draft())
	case "list":
		err = cmd.list(ctx, flags.filter())
	case "show":
		err = cmd.show(ctx, id)
	case "overlaps":
		err = cmd.overlaps(ctx, id)
	case "approve":
		err = cmd.transition(ctx, "approve", ledger.Approve, id, flags.version, flags.approver)
	case "retire":
		err = cmd.transition(ctx, "retire", ledger.Retire, id, flags.version, flags.approver)
	case "import":
		err = cmd.importFile(ctx, flags.file)
	default:
		err = fmt.Errorf("%w %q", errUnknownAction, args[0])
	}
	if err != nil {
		return fail(stderr, "knowledge", err)
	}
	return 0
}

type knowledgeCommand struct {
	app    app
	ledger *knowledge.Ledger
	out    io.Writer
}

func (c knowledgeCommand) propose(ctx context.Context, draft knowledge.Knowledge) error {
	if err := c.app.checkReviews(ctx, draft.Evidence.TraceIDs()...); err != nil {
		return err
	}
	k, overlaps, err := c.ledger.Propose(ctx, draft)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "%s\tv%d\t%s\n", k.ID, k.Version, k.Status)
	for _, o := range overlaps {
		fmt.Fprintf(c.out, "overlaps\t%s\tv%d\t%s\n", o.ID, o.Version, o.Status)
	}
	return nil
}

func (c knowledgeCommand) list(ctx context.Context, f knowledge.Filter) error {
	all, err := c.ledger.All(ctx)
	if err != nil {
		return err
	}
	for _, k := range all.Current().Matching(f) {
		fmt.Fprintf(c.out, "%s\tv%d\t%s\t%s\t%s\n", k.ID, k.Version, k.Status, k.Kind, k.Content)
	}
	return nil
}

func (c knowledgeCommand) show(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("an id %w", errRequired)
	}
	history, err := c.ledger.History(ctx, id)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(history, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(c.out, string(b))
	return nil
}

func (c knowledgeCommand) overlaps(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("an id %w", errRequired)
	}
	overlaps, err := c.ledger.Overlaps(ctx, id)
	if err != nil {
		return err
	}
	for _, o := range overlaps {
		fmt.Fprintf(c.out, "%s\tv%d\t%s\t%s\n", o.ID, o.Version, o.Status, o.Content)
	}
	return nil
}

// Approve and retire move one version to a new status under a name
func (c knowledgeCommand) transition(
	ctx context.Context, action string, move func(context.Context, string, int, string) (knowledge.Knowledge, error),
	id string, version int, approver string,
) error {
	if id == "" || version <= 0 || approver == "" {
		return fmt.Errorf("%s: an id, --version and --approver %w", action, errRequired)
	}
	k, err := move(ctx, id, version, approver)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "%s\tv%d\t%s\t%s\n", k.ID, k.Version, k.Status, k.Approver)
	return nil
}

// Appends the records of a jsonl file as they are
// The demo knowledge arrives this way
func (c knowledgeCommand) importFile(ctx context.Context, path string) error {
	if path == "" {
		return fmt.Errorf("--file %w", errRequired)
	}
	// jsonl Open reads a missing file as empty so a wrong path would import nothing
	if _, err := os.Stat(path); err != nil {
		return err
	}
	file, err := jsonl.Open[knowledge.Knowledge](filepath.Dir(path), filepath.Base(path))
	if err != nil {
		return err
	}
	records, err := file.All()
	if err != nil {
		return err
	}
	if err := c.ledger.Import(ctx, records); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	fmt.Fprintf(c.out, "imported %d\n", len(records))
	return nil
}
