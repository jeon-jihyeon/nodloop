// Package nodloop records the runs of any AI producer and the verdicts people give on them and hands the knowledge they approve to the next run in the same place
package nodloop

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	feedbackfile "github.com/jeon-jihyeon/nodloop/internal/feedback/file"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	knowledgefile "github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	"github.com/jeon-jihyeon/nodloop/internal/loop"
	"github.com/jeon-jihyeon/nodloop/internal/retain"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
	"github.com/jeon-jihyeon/nodloop/internal/veto"
	vetofile "github.com/jeon-jihyeon/nodloop/internal/veto/file"
)

// The record directory a producer writes to and reads its knowledge from
// Every method reads the files again so several processes may share one directory
type Client struct {
	traces   *tracefile.Store
	verdicts *feedbackfile.Store
	outcomes *feedbackfile.OutcomeStore
	ledger   *knowledge.Ledger
	now      func() time.Time
	holdout  knowledge.Holdout
	days     retain.Days
	// The last prune of the directory whatever surface ran it so Report reads only runs no prune touched
	marks retain.Marker
	// The daily prune after Record and nil without retention
	prune *retain.Daily
	// Receives the error of a failed prune and nil drops it
	pruneFailed func(error)
	// The daily prunes Record started and WaitPrune waits for
	pruning sync.WaitGroup
}

// How Open builds a client
type Option func(*options)

type options struct {
	now          func() time.Time
	vetoHome     string
	vetoProducer string
	holdout      float64
	retention    int
	pruneFailed  func(error)
}

// The clock every record takes its time from
func WithClock(now func() time.Time) Option {
	return func(o *options) { o.now = now }
}

// Writes the vetoes the producer approved under the home Claude Code reads its guard vetoes from
// Without it an approval keeps its vetoes in the records alone and CheckCall still reads them
func WithVetoHome(home, producer string) Option {
	return func(o *options) { o.vetoHome, o.vetoProducer = home, producer }
}

// The share of runs whose Draw withholds every item so report effect compares runs with and without them
// 1. the package never reads config.json so a service sets the share itself
// 2. 0 by default withholds nothing
// 3. a share outside 0 up to but not including 1 fails Open with ErrHoldoutInvalid
func WithHoldout(share float64) Option {
	return func(o *options) { o.holdout = share }
}

// Prunes the records at most once a day after Record
// 1. a prune removes the runs older than days that no verdict or outcome or knowledge version cites
// The prune runs in a goroutine of its own and WaitPrune waits for it
// 2. 0 by default keeps every trace
// 3. fewer than 7 days fails Open with ErrRetentionTooShort
// 4. each prune keeps its cutoff in prune.json of the directory so the CLI and MCP reports over it read the same window
func WithRetention(days int) Option {
	return func(o *options) { o.retention = days }
}

// Receives the error of a failed daily prune since Record returns the recorded run without it
// 1. without it the error is dropped and the prune is tried again a day later
// 2. it is called from the goroutine of the prune so it must be safe to call from any goroutine
func WithPruneError(report func(error)) Option {
	return func(o *options) { o.pruneFailed = report }
}

// The client of the records in dir, created on first use
func Open(dir string, opts ...Option) (*Client, error) {
	o := options{now: time.Now}
	for _, opt := range opts {
		opt(&o)
	}
	holdout, days := knowledge.Holdout(o.holdout), retain.Days(o.retention)
	if err := errors.Join(holdout.Check(), days.Check()); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("record dir: %w", err)
	}
	traces, err := tracefile.New(dir)
	if err != nil {
		return nil, err
	}
	verdicts, err := feedbackfile.New(dir)
	if err != nil {
		return nil, err
	}
	store, err := knowledgefile.New(dir)
	if err != nil {
		return nil, err
	}
	var sink knowledge.VetoSink = noVetoFile{}
	if o.vetoHome != "" {
		sink = vetofile.NewApprovedFile(o.vetoHome, dir).Of(o.vetoProducer)
	}
	outcomes, err := feedbackfile.NewOutcomeStore(dir)
	if err != nil {
		return nil, err
	}
	ledger := knowledge.NewLedger(store, sink, o.now, knowledge.NewIDs(o.now))
	c := &Client{
		traces: traces, verdicts: verdicts, outcomes: outcomes, ledger: ledger, now: o.now, holdout: holdout, days: days,
		marks: retain.NewMarker(dir), pruneFailed: o.pruneFailed,
	}
	if days == 0 {
		return c, nil
	}
	counter := loop.Stores{Traces: traces, Verdicts: verdicts, Outcomes: outcomes, Items: ledger}
	retainer := retain.New(traces, verdicts, outcomes, ledger, counter.Rollup, o.now)
	daily := retain.NewDaily(c.marks, days, func() (*retain.Retainer, error) { return retainer, nil }, o.now)
	c.prune = &daily
	return c, nil
}

// The veto sink of a client without a veto home
// CheckCall reads the vetoes from the records so nothing needs a file
type noVetoFile struct{}

func (noVetoFile) Replace(func() ([]veto.Spec, error)) error { return nil }
