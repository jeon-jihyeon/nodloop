package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/analysis"
	"github.com/jeon-jihyeon/nodloop/internal/compact"
	"github.com/jeon-jihyeon/nodloop/internal/diagnose"
	"github.com/jeon-jihyeon/nodloop/internal/eval"
	"github.com/jeon-jihyeon/nodloop/internal/evidence"
	evidencefile "github.com/jeon-jihyeon/nodloop/internal/evidence/file"
	feedbackfile "github.com/jeon-jihyeon/nodloop/internal/feedback/file"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	knowledgefile "github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	"github.com/jeon-jihyeon/nodloop/internal/llm"
	"github.com/jeon-jihyeon/nodloop/internal/mcp"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
	vetofile "github.com/jeon-jihyeon/nodloop/internal/veto/file"
)

// Repeatable string flag
type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

func (l listFlag) contexts() []evidence.Context {
	out := make([]evidence.Context, 0, len(l))
	for _, v := range l {
		out = append(out, evidence.Context(v))
	}
	return out
}

// Flags every data subcommand shares
// Parsed after the subcommand's own flags are registered
type dataFlags struct {
	source, dataDir, recordDir string
}

func (f *dataFlags) bind(fs *flag.FlagSet) {
	fs.StringVar(&f.source, "source", "", "source implementation. Overrides "+envSource)
	fs.StringVar(&f.dataDir, "data-dir", "", "reference data directory. Overrides "+envFileDir)
	fs.StringVar(&f.recordDir, "record-dir", "", "record directory. Overrides "+envRecordDir)
}

func (f dataFlags) app(getenv func(string) string, now func() time.Time) (app, error) {
	cfg, err := resolveConfig(getenv, f.source, f.dataDir, f.recordDir)
	if err != nil {
		return app{}, err
	}
	return app{cfg: cfg, now: now}, nil
}

// What every data command opens from one resolved config
// The record directory is owned by nodloop and created on first use
type app struct {
	cfg config
	now func() time.Time
}

func (a app) source() (*evidencefile.Source, error) {
	return evidencefile.New(a.cfg.dataDir)
}

// `policy.yaml` in the reference directory
// The data set owns its analyzers so a directory without the file cannot be analyzed
func (a app) policy() (analysis.Policy, error) {
	b, err := os.ReadFile(filepath.Join(a.cfg.dataDir, "policy.yaml"))
	if errors.Is(err, os.ErrNotExist) {
		return analysis.Policy{}, fmt.Errorf("%w: %s", errPolicyMissing, a.cfg.dataDir)
	}
	if err != nil {
		return analysis.Policy{}, err
	}
	return analysis.LoadPolicy(b)
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
	return a.ledgerIn(dir)
}

// Approved vetoes of the record directory under home
func (a app) vetoFile(dir string) *vetofile.ApprovedFile {
	return vetofile.NewApprovedFile(string(a.cfg.home), dir)
}

func (a app) ledgerIn(dir string) (*knowledge.Ledger, error) {
	store, err := knowledgefile.New(dir)
	if err != nil {
		return nil, err
	}
	return knowledge.NewLedger(store, a.vetoFile(dir), a.now, a.newID), nil
}

// The prefix and the clock milliseconds in hex and two random bytes so two ids in one millisecond differ
func (a app) newID(prefix string) string {
	var suffix [2]byte
	// crypto rand Read never returns an error
	_, _ = rand.Read(suffix[:])
	return fmt.Sprintf("%s%x%x", prefix, a.now().UnixMilli(), suffix)
}

// Feedback and outcomes and knowledge may cite only recorded reviews
// The first id that names no review fails
func (a app) checkReviews(ctx context.Context, ids ...string) error {
	traces, err := a.traces()
	if err != nil {
		return err
	}
	for _, id := range ids {
		t, err := traces.Get(ctx, id)
		if err != nil {
			return err
		}
		if err := t.CheckReview(); err != nil {
			return err
		}
	}
	return nil
}

// Everything the review pipeline shares
// Opened once so two consumers never hold two handles on one file
type pipeline struct {
	src      *evidencefile.Source
	policy   analysis.Policy
	traces   *tracefile.Store
	feedback *feedbackfile.Store
	outcomes *feedbackfile.OutcomeStore
	ledger   *knowledge.Ledger
	// Reviews of compaction replays kept apart from traces
	replays *tracefile.Store
	now     func() time.Time
}

func (a app) pipeline() (pipeline, error) {
	src, err := a.source()
	if err != nil {
		return pipeline{}, err
	}
	policy, err := a.policy()
	if err != nil {
		return pipeline{}, err
	}
	dir, err := a.makeRecordDir()
	if err != nil {
		return pipeline{}, err
	}
	traces, err := tracefile.New(dir)
	if err != nil {
		return pipeline{}, err
	}
	feedback, err := feedbackfile.New(dir)
	if err != nil {
		return pipeline{}, err
	}
	outcomes, err := feedbackfile.NewOutcomeStore(dir)
	if err != nil {
		return pipeline{}, err
	}
	ledger, err := a.ledgerIn(dir)
	if err != nil {
		return pipeline{}, err
	}
	replays, err := tracefile.NewReplays(dir)
	if err != nil {
		return pipeline{}, err
	}
	return pipeline{
		src: src, policy: policy, traces: traces, feedback: feedback, outcomes: outcomes, ledger: ledger, replays: replays,
		now: a.now,
	}, nil
}

// The compactor over one ledger without the policy because checking a folder never analyzes an event
// So an approval that only asks whether a compaction is due works on a data set whose policy is broken
func (a app) compactor(ledger *knowledge.Ledger) (*compact.Compactor, error) {
	src, err := a.source()
	if err != nil {
		return nil, err
	}
	traces, err := a.traces()
	if err != nil {
		return nil, err
	}
	verdicts, err := a.feedback()
	if err != nil {
		return nil, err
	}
	dir, err := a.makeRecordDir()
	if err != nil {
		return nil, err
	}
	replays, err := tracefile.NewReplays(dir)
	if err != nil {
		return nil, err
	}
	return compact.New(src, ledger, traces, verdicts, replays), nil
}

func (p pipeline) compactor() *compact.Compactor {
	return compact.New(p.src, p.ledger, p.traces, p.feedback, p.replays)
}

// Reviews over the preview of a compaction into the replay store
// The only diagnoser that reads knowledge other than the ledger
func (p pipeline) replayDiagnoser(ctx context.Context, client llm.Client, id string) (*diagnose.Diagnoser, error) {
	preview, err := p.ledger.Preview(ctx, id)
	if err != nil {
		return nil, err
	}
	return diagnose.New(p.src, p.policy, client, p.replays, p.feedback, preview, p.now), nil
}

// A nil client serves the conversation that writes the review itself
func (p pipeline) diagnoser(client llm.Client) *diagnose.Diagnoser {
	return diagnose.New(p.src, p.policy, client, p.traces, p.feedback, p.ledger, p.now)
}

func (a app) diagnoser(client llm.Client) (*diagnose.Diagnoser, error) {
	p, err := a.pipeline()
	if err != nil {
		return nil, err
	}
	return p.diagnoser(client), nil
}

func (a app) runner(client llm.Client) (*eval.Runner, error) {
	p, err := a.pipeline()
	if err != nil {
		return nil, err
	}
	return eval.New(p.src, p.diagnoser(client), p.traces, p.feedback, p.ledger), nil
}

// The server never calls a model and passes no client
func (a app) server() (*mcp.Server, error) {
	p, err := a.pipeline()
	if err != nil {
		return nil, err
	}
	return mcp.New(
		p.src, p.policy, p.diagnoser(nil), p.traces, p.feedback, p.outcomes, p.ledger, p.compactor(), p.now, buildVersion(), executable(),
	), nil
}
