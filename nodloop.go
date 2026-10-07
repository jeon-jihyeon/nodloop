// Package nodloop records the runs of any AI producer and the verdicts people give on them and hands the knowledge they approve to the next run in the same place
package nodloop

import (
	"fmt"
	"os"
	"time"

	feedbackfile "github.com/jeon-jihyeon/nodloop/internal/feedback/file"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	knowledgefile "github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
	"github.com/jeon-jihyeon/nodloop/internal/veto"
	vetofile "github.com/jeon-jihyeon/nodloop/internal/veto/file"
)

// The record directory a producer writes to and reads its knowledge from
// Every method reads the files again so several processes may share one directory
type Client struct {
	traces   *tracefile.Store
	verdicts *feedbackfile.Store
	ledger   *knowledge.Ledger
	now      func() time.Time
}

// How Open builds a client
type Option func(*options)

type options struct {
	now          func() time.Time
	vetoHome     string
	vetoProducer string
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

// The client of the records in dir, created on first use
func Open(dir string, opts ...Option) (*Client, error) {
	o := options{now: time.Now}
	for _, opt := range opts {
		opt(&o)
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
	ledger := knowledge.NewLedger(store, sink, o.now, knowledge.NewIDs(o.now))
	return &Client{traces: traces, verdicts: verdicts, ledger: ledger, now: o.now}, nil
}

// The veto sink of a client without a veto home
// CheckCall reads the vetoes from the records so nothing needs a file
type noVetoFile struct{}

func (noVetoFile) Replace(func() ([]veto.Spec, error)) error { return nil }
