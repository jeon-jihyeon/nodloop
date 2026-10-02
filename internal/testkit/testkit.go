// Package testkit opens empty record stores for the tests of every module above the stores
package testkit

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	feedbackfile "github.com/jeon-jihyeon/nodloop/internal/feedback/file"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	knowledgefile "github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
	vetofile "github.com/jeon-jihyeon/nodloop/internal/veto/file"
)

// Counts the reads of a wrapped store and fails every read after the allowed ones
type Reads struct {
	Allowed int
	Err     error
	count   int
}

func (r *Reads) next() error {
	r.count++
	if r.count > r.Allowed {
		return r.Err
	}
	return nil
}

// The ledger reads it so a test can let Prepare see the items and make the selection fail
type FlakyKnowledge struct {
	*knowledgefile.Store
	Reads
}

func (f *FlakyKnowledge) List(ctx context.Context) ([]knowledge.Knowledge, error) {
	if err := f.next(); err != nil {
		return nil, err
	}
	return f.Store.List(ctx)
}

type FlakyFeedback struct {
	*feedbackfile.Store
	Reads
}

func (f *FlakyFeedback) List(ctx context.Context, filter feedback.Filter) ([]feedback.Feedback, error) {
	if err := f.next(); err != nil {
		return nil, err
	}
	return f.Store.List(ctx, filter)
}

// Panics on the append of a trace of Name
// A test proves that a panic under a held lock never blocks a later call
type PanickingTraces struct {
	*tracefile.Store
	Name trace.Name
}

func (p PanickingTraces) Append(ctx context.Context, tr trace.Trace) error {
	if tr.Name == p.Name {
		panic("store failed")
	}
	return p.Store.Append(ctx, tr)
}

// One temp record dir behind every store
// The ledger runs on the clock
type Stores struct {
	Traces   *tracefile.Store
	Feedback *feedbackfile.Store
	Outcomes *feedbackfile.OutcomeStore
	Ledger   *knowledge.Ledger
	Clock    *Clock
}

// Empty record stores in a temp dir and a clock the test advances
func Open(t *testing.T) Stores {
	t.Helper()
	dir := t.TempDir()
	traces, err := tracefile.New(dir)
	require.NoError(t, err)
	verdicts, err := feedbackfile.New(dir)
	require.NoError(t, err)
	outcomes, err := feedbackfile.NewOutcomeStore(dir)
	require.NoError(t, err)
	items, err := knowledgefile.New(dir)
	require.NoError(t, err)
	clock := &Clock{now: time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)}
	ledger := knowledge.NewLedger(
		items, vetofile.NewApprovedFile(t.TempDir(), dir), clock.Now, func(prefix string) string { return prefix + "generated" },
	)
	return Stores{Traces: traces, Feedback: verdicts, Outcomes: outcomes, Ledger: ledger, Clock: clock}
}

// Steps a second per call so two calls around a model call give a positive duration
// A second keeps trace ids apart since they carry the millisecond
const ClockStep = time.Second

// A clock that advances a fixed step on every read
// Safe for parallel use
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

// The next read is one step later
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now
	c.now = now.Add(ClockStep)
	return now
}

// The error of a call whose value the test does not need such as the records an import appended
func Err[T any](_ T, err error) error {
	return err
}
