package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/classify"
	"github.com/jeon-jihyeon/nodloop/internal/compact"
	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/extract"
	feedbackfile "github.com/jeon-jihyeon/nodloop/internal/feedback/file"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	knowledgefile "github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	"github.com/jeon-jihyeon/nodloop/internal/llm"
	"github.com/jeon-jihyeon/nodloop/internal/loop"
	"github.com/jeon-jihyeon/nodloop/internal/replay"
	"github.com/jeon-jihyeon/nodloop/internal/retain"
	"github.com/jeon-jihyeon/nodloop/internal/textkey"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
	vetofile "github.com/jeon-jihyeon/nodloop/internal/veto/file"
)

// Repeatable string flag
type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

type recordFlags struct {
	recordDir string
}

func (f *recordFlags) bind(fs *flag.FlagSet) {
	fs.StringVar(&f.recordDir, "record-dir", "", "record directory. Overrides "+envRecordDir)
}

func (f recordFlags) app(getenv func(string) string, now func() time.Time) (app, error) {
	cfg, err := resolveConfig(getenv, f.recordDir)
	if err != nil {
		return app{}, err
	}
	return app{cfg: cfg, now: now}, nil
}

// What every command opens from one resolved config
// The record directory is owned by nodloop and created on first use
type app struct {
	cfg config
	now func() time.Time
}

func (a app) makeRecordDir() (string, error) {
	if a.cfg.recordDir == "" {
		return "", fmt.Errorf("%w: set --record-dir or %s", errHomeUnknown, envRecordDir)
	}
	if err := os.MkdirAll(a.cfg.recordDir, 0o755); err != nil {
		return "", fmt.Errorf("record dir: %w", err)
	}
	return a.cfg.recordDir, nil
}

func (a app) traces() (*tracefile.Store, error) {
	dir, err := a.makeRecordDir()
	if err != nil {
		return nil, err
	}
	return tracefile.New(dir)
}

func (a app) feedback() (*feedbackfile.Store, error) {
	dir, err := a.makeRecordDir()
	if err != nil {
		return nil, err
	}
	return feedbackfile.New(dir)
}

func (a app) outcomes() (*feedbackfile.OutcomeStore, error) {
	dir, err := a.makeRecordDir()
	if err != nil {
		return nil, err
	}
	return feedbackfile.NewOutcomeStore(dir)
}

func (a app) ledger() (*knowledge.Ledger, error) {
	dir, err := a.makeRecordDir()
	if err != nil {
		return nil, err
	}
	store, err := knowledgefile.New(dir)
	if err != nil {
		return nil, err
	}
	return knowledge.NewLedger(store, a.vetoFile(dir), a.now, knowledge.NewIDs(a.now)), nil
}

// The item hold that curate.json of the record directory turns on
// No record directory holds nothing since no curate pass could have written its mark
func (a app) itemHold() (knowledge.ItemHold, error) {
	if a.cfg.recordDir == "" {
		return 0, nil
	}
	mark, err := curate.ReadMark(a.cfg.recordDir)
	return mark.Knobs.Hold(), err
}

// Approved vetoes of the record directory under home
// Only those of the conversation producer since the guard hook checks the tool calls of Claude Code
func (a app) vetoFile(dir string) *vetofile.ApprovedFile {
	return vetofile.NewApprovedFile(string(a.cfg.home), dir).Of(sessionProducer)
}

func (a app) compactor(ledger *knowledge.Ledger) (*compact.Compactor, error) {
	traces, err := a.traces()
	if err != nil {
		return nil, err
	}
	verdicts, err := a.feedback()
	if err != nil {
		return nil, err
	}
	return compact.New(ledger, traces, verdicts, traces, a.now), nil
}

// Time an endpoint may take for one request
// The critic runs in the detached extraction so a slow CPU model only delays a draft
// A probe in the foreground waits as long so it shows what the critic would see
const classifierTimeout = 30 * time.Second

// The critic of an extraction
// 1. claude alone with no classify trace while the user set up nothing for the critic point
// 2. otherwise the plan of the endpoint set for the critic point with claude as its fallback and every answer recorded
func (a app) critic(claude extract.ClaudeCritic, getenv func(string) string) (extract.Classifier, error) {
	plan, err := a.plan(classify.PointCritic, classify.Member{Name: builtinMember, Classifier: claude}, getenv, classifierTimeout)
	if err != nil || plan == nil {
		return claude, err
	}
	return plan, nil
}

// The plan of the endpoint the user set for the point and nil when none is set
// 1. builtin is the member the point falls back to
// 2. the HMAC key of the record directory seals what the point keeps of its state
func (a app) plan(point classify.Point, builtin classify.Member, getenv func(string) string, timeout time.Duration) (*classify.Plan, error) {
	endpoints, err := a.cfg.classifiers.endpoints()
	if err != nil {
		return nil, err
	}
	e, ok := endpoints[point]
	if !ok {
		return nil, nil
	}
	traces, err := a.traces()
	if err != nil {
		return nil, err
	}
	key, err := textkey.Load(a.cfg.recordDir)
	if err != nil {
		return nil, err
	}
	endpoint := classify.Member{Name: e.Name(), Classifier: e.Classifier(getenv, timeout)}
	return classify.NewPlan(point, endpoint, builtin, traces, key.Sum, a.now), nil
}

// The replayer of the ledger over the runs and verdicts of the record directory
func (a app) replayer(ledger *knowledge.Ledger, client llm.Client, model string) (*replay.Replayer, error) {
	traces, err := a.traces()
	if err != nil {
		return nil, err
	}
	verdicts, err := a.feedback()
	if err != nil {
		return nil, err
	}
	return replay.New(ledger, traces, verdicts, client, model, a.now), nil
}

// Feedback and outcomes and knowledge may cite only recorded runs
// The first id that names none fails
func (a app) checkRuns(ctx context.Context, ids ...string) error {
	traces, err := a.traces()
	if err != nil {
		return err
	}
	for _, id := range ids {
		t, err := traces.Get(ctx, id)
		if err != nil {
			return err
		}
		if err := trace.Restore(t).CheckRun(); err != nil {
			return err
		}
	}
	return nil
}

// The retainer over every store of the record directory that may cite a run
func (a app) retainer() (*retain.Retainer, error) {
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
	counter := loop.Stores{Traces: traces, Verdicts: verdicts, Outcomes: outcomes, Items: ledger}
	return retain.New(traces, verdicts, outcomes, ledger, counter.Rollup, a.now), nil
}

// The daily prune of the record directory under the retention of config.json
// The retainer opens its stores only when the marker says a prune is due
func (a app) daily() (retain.Daily, error) {
	dir, err := a.makeRecordDir()
	if err != nil {
		return retain.Daily{}, err
	}
	return retain.NewDaily(retain.NewMarker(dir), a.cfg.retention, a.retainer, a.now), nil
}

// The daily retention prune that follows every recorded run
// Nothing runs without retention so the records stay as they are until the user sets one
func (a app) dailyPrune(ctx context.Context) error {
	if a.cfg.retention == 0 {
		return nil
	}
	daily, err := a.daily()
	if err != nil {
		return err
	}
	_, err = daily.Run(ctx)
	return err
}

// The start of the runs the reports read
// The later of the retention of config.json and the cutoff of the last prune of the record directory
// So records a service pruned under its own retention never mix judged runs with the unjudged runs that survived
func (a app) window(ctx context.Context) (time.Time, error) {
	dir, err := a.makeRecordDir()
	if err != nil {
		return time.Time{}, err
	}
	last, err := retain.NewMarker(dir).Last(ctx)
	if err != nil {
		return time.Time{}, err
	}
	return a.cfg.retention.Window(a.now(), last.Cutoff), nil
}

// Starts export otel --pending in a process of its own
// 1. the process names the record directory of this one since a --record-dir flag is not in its environment
// 2. the mark of the export decides what it sends so a start per record resends nothing
func (a app) startExport(start starter) error {
	dir, err := a.makeRecordDir()
	if err != nil {
		return err
	}
	return start([]string{"export", "otel", "--pending", "--record-dir", dir})
}

// The OTLP export after a verdict or an outcome or an approval or a reaffirm the MCP server recorded
// A failure to start goes to log and never fails the call since the record stands
func (a app) afterVerdict(start starter, log io.Writer) func(ctx context.Context) {
	return func(context.Context) {
		if err := a.startExport(start); err != nil {
			fmt.Fprintln(log, "nodloop: export:", err)
		}
	}
}

// The daily prune and the OTLP export after a run the MCP server recorded
func (a app) afterRunExport(start starter, log io.Writer) func(ctx context.Context) {
	prune, export := a.afterRun(log), a.afterVerdict(start, log)
	return func(ctx context.Context) {
		prune(ctx)
		export(ctx)
	}
}

// Starts the OTLP export when config.json holds an otel target
// A command that writes a record in a detached process such as an extraction calls it once its trace is written
func (a app) exportAfter(start starter, log io.Writer) {
	if a.cfg.otel != nil && start != nil {
		a.afterVerdict(start, log)(context.Background())
	}
}

// The daily prune after a run a command recorded
// A failure goes to stderr and never fails the command since the run is recorded
func (a app) afterRun(stderr io.Writer) func(ctx context.Context) {
	return func(ctx context.Context) {
		if err := a.dailyPrune(ctx); err != nil {
			fmt.Fprintln(stderr, "nodloop: retention:", err)
		}
	}
}
