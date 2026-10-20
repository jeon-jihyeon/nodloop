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
	"unicode/utf8"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/jsonl"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	knowledgefile "github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	"github.com/jeon-jihyeon/nodloop/internal/llm"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// Flags of the knowledge subcommands
// Each action reads the ones it needs
type knowledgeFlags struct {
	id, kind, content, basis, approver, author, status, traceID, file string
	from, producer, key, model, holdoutKey                            string
	version                                                           int
	feedbackIDs, outcomeIDs                                           listFlag
	labels, except                                                    labelFlag
	vetoTool, vetoField, vetoMatch, vetoUnless, vetoExample           string
	stale, newLabels, replay, everywhere, daily                       bool
}

func (f *knowledgeFlags) bind(fs *flag.FlagSet) {
	fs.BoolVar(&f.stale, "stale", false, "list: only the approved versions past their review deadline")
	fs.BoolVar(&f.replay, "replay", false, "check: also replay every new item of the compaction against its outputs")
	fs.BoolVar(&f.everywhere, "everywhere", false, "approve: approve a candidate for every run of its producer")
	fs.BoolVar(&f.daily, "daily", false,
		"consolidate: run only when consolidate.json is a day old and print nothing, as the stop hook starts it")
	fs.BoolVar(&f.newLabels, "new-labels", false, "propose: allow label values no recorded run carries yet such as a new tenant")
	fs.StringVar(&f.id, "id", "", "knowledge id. propose generates one when empty")
	fs.IntVar(&f.version, "version", 0, "the version to act on. 0 means the approved one for reaffirm and the current one for replay")
	fs.StringVar(&f.kind, "kind", "", "meaning or judgment")
	fs.StringVar(&f.content, "content", "", "the knowledge in one or a few sentences")
	fs.StringVar(&f.basis, "basis", string(knowledge.BasisStated), "stated or verified")
	fs.StringVar(&f.approver, "approver", "",
		"approve, retire, reaffirm and approve-compaction: the name of the person deciding. approver of config.json, then person, when empty")
	fs.StringVar(&f.author, "author", feedback.ReviewerAuthor, "who proposed")
	fs.StringVar(&f.status, "status", "", "list: candidate, approved, retired or superseded")
	fs.StringVar(&f.traceID, "trace", "", "propose: the run whose feedback is the evidence")
	fs.StringVar(&f.file, "file", "", "import: a jsonl file of knowledge records")
	fs.StringVar(&f.producer, "producer", "", "propose and for: the producer whose runs the item applies to")
	fs.Var(&f.labels, "label", "propose, for and approve: key=value a run must carry. Repeatable. approve uses them instead of the proposed ones")
	fs.Var(&f.except, "except", "propose: key=value a run must not carry. Repeatable")
	fs.StringVar(&f.key, "key", "", "narrow: the label key whose refuted values the item stops reaching")
	fs.StringVar(&f.holdoutKey, "holdout-key", "",
		"for: a key kept for one run such as a window id. The holdout of config holdout draws on it. Pass the same key to run record")
	fs.Var(&f.feedbackIDs, "evidence-feedback", "run id whose feedback supports it. Repeatable")
	fs.Var(&f.outcomeIDs, "evidence-outcome", "run id whose outcome supports it. Repeatable")
	fs.StringVar(&f.vetoTool, "veto-tool", "",
		"propose: tool a judgment forbids such as Bash. Approval makes it a guard veto")
	fs.StringVar(&f.vetoField, "veto-field", "", "propose: tool_input field the veto matches such as commands, one line per simple command derived from command")
	fs.StringVar(&f.vetoMatch, "veto-match", "", "propose: RE2 regexp the field must match")
	fs.StringVar(&f.vetoUnless, "veto-unless", "", "propose: RE2 regexp that lets the call through")
	fs.StringVar(&f.vetoExample, "veto-example", "",
		`propose: a tool_input JSON object the veto must block such as {"command":"sed -i s/a/b/ f"}`)
	fs.StringVar(&f.from, "from", "",
		"propose and extract: a run a person corrected by edit or reject. propose fills producer, labels and evidence from it unless given")
	fs.StringVar(&f.model, "model", "", "compact, check, extract, replay and consolidate: model alias or name. Empty means the llm default")
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
	// A person may claim verified only by naming the outcome that confirmed it
	// knowledge promote fills the confirmed runs itself
	if knowledge.Basis(f.basis) == knowledge.BasisVerified && len(f.outcomeIDs) == 0 {
		return knowledge.Knowledge{}, fmt.Errorf("propose: --evidence-outcome %w with --basis verified", errRequired)
	}
	return knowledge.Knowledge{
		Run:       &knowledge.RunScope{Producer: f.producer, Labels: trace.Labels(f.labels), Except: trace.Labels(f.except)},
		ID:        f.id,
		Kind:      knowledge.Kind(f.kind),
		Content:   f.content,
		Evidence:  knowledge.Evidence{FeedbackTraceIDs: feedbackIDs, OutcomeTraceIDs: f.outcomeIDs},
		Basis:     knowledge.Basis(f.basis),
		Author:    f.author,
		Veto:      v,
		NewLabels: f.newLabels,
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

// proof is the terminal proof of the process
// The commands that decide for a person carry out what they record only with it
func runKnowledge(
	args []string, getenv func(string) string, client llm.Client, now func() time.Time, proof trace.Proof, stdout, stderr io.Writer,
) int {
	if len(args) == 0 {
		return fail(stderr, "knowledge", errNoAction)
	}
	fs := newFlagSet("knowledge "+args[0], stderr)
	var records recordFlags
	records.bind(fs)
	var flags knowledgeFlags
	flags.bind(fs)
	id, err := parseID(fs, args[1:])
	if err != nil {
		return parseFailed(err)
	}
	a, err := records.app(getenv, now)
	if err != nil {
		return fail(stderr, "knowledge", err)
	}
	ctx := context.Background()
	switch {
	case slices.Contains([]string{"compact", "compaction", "check", "approve-compaction"}, args[0]):
		err = flags.runCompaction(ctx, args[0], id, a, client, proof, stdout)
	case args[0] == "propose":
		err = flags.runPropose(ctx, a, stdout)
	case args[0] == "extract":
		// The extraction runs detached after a stop so its trace leaves at once instead of after the next stop
		err = flags.runExtract(ctx, a, client, getenv, stdout)
		a.exportAfter(detached(a.cfg.home.hookLog()), stderr)
	case args[0] == "replay":
		err = flags.runReplay(ctx, id, a, client, stdout)
	case args[0] == "consolidate":
		err = flags.runConsolidate(ctx, a, client, stdout)
		a.exportAfter(detached(a.cfg.home.hookLog()), stderr)
	default:
		err = flags.runRecords(ctx, args[0], id, a, proof, stdout)
	}
	if err != nil {
		return fail(stderr, "knowledge", err)
	}
	return 0
}

// The actions over the knowledge records and the records they cite
func (f knowledgeFlags) runRecords(ctx context.Context, action, id string, a app, proof trace.Proof, stdout io.Writer) error {
	ledger, err := a.ledger()
	if err != nil {
		return err
	}
	cmd := a.knowledgeCommand(ledger, stdout)
	cmd.proof = proof
	switch action {
	case "list":
		return cmd.list(ctx, f.filter(a.now()))
	case "health", "audit", "reaffirm", "narrow", "promote":
		return f.runHealth(ctx, action, id, cmd)
	case "show":
		return cmd.show(ctx, id)
	case "overlaps":
		return cmd.overlaps(ctx, id)
	case "for":
		return cmd.forRun(ctx, f.producer, trace.Labels(f.labels), f.holdoutKey)
	case "waiting":
		return cmd.waiting(ctx, f.producer, trace.Labels(f.labels))
	case "approve", "retire":
		return f.runJudge(ctx, action, id, cmd)
	case "import":
		return cmd.importFile(ctx, f.file)
	case "export":
		return cmd.export(ctx)
	default:
		return fmt.Errorf("%w %q", errUnknownAction, action)
	}
}

// Moves one version to a new status under the person's name
// Without the terminal proof it records a proposal
// An approval prints the folder the version joined
func (f knowledgeFlags) runJudge(ctx context.Context, action, id string, cmd knowledgeCommand) error {
	if id == "" || f.version <= 0 {
		return fmt.Errorf("%s: an id and --version %w", action, errRequired)
	}
	target := trace.Intervention{ID: id, Version: f.version, Choice: trace.ChoiceUndo}
	var move mover = cmd.ledger.Retire
	if action == "approve" {
		var err error
		if target, move, err = f.approval(cmd, id); err != nil {
			return err
		}
	}
	judgment, err := cmd.judgment(target, f.approver)
	if err != nil {
		return err
	}
	if err := cmd.transition(ctx, action, move, judgment); err != nil {
		return err
	}
	if action != "approve" || judgment.Proof == trace.ProofNone {
		return nil
	}
	return cmd.folder(ctx, id, f.version)
}

// One version moved to a new status under a name
type mover func(ctx context.Context, id string, version int, approver string) (knowledge.Knowledge, error)

// The approval of the flags and the target it records
// 1. as proposed without --label and --everywhere
// 2. in the labels or for every run of the producer when the person picked one
func (f knowledgeFlags) approval(cmd knowledgeCommand, id string) (trace.Intervention, mover, error) {
	target := trace.Intervention{ID: id, Version: f.version, Choice: trace.ChoiceKeep}
	scoped := target
	scoped.Choice = trace.ChoiceScope
	switch {
	case f.everywhere && len(f.labels) > 0:
		return trace.Intervention{}, nil, fmt.Errorf("approve: %w", errScopeFlags)
	case f.everywhere:
		scoped.Labels = trace.Labels{}
		return scoped, cmd.approveIn(scoped.Labels), nil
	case len(f.labels) > 0:
		scoped.Labels = trace.Labels(f.labels)
		return scoped, cmd.approveIn(scoped.Labels), nil
	}
	return target, cmd.ledger.Approve, nil
}

// The actions that read how the reviews of a version held up and the ones that act on it
func (f knowledgeFlags) runHealth(ctx context.Context, action, id string, cmd knowledgeCommand) error {
	switch action {
	case "health":
		return cmd.health(ctx)
	case "audit":
		return cmd.audit(ctx)
	case "reaffirm":
		return cmd.reaffirm(ctx, id, f.version, f.approver)
	case "narrow":
		return cmd.narrow(ctx, id, f.version, f.key, f.author)
	default:
		return cmd.promote(ctx, id, f.version, f.author)
	}
}

type knowledgeCommand struct {
	app    app
	ledger *knowledge.Ledger
	// The terminal proof of the process
	// The commands that decide for a person carry out what they record only with it
	proof trace.Proof
	// The approved veto file of the record directory
	// Empty without a home
	vetoPath     string
	settingsPath string
	stableBinary string
	// The approved knowledge as rules a CLAUDE.md imports
	rulesPath string
	out       io.Writer
}

// The command proves no person until its caller sets the terminal proof of the process
func (a app) knowledgeCommand(ledger *knowledge.Ledger, out io.Writer) knowledgeCommand {
	return knowledgeCommand{
		app: a, ledger: ledger, proof: trace.ProofNone, vetoPath: a.vetoFile(a.cfg.recordDir).Path(), settingsPath: a.cfg.home.settingsPath(),
		stableBinary: a.cfg.home.stableBinary(), rulesPath: knowledgefile.RulesPath(a.cfg.recordDir), out: out,
	}
}

// A proposal from the flags, filled from a corrected run when --from names one
func (f knowledgeFlags) runPropose(ctx context.Context, a app, stdout io.Writer) error {
	draft, err := f.draft()
	if err != nil {
		return err
	}
	ledger, err := a.ledger()
	if err != nil {
		return err
	}
	cmd := a.knowledgeCommand(ledger, stdout)
	if f.from != "" {
		if draft, err = cmd.fromRun(ctx, f.from, draft); err != nil {
			return err
		}
	}
	if draft.Run.Producer == "" {
		return fmt.Errorf("propose: --producer or --from %w", errRequired)
	}
	return cmd.propose(ctx, draft)
}

// The draft with the producer and labels of a run a person corrected and the run as evidence
func (c knowledgeCommand) fromRun(ctx context.Context, id string, draft knowledge.Knowledge) (knowledge.Knowledge, error) {
	traces, err := c.app.traces()
	if err != nil {
		return knowledge.Knowledge{}, err
	}
	tr, err := traces.Get(ctx, id)
	if err != nil {
		return knowledge.Knowledge{}, err
	}
	store, err := c.app.feedback()
	if err != nil {
		return knowledge.Knowledge{}, err
	}
	verdicts, err := store.List(ctx, record.FeedbackFilter{TraceID: id})
	if err != nil {
		return knowledge.Knowledge{}, err
	}
	return draft.From(trace.Restore(tr), feedback.RestoreAll(verdicts))
}

func (c knowledgeCommand) propose(ctx context.Context, draft knowledge.Knowledge) error {
	if err := c.check(ctx, draft); err != nil {
		return err
	}
	return c.add(ctx, draft)
}

// Every cited run must be recorded and every label carried by some run of the producer unless the draft allows new labels
func (c knowledgeCommand) check(ctx context.Context, draft knowledge.Knowledge) error {
	if err := c.app.checkRuns(ctx, draft.Evidence.TraceIDs()...); err != nil {
		return err
	}
	if draft.NewLabels {
		return nil
	}
	return c.recorded(ctx, *draft.Run)
}

// Reads the runs of the producer
func (c knowledgeCommand) recorded(ctx context.Context, run knowledge.RunScope) error {
	store, err := c.app.traces()
	if err != nil {
		return err
	}
	runs, err := store.List(ctx, record.TraceFilter{Name: string(trace.NameRun)})
	if err != nil {
		return err
	}
	return run.Recorded(trace.RestoreAll(runs).Vocabulary(run.Producer))
}

// Approves in the labels once a recorded run of the producer of the version carries each of them
func (c knowledgeCommand) approveIn(labels trace.Labels) func(context.Context, string, int, string) (knowledge.Knowledge, error) {
	return func(ctx context.Context, id string, version int, approver string) (knowledge.Knowledge, error) {
		scope, err := c.ledger.ScopeIn(ctx, id, version, labels)
		if err != nil {
			return knowledge.Knowledge{}, err
		}
		if err := c.recorded(ctx, scope); err != nil {
			return knowledge.Knowledge{}, err
		}
		return c.ledger.ApproveIn(ctx, id, version, labels, approver)
	}
}

// The approved items a run of the producer with the labels applies and then their size
// A run the holdout draws for the key gets a withheld line per item it would have received and no item
func (c knowledgeCommand) forRun(ctx context.Context, producer string, labels trace.Labels, holdoutKey string) error {
	if producer == "" {
		return fmt.Errorf("for: --producer %w", errRequired)
	}
	all, err := c.ledger.All(ctx)
	if err != nil {
		return err
	}
	items := all.For(producer, labels)
	if c.app.cfg.holdout.WithholdsRun(producer, holdoutKey) {
		for _, ref := range items.Refs() {
			fmt.Fprintf(c.out, "withheld\t%s\tv%d\n", ref.ID, ref.Version)
		}
		items = nil
	}
	chars := 0
	for _, k := range items {
		chars += utf8.RuneCountInString(k.Text())
		fmt.Fprintf(c.out, "%s\tv%d\t%s\t%s\n", k.ID, k.Version, k.Kind, k.Content)
	}
	fmt.Fprintf(c.out, "total\t%d items\t%d chars\n", len(items), chars)
	return nil
}

// The candidates that would reach a run of the producer with these labels, with the runs whose verdicts taught them
func (c knowledgeCommand) waiting(ctx context.Context, producer string, labels trace.Labels) error {
	if producer == "" {
		return fmt.Errorf("waiting: --producer %w", errRequired)
	}
	all, err := c.ledger.All(ctx)
	if err != nil {
		return err
	}
	waiting := all.Waiting(producer, labels)
	if len(waiting) == 0 {
		return nil
	}
	replays, err := c.lastReplays(ctx)
	if err != nil {
		return err
	}
	for _, k := range waiting {
		fmt.Fprintf(c.out, "%s\tv%d\t%s\t%s\tscope %s\tfrom %s\treplay %s\n",
			k.ID, k.Version, k.Kind, k.Content, k.Run, strings.Join(k.Evidence.FeedbackTraceIDs, " "), replays.of(k.ID, k.Version))
	}
	return nil
}

// Prints the candidate and its scope and its veto and the folder it would join so the person sees how wide it reaches before approving
func (c knowledgeCommand) add(ctx context.Context, draft knowledge.Knowledge) error {
	k, overlaps, err := c.ledger.Propose(ctx, draft)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "%s\tv%d\t%s\nscope\t%s\n", k.ID, k.Version, k.Status, k.Run)
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
	if f.Producer == "" {
		fmt.Fprintln(c.out, "folder\tnone because a judgment with a veto acts through the guard and joins no run")
		return nil
	}
	fmt.Fprintf(c.out, "folder\t%d of %d chars in runs of %s\t%s\n", f.Chars, knowledge.RunChars, f.Producer, f)
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

// Approve and retire move one version to a new status under the person's name
// 1. without the terminal proof the call records an agent proposal once the version is found and changes nothing
// 2. a failed export still prints the status line because the record is written and the error follows
// 3. the intervention follows the record since it names the record written
// 4. the veto line appears only when the export worked and the approved vetoes changed
func (c knowledgeCommand) transition(ctx context.Context, action string, move mover, judgment trace.Intervention) error {
	if judgment.Proof == trace.ProofNone {
		if err := c.proposable(ctx, judgment); err != nil {
			return err
		}
		return c.proposal(ctx, action, judgment)
	}
	before, err := c.ledger.All(ctx)
	if err != nil {
		return err
	}
	k, err := move(ctx, judgment.ID, judgment.Version, judgment.Person)
	if err != nil && !errors.Is(err, knowledge.ErrExport) {
		return err
	}
	fmt.Fprintf(c.out, "%s\tv%d\t%s\t%s\n", k.ID, k.Version, k.Status, k.Approver)
	if ierr := c.intervened(ctx, judgment, knowledge.Set{k}); ierr != nil || err != nil {
		return errors.Join(err, ierr)
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

// The judgment of a target under the proof of the process
// 1. a person is named by --approver, then approver of config.json, then person
// 2. the reserved name is refused whatever the proof
// 3. a proposal names no person
func (c knowledgeCommand) judgment(target trace.Intervention, approver string) (trace.Intervention, error) {
	name := cmp.Or(approver, c.app.cfg.approver, unnamedPerson)
	if err := knowledge.CheckApprover(name); err != nil {
		return trace.Intervention{}, err
	}
	target.Action, target.Proof = trace.ActionJudge, c.proof
	if c.proof.Authority() == trace.AuthorityPerson {
		target.Person = name
	}
	return target, nil
}

// Records the intervention of a judgment with the records it wrote
func (c knowledgeCommand) intervened(ctx context.Context, judgment trace.Intervention, wrote knowledge.Set) error {
	tr, err := judgment.Trace(wrote.Written(), "", c.app.now())
	if err != nil {
		return err
	}
	store, err := c.app.traces()
	if err != nil {
		return err
	}
	return store.Append(ctx, tr.Record())
}

// Records the judgment as an agent proposal and says so
// Nothing changes until the person answers the question made of it
func (c knowledgeCommand) proposal(ctx context.Context, action string, judgment trace.Intervention) error {
	if err := c.intervened(ctx, judgment, nil); err != nil {
		return err
	}
	target := judgment.Compaction
	if target == "" {
		target = fmt.Sprintf("%s\tv%d", judgment.ID, judgment.Version)
	}
	fmt.Fprintf(c.out, "proposal\t%s\t%s is recorded as a proposal and nothing changed. "+
		"Only a person at an interactive terminal outside an agent session decides, so the person is asked about it\n", target, action)
	return nil
}

// Whether the version a proposal names is recorded and a scope it picks reaches only labels a recorded run carries
// So a proposal never asks the person about a version that is not there
func (c knowledgeCommand) proposable(ctx context.Context, judgment trace.Intervention) error {
	if judgment.Choice == trace.ChoiceScope {
		scope, err := c.ledger.ScopeIn(ctx, judgment.ID, judgment.Version, judgment.Labels)
		if err != nil {
			return err
		}
		return c.recorded(ctx, scope)
	}
	all, err := c.ledger.All(ctx)
	if err != nil {
		return err
	}
	_, err = all.Version(judgment.ID, judgment.Version)
	return err
}

// Writes the approved vetoes and rules again after a failed export or before the first import of the rules
// The rules line names the import a CLAUDE.md needs so every session loads them
func (c knowledgeCommand) export(ctx context.Context) error {
	if err := c.ledger.Export(ctx); err != nil {
		return err
	}
	all, err := c.ledger.All(ctx)
	if err != nil {
		return err
	}
	c.vetoLine(len(all.Vetoes()))
	if len(all.Approved()) == 0 {
		fmt.Fprintln(c.out, "rules\tnot written because no item is approved")
		return nil
	}
	fmt.Fprintf(c.out, "rules\t%d approved in %s\timport it from CLAUDE.md with the line @%s\n",
		len(all.Approved()), c.rulesPath, c.rulesPath)
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
// Only with the terminal proof since the records name their approvers and skip the status checks
func (c knowledgeCommand) importFile(ctx context.Context, path string) error {
	if path == "" {
		return fmt.Errorf("--file %w", errRequired)
	}
	if c.proof == trace.ProofNone {
		return fmt.Errorf("import: %w", errPersonOnly)
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
