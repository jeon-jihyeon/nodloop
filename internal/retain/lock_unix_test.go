//go:build unix

package retain_test

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/retain"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// A process that finds the marker locked skips at once and prunes nothing
func TestDailySkipsAHeldLock(t *testing.T) {
	st := testkit.Open(t)
	add(t, st, trace.NameRun, 40, "", `{"applied":[]}`)
	held, err := os.OpenFile(filepath.Join(st.Dir, retain.MarkerFile), os.O_CREATE|os.O_RDWR, 0o600)
	require.NoError(t, err)
	t.Cleanup(func() { _ = held.Close() })
	require.NoError(t, syscall.Flock(int(held.Fd()), syscall.LOCK_EX))
	retainer := retain.New(st.Traces, st.Feedback, st.Outcomes, st.Ledger)
	daily := retain.NewDaily(retain.NewMarker(st.Dir), 30, func() (*retain.Retainer, error) { return retainer, nil }, func() time.Time { return now })
	done := make(chan bool, 1)

	go func() {
		ran, err := daily.Run(context.Background())
		assert.NoError(t, err)
		done <- ran
	}()

	select {
	case ran := <-done:
		assert.False(t, ran)
	case <-time.After(5 * time.Second):
		t.Fatal("the trigger waited for the lock")
	}
	left, err := st.Traces.List(context.Background(), record.TraceFilter{})
	require.NoError(t, err)
	assert.Len(t, left, 1)
}
