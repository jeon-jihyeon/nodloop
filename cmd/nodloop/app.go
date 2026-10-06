package main

import (
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/classify"
	"github.com/jeon-jihyeon/nodloop/internal/compact"
	"github.com/jeon-jihyeon/nodloop/internal/extract"
	feedbackfile "github.com/jeon-jihyeon/nodloop/internal/feedback/file"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	knowledgefile "github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
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
	return knowledge.NewLedger(store, a.vetoFile(dir), a.now, a.newID), nil
}

// Approved vetoes of the record directory under home
func (a app) vetoFile(dir string) *vetofile.ApprovedFile {
	return vetofile.NewApprovedFile(string(a.cfg.home), dir)
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
// 2. otherwise the plan of the setup with claude as the built in member and every answer recorded
func (a app) critic(claude extract.ClaudeCritic, getenv func(string) string) (extract.Classifier, error) {
	plan, err := a.plan(classify.PointCritic, claude, getenv, classifierTimeout)
	if err != nil || plan == nil {
		return claude, err
	}
	return plan, nil
}

// The plan of the classifiers the user set up for the point and nil when none is set up
// builtin is the member the setup names claude
func (a app) plan(point classify.Point, builtin classify.Classifier, getenv func(string) string, timeout time.Duration) (*classify.Plan, error) {
	setup, ok := a.cfg.decisions[point]
	if !ok {
		return nil, nil
	}
	if err := setup.Check(); err != nil {
		return nil, fmt.Errorf("%w: decisions.%s in %s", err, point, configFile)
	}
	members, err := a.cfg.classifiers.Members(setup, builtin, getenv, timeout)
	if err != nil {
		return nil, err
	}
	traces, err := a.traces()
	if err != nil {
		return nil, err
	}
	return classify.NewPlan(point, setup, members, traces, a.now), nil
}

// The prefix and the clock milliseconds in hex and two random bytes so two ids in one millisecond differ
func (a app) newID(prefix string) string {
	var suffix [2]byte
	// crypto rand Read never returns an error
	_, _ = rand.Read(suffix[:])
	return fmt.Sprintf("%s%x%x", prefix, a.now().UnixMilli(), suffix)
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
		if err := t.CheckRun(); err != nil {
			return err
		}
	}
	return nil
}
