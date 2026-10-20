// Package testkit opens empty record stores for the tests of every module above the stores
package testkit

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	feedbackfile "github.com/jeon-jihyeon/nodloop/internal/feedback/file"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	knowledgefile "github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/schema"
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

func (f *FlakyKnowledge) List(ctx context.Context) ([]record.Knowledge, error) {
	if err := f.next(); err != nil {
		return nil, err
	}
	return f.Store.List(ctx)
}

type FlakyFeedback struct {
	*feedbackfile.Store
	Reads
}

func (f *FlakyFeedback) List(ctx context.Context, filter record.FeedbackFilter) ([]record.Feedback, error) {
	if err := f.next(); err != nil {
		return nil, err
	}
	return f.Store.List(ctx, filter)
}

// Fails Heads and Prune after the allowed calls so a test sees a prune fail at its read or at its removal
// Append fails after the allowed appends so a test sees a trace that is never recorded
// List fails after the allowed lists so a test sees one read of a pass fail after the others
type FlakyTraces struct {
	*tracefile.Store
	Reads
	// Counted apart from the reads
	Appends Reads
	Lists   Reads
}

func (f *FlakyTraces) Append(ctx context.Context, tr record.Trace) error {
	if err := f.Appends.next(); err != nil {
		return err
	}
	return f.Store.Append(ctx, tr)
}

func (f *FlakyTraces) List(ctx context.Context, filter record.TraceFilter) ([]record.Trace, error) {
	if err := f.Lists.next(); err != nil {
		return nil, err
	}
	return f.Store.List(ctx, filter)
}

func (f *FlakyTraces) Heads(ctx context.Context, names ...string) ([]record.Trace, error) {
	if err := f.next(); err != nil {
		return nil, err
	}
	return f.Store.Heads(ctx, names...)
}

func (f *FlakyTraces) Prune(ctx context.Context, decide func(context.Context) ([]string, error)) (int, error) {
	if err := f.next(); err != nil {
		return 0, err
	}
	return f.Store.Prune(ctx, decide)
}

// Panics on the append of a trace of Name
// A test proves that a panic under a held lock never blocks a later call
type PanickingTraces struct {
	*tracefile.Store
	Name trace.Name
}

func (p PanickingTraces) Append(ctx context.Context, tr record.Trace) error {
	if tr.Name == string(p.Name) {
		panic("store failed")
	}
	return p.Store.Append(ctx, tr)
}

// One temp record dir behind every store
// The ledger runs on the clock
type Stores struct {
	Dir      string
	Traces   *tracefile.Store
	Feedback *feedbackfile.Store
	Outcomes *feedbackfile.OutcomeStore
	// The store behind the ledger as the raw record pages read it
	Knowledge *knowledgefile.Store
	Ledger    *knowledge.Ledger
	Clock     *Clock
}

// The stores of the record dir as the raw record pages read them
// No guard log since the harness keeps no home
func (s Stores) Source() schema.Source {
	return schema.Source{Traces: s.Traces, Verdicts: s.Feedback, Outcomes: s.Outcomes, Knowledge: s.Knowledge}
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
	return Stores{Dir: dir, Traces: traces, Feedback: verdicts, Outcomes: outcomes, Knowledge: items, Ledger: ledger, Clock: clock}
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

// Moves the clock on without a read such as past the settle time of a records page
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// The error of a call whose value the test does not need such as the records an import appended
func Err[T any](_ T, err error) error {
	return err
}
