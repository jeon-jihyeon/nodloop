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
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/diagnose"
	"github.com/jeon-jihyeon/nodloop/internal/evidence"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/jsonl"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/llm"
)

// Flags of the knowledge subcommands
// Each action reads the ones it needs
type knowledgeFlags struct {
	id, kind, content, basis, approver, author, status, traceID, file  string
	from                                                               string
	version, parallel                                                  int
	contexts, metrics, exceptions, paragraphs, feedbackIDs, outcomeIDs listFlag
	vetoTool, vetoField, vetoMatch, vetoUnless, vetoExample            string
	model                                                              string
	events                                                             idList
	stale                                                              bool
}

func (f *knowledgeFlags) bind(fs *flag.FlagSet) {
	fs.BoolVar(&f.stale, "stale", false, "list: only the approved versions past their review deadline")
	fs.StringVar(&f.id, "id", "", "knowledge id. propose generates one when empty")
	fs.IntVar(&f.version, "version", 0, "version for approve and retire")
	fs.StringVar(&f.kind, "kind", "", "meaning or judgment")
	fs.StringVar(&f.content, "content", "", "the knowledge in one or a few sentences")
	fs.StringVar(&f.basis, "basis", string(knowledge.BasisStated), "stated or verified")
	fs.StringVar(&f.approver, "approver", "", "name of the person approving or retiring")
	fs.StringVar(&f.author, "author", feedback.ReviewerAuthor, "who proposed")
	fs.StringVar(&f.status, "status", "", "list: candidate, approved, retired or superseded")
	fs.StringVar(&f.traceID, "trace", "", "propose: the diagnose trace whose feedback is the evidence")
	fs.StringVar(&f.file, "file", "", "import: a jsonl file of knowledge records")
	fs.Var(&f.contexts, "scope-context", "change context the item applies to. Repeatable")
	fs.Var(&f.metrics, "scope-metric", "metric the item applies to. Repeatable")
	fs.Var(&f.exceptions, "exception", "change context where it must not apply. Repeatable")
	fs.Var(&f.paragraphs, "evidence-paragraph", "procedure paragraph id. Repeatable")
	fs.Var(&f.feedbackIDs, "evidence-feedback", "diagnose trace id whose feedback supports it. Repeatable")
	fs.Var(&f.outcomeIDs, "evidence-outcome", "diagnose trace id whose outcome supports it. Repeatable")
	fs.StringVar(&f.vetoTool, "veto-tool", "",
		"propose: tool a judgment forbids such as Bash. Approval makes it a guard veto")
	fs.StringVar(&f.vetoField, "veto-field", "", "propose: tool_input field the veto matches such as command")
	fs.StringVar(&f.vetoMatch, "veto-match", "", "propose: RE2 regexp the field must match")
	fs.StringVar(&f.vetoUnless, "veto-unless", "", "propose: RE2 regexp that lets the call through")
	fs.StringVar(&f.vetoExample, "veto-example", "",
		`propose: a tool_input JSON object the veto must block such as {"command":"sed -i s/a/b/ f"}`)
	fs.StringVar(&f.from, "from", "",
		"propose: the diagnose trace of a review corrected by edit or reject. Scope, evidence and basis come from it")
	fs.StringVar(&f.model, "model", "", "compact, replay and propose --from: model alias or name. Empty means the llm default")
	fs.Var(&f.events, "events", "replay: comma separated event ids to replay again. Empty means every replay event")
	fs.IntVar(&f.parallel, "parallel", 0, "replay: reviews in flight at once. 0 means 4")
}

// The candidate the propose flags describe
func (f knowledgeFlags) draft() (knowledge.Knowledge, error) {
	feedbackIDs := []string(f.feedbackIDs)
	if f.traceID != "" {
		feedbackIDs = slices.Concat(feedbackIDs, []string{f.traceID})
	}
	v, err := f.veto()
	if err != nil {
		return knowledge.Knowledge{}, err
	}
	return knowledge.Knowledge{
		ID:         f.id,
		Kind:       knowledge.Kind(f.kind),
		Content:    f.content,
		Scope:      knowledge.Scope{Scope: evidence.Scope{ChangeContexts: f.contexts.contexts(), Metrics: f.metrics}},
		Exceptions: f.exceptions.contexts(),
		Evidence:   knowledge.Evidence{FeedbackTraceIDs: feedbackIDs, OutcomeTraceIDs: f.outcomeIDs, ParagraphIDs: f.paragraphs},
		Basis:      knowledge.Basis(f.basis),
		Author:     f.author,
		Veto:       v,
	}, nil
}

// Nil without `--veto-tool` so a plain proposal carries no veto
func (f knowledgeFlags) veto() (*knowledge.Veto, error) {
	if f.vetoTool == "" {
		return nil, nil
	}
	var example map[string]any
	if err := json.Unmarshal([]byte(f.vetoExample), &example); err != nil {
		return nil, fmt.Errorf("%w: --veto-example: %w", errVetoExample, err)
	}
	return &knowledge.Veto{
		Tool:    f.vetoTool,
		When:    []knowledge.VetoCondition{{Field: f.vetoField, Match: f.vetoMatch, Unless: f.vetoUnless}},
		Example: example,
	}, nil
}

// `--stale` keeps the versions stale at now
func (f knowledgeFlags) filter(now time.Time) knowledge.Filter {
	var out knowledge.Filter
	if f.status != "" {
		out.Statuses = []knowledge.Status{knowledge.Status(f.status)}
	}
	if f.kind != "" {
		out.Kinds = []knowledge.Kind{knowledge.Kind(f.kind)}
	}
	if f.stale {
		out.StaleAt = now
	}
	return out
}

func runKnowledge(
	args []string, getenv func(string) string, client llm.Client, now func() time.Time, stdout, stderr io.Writer,
) int {
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
	ctx := context.Background()
	switch {
	case slices.Contains([]string{"compact", "compaction", "replay", "approve-compaction"}, args[0]):
		err = flags.runCompaction(ctx, args[0], id, a, client, stdout, stderr)
	case args[0] == "propose":
		err = flags.runPropose(ctx, a, client, stdout)
	default:
		err = flags.runRecords(ctx, args[0], id, a, stdout)
	}
	if err != nil {
		return fail(stderr, "knowledge", err)
	}
	return 0
}

// The actions over the knowledge records and the records they cite and never the policy
func (f knowledgeFlags) runRecords(ctx context.Context, action, id string, a app, stdout io.Writer) error {
	ledger, err := a.ledger()
	if err != nil {
		return err
	}
	cmd := a.knowledgeCommand(ledger, stdout)
	switch action {
	case "list":
		return cmd.list(ctx, f.filter(a.now()))
	case "health":
		return cmd.health(ctx)
	case "audit":
		return cmd.audit(ctx)
	case "reaffirm":
		return cmd.reaffirm(ctx, id, f.version, f.approver)
	case "narrow":
		return cmd.narrow(ctx, id, f.version, f.author)
	case "show":
		return cmd.show(ctx, id)
	case "overlaps":
		return cmd.overlaps(ctx, id)
	case "approve":
		if err := cmd.transition(ctx, "approve", ledger.Approve, id, f.version, f.approver); err != nil {
			return err
		}
		if err := cmd.folder(ctx, id, f.version); err != nil {
			return err
		}
		return cmd.compaction(ctx, id, f.version)
	case "retire":
		return cmd.transition(ctx, "retire", ledger.Retire, id, f.version, f.approver)
	case "import":
		return cmd.importFile(ctx, f.file)
	case "export":
		return cmd.export(ctx)
	default:
		return fmt.Errorf("%w %q", errUnknownAction, action)
	}
}

type knowledgeCommand struct {
	app    app
	ledger *knowledge.Ledger
	// The approved veto file of the record directory
	// Empty without a home
	vetoPath     string
	settingsPath string
	stableBinary string
	out          io.Writer
}

func (a app) knowledgeCommand(ledger *knowledge.Ledger, out io.Writer) knowledgeCommand {
	return knowledgeCommand{
		app: a, ledger: ledger, vetoPath: a.vetoFile(a.cfg.recordDir).Path(), settingsPath: a.cfg.home.settingsPath(),
		stableBinary: a.cfg.home.stableBinary(), out: out,
	}
}

// A proposal from the flags alone reads the records only
// One filled from a corrected review reads the review through the diagnoser and so opens the whole pipeline
func (f knowledgeFlags) runPropose(ctx context.Context, a app, client llm.Client, stdout io.Writer) error {
	draft, err := f.draft()
	if err != nil {
		return err
	}
	if f.from == "" {
		ledger, err := a.ledger()
		if err != nil {
			return err
		}
		return a.knowledgeCommand(ledger, stdout).propose(ctx, draft)
	}
	p, err := a.pipeline()
	if err != nil {
		return err
	}
	return a.knowledgeCommand(p.ledger, stdout).proposeFrom(ctx, p.diagnoser(client), f.from, draft, f.model)
}

// Code fills scope and evidence and basis from the correction and the fields of the draft replace or add to them
// Without content one model call drafts it and the candidate says so
func (c knowledgeCommand) proposeFrom(
	ctx context.Context, d *diagnose.Diagnoser, from string, draft knowledge.Knowledge, model string,
) error {
	correction, err := d.Correction(ctx, from)
	if err != nil {
		return err
	}
	p := correction.Proposal()
	draft = draft.Filled(p.Scope, p.Evidence, p.Basis)
	if draft.Content != "" {
		return c.propose(ctx, draft)
	}
	written, err := d.DraftContent(ctx, correction, model)
	if err != nil {
		return err
	}
	draft.Content, draft.Drafted = written.Content, true
	if err := c.propose(ctx, draft); err != nil {
		return err
	}
	fmt.Fprintf(c.out, "drafted\t%.4f usd\t%s\n", written.CostUSD, written.Content)
	return nil
}

// Prints the candidate and its scope and its veto and the folder it would join so the person sees how wide it reaches before approving
func (c knowledgeCommand) propose(ctx context.Context, draft knowledge.Knowledge) error {
	if err := c.app.checkReviews(ctx, draft.Evidence.TraceIDs()...); err != nil {
		return err
	}
	k, overlaps, err := c.ledger.Propose(ctx, draft)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "%s\tv%d\t%s\nscope\t%s\n", k.ID, k.Version, k.Status, k.Scope)
	for _, o := range overlaps {
		fmt.Fprintf(c.out, "overlaps\t%s\tv%d\t%s\n", o.ID, o.Version, o.Status)
	}
	if k.Veto != nil {
		for _, w := range k.Veto.When {
			fmt.Fprintf(c.out, "veto\t%s\t%s matches %s unless %q\n", k.Veto.Tool, w.Field, w.Match, w.Unless)
		}
	}
	return c.folder(ctx, k.ID, k.Version)
}

// The folder a version joins with its size
func (c knowledgeCommand) folder(ctx context.Context, id string, version int) error {
	f, err := c.ledger.Folder(ctx, id, version)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "folder\t%d of %d chars\t%d of %d items in %s\t%s\n",
		f.Chars, knowledge.ReviewChars, f.Size(), knowledge.ReviewItems, cmp.Or(string(f.Context), "no change context"), f)
	return nil
}

// A line when the folder of an approved version is crowded
// 1. compaction due when a compaction could pass its replay
// 2. compaction blocked with the events that lack an expected status when none of them has one
// 3. compaction unknown with the reason when the folder cannot be checked since the approval is already recorded
func (c knowledgeCommand) compaction(ctx context.Context, id string, version int) error {
	f, err := c.ledger.Folder(ctx, id, version)
	if err != nil || !f.Crowded() {
		return err
	}
	compactor, err := c.app.compactor(c.ledger)
	if err != nil {
		fmt.Fprintf(c.out, "compaction unknown\t%s\n", err)
		return nil
	}
	cf, err := compactor.Folder(ctx, id)
	switch {
	case err != nil:
		fmt.Fprintf(c.out, "compaction unknown\t%s\n", err)
	case cf.Due():
		fmt.Fprintf(c.out, "compaction due\ta review of one change context carries more than %d approved items an event can replay. "+
			"Run nodloop knowledge compact %s\n", knowledge.FolderItems, id)
	default:
		fmt.Fprintf(c.out, "compaction blocked\ta review of one change context carries more than %d approved items "+
			"but no evidence event has a label or an edit or approve verdict: %s\n",
			knowledge.FolderItems, strings.Join(cf.Unverifiable, ", "))
	}
	return nil
}

// The current version per id or every version of a named status so a narrowed candidate shows beside its approved version
// The last column says whether the version is past its review deadline
func (c knowledgeCommand) list(ctx context.Context, f knowledge.Filter) error {
	all, err := c.ledger.All(ctx)
	if err != nil {
		return err
	}
	now := c.app.now()
	items := all.Current()
	if len(f.Statuses) > 0 {
		items = all.Versions()
	}
	for _, k := range items.Matching(f) {
		fmt.Fprintf(c.out, "%s\tv%d\t%s\t%s\t%s\tstale=%t\n", k.ID, k.Version, k.Status, k.Kind, k.Content, k.Stale(now))
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
// 1. a failed veto export still prints the status line because the record is written and the error follows
// 2. the veto line appears only when the export worked and the approved vetoes changed
func (c knowledgeCommand) transition(
	ctx context.Context, action string, move func(context.Context, string, int, string) (knowledge.Knowledge, error),
	id string, version int, approver string,
) error {
	if id == "" || version <= 0 || approver == "" {
		return fmt.Errorf("%s: an id, --version and --approver %w", action, errRequired)
	}
	before, err := c.ledger.All(ctx)
	if err != nil {
		return err
	}
	k, err := move(ctx, id, version, approver)
	if err != nil && !errors.Is(err, knowledge.ErrVetoExport) {
		return err
	}
	fmt.Fprintf(c.out, "%s\tv%d\t%s\t%s\n", k.ID, k.Version, k.Status, k.Approver)
	if err != nil {
		return err
	}
	after, err := c.ledger.All(ctx)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(before.Vetoes(), after.Vetoes()) {
		c.vetoLine(len(after.Vetoes()))
	}
	return nil
}

// Hands the approved vetoes to the file again after a failed export
func (c knowledgeCommand) export(ctx context.Context) error {
	if err := c.ledger.ExportVetoes(ctx); err != nil {
		return err
	}
	all, err := c.ledger.All(ctx)
	if err != nil {
		return err
	}
	c.vetoLine(len(all.Vetoes()))
	return nil
}

// Where the vetoes went and whether guard enforces them
// The file blocks nothing until the hook is registered
func (c knowledgeCommand) vetoLine(count int) {
	if c.vetoPath == "" {
		fmt.Fprintln(c.out, "vetoes\tnot exported because the home directory is unknown")
		return
	}
	fmt.Fprintf(c.out, "vetoes\t%d approved in %s\t%s\n", count, c.vetoPath, hookState(c.settingsPath, c.stableBinary))
}

// Appends the records of a jsonl file as they are
// The seed knowledge of a data set arrives this way
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
	imported, err := c.ledger.Import(ctx, records)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if skipped := len(records) - len(imported); skipped > 0 {
		fmt.Fprintf(c.out, "imported %d\tskipped %d already recorded\n", len(imported), skipped)
		return nil
	}
	fmt.Fprintf(c.out, "imported %d\n", len(imported))
	return nil
}
