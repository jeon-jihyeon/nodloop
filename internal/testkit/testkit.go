// Package testkit opens the demo data and empty record stores for the tests of every module above the stores
package testkit

import (
	"context"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	evidencefile "github.com/jeon-jihyeon/nodloop/internal/evidence/file"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	feedbackfile "github.com/jeon-jihyeon/nodloop/internal/feedback/file"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	knowledgefile "github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
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

// The demo source and one temp record dir behind every store
// The ledger runs on the clock
type Stores struct {
	Source   *evidencefile.Source
	Traces   *tracefile.Store
	Feedback *feedbackfile.Store
	Outcomes *feedbackfile.OutcomeStore
	Ledger   *knowledge.Ledger
	Clock    *Clock
}

func Open(t *testing.T) Stores {
	t.Helper()
	src, err := evidencefile.New(DemoDir(t))
	require.NoError(t, err)
	dir := t.TempDir()
	traces, err := tracefile.New(dir)
	require.NoError(t, err)
	verdicts, err := feedbackfile.New(dir)
	require.NoError(t, err)
	outcomes, err := feedbackfile.NewOutcomeStore(dir)
	require.NoError(t, err)
	items, err := knowledgefile.New(dir)
	require.NoError(t, err)
	// Later than every record in the demo data
	clock := &Clock{now: time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)}
	ledger := knowledge.NewLedger(items, clock.Now, func() string { return "k-generated" })
	return Stores{Source: src, Traces: traces, Feedback: verdicts, Outcomes: outcomes, Ledger: ledger, Clock: clock}
}

// The demo data set checked in next to the evidence file source
// Resolved from this file so a test in any package finds it
func DemoDir(t *testing.T) string {
	t.Helper()
	_, self, _, ok := runtime.Caller(0)
	require.True(t, ok, "caller unknown")
	return filepath.Join(filepath.Dir(self), "..", "evidence", "file", "testdata")
}

// Steps a second per call so two calls around a model call give a positive duration
// A second keeps trace ids apart since they carry the millisecond
const ClockStep = time.Second

// A clock that advances a fixed step on every read
// Safe for the parallel reviews of eval
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
