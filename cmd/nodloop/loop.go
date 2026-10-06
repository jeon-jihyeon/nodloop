package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/loop"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// The conversation records joined once for the loop views
func (a app) history(ctx context.Context) (*loop.History, error) {
	traces, err := a.traces()
	if err != nil {
		return nil, err
	}
	verdicts, err := a.feedback()
	if err != nil {
		return nil, err
	}
	outcomes, err := a.outcomes()
	if err != nil {
		return nil, err
	}
	ledger, err := a.ledger()
	if err != nil {
		return nil, err
	}
	return loop.Load(ctx, traces, verdicts, outcomes, ledger)
}

// One JSON row per knowledge version
func (c knowledgeCommand) health(ctx context.Context) error {
	h, err := c.app.history(ctx)
	if err != nil {
		return err
	}
	return json.NewEncoder(c.out).Encode(h.Health(c.app.now()))
}

// One JSON issue per broken reference of a current item
func (c knowledgeCommand) audit(ctx context.Context) error {
	h, err := c.app.history(ctx)
	if err != nil {
		return err
	}
	return json.NewEncoder(c.out).Encode(h.BrokenReferences())
}

// Without a version the approved version of the id is the one reaffirmed
func (c knowledgeCommand) reaffirm(ctx context.Context, id string, version int, approver string) error {
	if id == "" || approver == "" {
		return fmt.Errorf("reaffirm: an id and --approver %w", errRequired)
	}
	if version == 0 {
		var err error
		if version, err = c.ledger.ApprovedVersion(ctx, id); err != nil {
			return err
		}
	}
	k, err := c.ledger.Reaffirm(ctx, id, version, approver)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "%s\tv%d\treaffirmed\t%s\n", k.ID, k.Version, k.Approver)
	return nil
}

// Proposes the next version that excepts the values of the key its refuted runs carried
// Prints the candidate like propose so the person sees what approval would change
func (c knowledgeCommand) narrow(ctx context.Context, id string, version int, key, author string) error {
	if id == "" || version <= 0 {
		return fmt.Errorf("narrow: an id and --version %w", errRequired)
	}
	if key == "" {
		return fmt.Errorf("narrow: --key, the label whose refuted values the item stops reaching, %w", errRequired)
	}
	h, err := c.app.history(ctx)
	if err != nil {
		return err
	}
	n, overlaps, err := c.ledger.Narrow(ctx, id, version, key, h.RefutedValues(id, version, key), h.RefutedTraces(id, version), author)
	if errors.Is(err, knowledge.ErrNarrowExhausted) {
		return fmt.Errorf("%w. Keep the version or retire it by name with nodloop knowledge retire %s --version %d --approver <name>",
			err, id, version)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "%s\tv%d\t%s\tscope %s\n", n.ID, n.Version, n.Status, n.Run)
	for _, o := range overlaps {
		fmt.Fprintf(c.out, "overlaps\t%s\tv%d\t%s\n", o.ID, o.Version, o.Status)
	}
	return c.folder(ctx, n.ID, n.Version)
}

// Proposes the next version with basis verified on the runs whose outcome confirmed it
// Prints the candidate like narrow so the person sees what approval would change
func (c knowledgeCommand) promote(ctx context.Context, id string, version int, author string) error {
	if id == "" || version <= 0 {
		return fmt.Errorf("promote: an id and --version %w", errRequired)
	}
	h, err := c.app.history(ctx)
	if err != nil {
		return err
	}
	k, overlaps, err := c.ledger.Promote(ctx, id, version, h.ConfirmedTraces(id, version), author)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "%s\tv%d\t%s\tbasis %s\toutcomes %v\n", k.ID, k.Version, k.Status, k.Basis, k.Evidence.OutcomeTraceIDs)
	for _, o := range overlaps {
		fmt.Fprintf(c.out, "overlaps\t%s\tv%d\t%s\n", o.ID, o.Version, o.Status)
	}
	return c.folder(ctx, k.ID, k.Version)
}

// The queue and the online report print JSON
type loopCommand struct {
	app app
	out io.Writer
}

func (c loopCommand) queue(ctx context.Context, opts loop.QueueOptions) error {
	h, err := c.app.history(ctx)
	if err != nil {
		return err
	}
	items, err := h.Queue(opts)
	if err != nil {
		return err
	}
	return json.NewEncoder(c.out).Encode(map[string]any{"seed": opts.Seed, "items": items})
}

func (c loopCommand) online(ctx context.Context, since time.Time) error {
	h, err := c.app.history(ctx)
	if err != nil {
		return err
	}
	return json.NewEncoder(c.out).Encode(h.Report(since))
}

func runQueue(args []string, getenv func(string) string, now func() time.Time, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("queue", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var records recordFlags
	records.bind(fs)
	opts := loop.QueueOptions{}
	fs.IntVar(&opts.Limit, "limit", loop.QueueLimit, "runs to list including audit samples. 0 lists every run")
	fs.Float64Var(&opts.AuditRate, "audit-rate", loop.QueueAuditRate, "share of the limit drawn at random from the rest of the order")
	fs.Int64Var(&opts.Seed, "seed", now().UnixNano(), "the same seed draws the same audit samples")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() != 0 {
		return fail(stderr, "queue", fmt.Errorf("%w %q", errUnknownAction, fs.Arg(0)))
	}
	a, err := records.app(getenv, now)
	if err != nil {
		return fail(stderr, "queue", err)
	}
	if err := (loopCommand{app: a, out: stdout}).queue(context.Background(), opts); err != nil {
		return fail(stderr, "queue", err)
	}
	return 0
}

func runReport(args []string, getenv func(string) string, now func() time.Time, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return fail(stderr, "report", errNoAction)
	}
	if show, ok := recordReports[args[0]]; ok {
		return runRecordReport(args, show, getenv, now, stdout, stderr)
	}
	if args[0] != "online" {
		return fail(stderr, "report", fmt.Errorf("%w %q", errUnknownAction, args[0]))
	}
	fs := flag.NewFlagSet("report online", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var records recordFlags
	records.bind(fs)
	since := fs.String("since", "", "only verdicts and outcomes from this RFC3339 time on")
	if err := fs.Parse(args[1:]); err != nil {
		return 1
	}
	if fs.NArg() != 0 {
		return fail(stderr, "report", fmt.Errorf("%w %q", errUnknownAction, fs.Arg(0)))
	}
	var start time.Time
	if *since != "" {
		var err error
		if start, err = time.Parse(time.RFC3339, *since); err != nil {
			return fail(stderr, "report", err)
		}
	}
	a, err := records.app(getenv, now)
	if err != nil {
		return fail(stderr, "report", err)
	}
	if err := (loopCommand{app: a, out: stdout}).online(context.Background(), start); err != nil {
		return fail(stderr, "report", err)
	}
	return 0
}

// The reports read from the records alone with their flags parsed the same way
var recordReports = map[string]func(c loopCommand, ctx context.Context) error{
	"loop":    loopCommand.runs,
	"extract": loopCommand.extractions,
	"critic":  loopCommand.critics,
}

func runRecordReport(args []string, show func(loopCommand, context.Context) error, getenv func(string) string, now func() time.Time, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("report "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	var records recordFlags
	records.bind(fs)
	if err := fs.Parse(args[1:]); err != nil {
		return 1
	}
	a, err := records.app(getenv, now)
	if err != nil {
		return fail(stderr, "report", err)
	}
	if err := show(loopCommand{app: a, out: stdout}, context.Background()); err != nil {
		return fail(stderr, "report", err)
	}
	return 0
}

// Every trace and verdict and knowledge record a report reads
type evidence struct {
	traces trace.Traces
	runs   loop.Runs
	items  knowledge.Set
}

func (c loopCommand) evidence(ctx context.Context) (evidence, error) {
	store, err := c.app.traces()
	if err != nil {
		return evidence{}, err
	}
	traces, err := store.List(ctx, trace.Filter{})
	if err != nil {
		return evidence{}, err
	}
	verdicts, err := c.app.feedback()
	if err != nil {
		return evidence{}, err
	}
	records, err := verdicts.List(ctx, feedback.Filter{})
	if err != nil {
		return evidence{}, err
	}
	ledger, err := c.app.ledger()
	if err != nil {
		return evidence{}, err
	}
	items, err := ledger.All(ctx)
	if err != nil {
		return evidence{}, err
	}
	return evidence{traces: traces, runs: loop.NewRuns(traces, records), items: items}, nil
}

// One line per plugin version and path: extractions by conclusion, drafts by refusal and the critic questions answered false
func (c loopCommand) extractions(ctx context.Context) error {
	ev, err := c.evidence(ctx)
	if err != nil {
		return err
	}
	for _, row := range ev.runs.Extractions(ev.traces) {
		fmt.Fprintf(c.out, "extract\t%s\t%s\textractions %d\t%s\trefused drafts %s\tquestions false %s\n",
			row.Version, row.Path, row.Extractions, counts(row.Conclusions), counts(row.Refusals), counts(row.Questions))
	}
	return nil
}

// A duration rounded to the second and at least one second, or a dash for none
func seconds(d time.Duration) string {
	if d <= 0 {
		return "-"
	}
	return max(d.Round(time.Second), time.Second).String()
}

// One line per critic: drafts it judged and how its judgments matched what a person later decided on the run
func (c loopCommand) critics(ctx context.Context) error {
	ev, err := c.evidence(ctx)
	if err != nil {
		return err
	}
	for _, row := range ev.runs.Critics(ev.items, ev.traces) {
		fmt.Fprintf(c.out, "critic\t%s\tjudged %d\tagree %d\tfalse pass %d\tfalse refuse %d\topen %d\n",
			row.Critic, row.Judged, row.Agree, row.FalsePass, row.FalseRefuse, row.Open)
	}
	return nil
}

// Counts as `a 2, b 1` in key order or a dash when there is none
func counts(m map[string]int) string {
	if len(m) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(m))
	for _, k := range slices.Sorted(maps.Keys(m)) {
		parts = append(parts, fmt.Sprintf("%s %d", k, m[k]))
	}
	return strings.Join(parts, ", ")
}

// A first line of totals, then one scope line per plugin version and one drafts line per plugin version and drafting path
// One line per approved run item after them: applied, followed of judged and repeat by people then by a session, and settle
// A last line says misapplied is not measured
func (c loopCommand) runs(ctx context.Context) error {
	ev, err := c.evidence(ctx)
	if err != nil {
		return err
	}
	report, items := ev.runs, ev.items
	t := report.Totals(items)
	fmt.Fprintf(c.out, "loop\truns %d\tjudged %d\tinferred %d\tcorrected %d\twaiting %d\tapproved %d\n",
		t.Runs, t.Judged, t.Inferred, t.Corrected, t.Waiting, t.Approved)
	for _, sc := range report.Scopes(items, ev.traces) {
		fmt.Fprintf(c.out, "scope\t%s\titems %d\tsingle session %d\tnever applied %d\n", sc.Version, sc.Items, sc.SingleSession, sc.NeverApplied)
	}
	for _, d := range report.Drafts(items, ev.traces) {
		fmt.Fprintf(c.out, "drafts\t%s\t%s\tdrafted %d\tapproved %d\tdropped %d\twaiting %d\tdecide %s\n",
			d.Version, d.Path, d.Drafted, d.Approved, d.Dropped, d.Waiting, seconds(d.Decide))
	}
	for _, row := range report.Report(items) {
		fmt.Fprintf(c.out, "%s\tv%d\tapplied %d\tfollowed %d of %d\trepeat %d\tinferred followed %d of %d\tinferred repeat %d\tsettle %s\n",
			row.ID, row.Version, row.Applied, row.Followed, row.Judged, row.Repeat,
			row.InferredFollowed, row.InferredJudged, row.InferredRepeat, seconds(row.Settle))
	}
	fmt.Fprintln(c.out, "misapplied\tnot measured: no label says which runs an item should have reached")
	return nil
}
