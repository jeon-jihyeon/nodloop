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
func TestDailySkipsAHeldMarker(t *testing.T) {
	type want struct {
		ran  bool
		left int
	}
	tcs := []struct {
		name string
		// The flock another process holds on the marker
		args int
		want want
	}{
		{"an exclusive lock skips the prune", syscall.LOCK_EX, want{ran: false, left: 1}},
		{"a shared lock skips the prune", syscall.LOCK_SH, want{ran: false, left: 1}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			st := testkit.Open(t)
			add(t, st, trace.NameRun, 40, "", `{"applied":[]}`)
			other, err := os.OpenFile(filepath.Join(st.Dir, retain.MarkerFile), os.O_CREATE|os.O_RDWR, 0o600)
			require.NoError(t, err)
			t.Cleanup(func() { _ = other.Close() })
			require.NoError(t, syscall.Flock(int(other.Fd()), tc.args))
			retainer := retain.New(st.Traces, st.Feedback, st.Outcomes, st.Ledger)
			open := func() (*retain.Retainer, error) { return retainer, nil }
			daily := retain.NewDaily(retain.NewMarker(st.Dir), 30, open, func() time.Time { return now })
			type result struct {
				ran bool
				err error
			}
			done := make(chan result, 1)

			// The run goes aside so a run that waits for the lock fails the test instead of hanging it
			go func() {
				ran, err := daily.Run(ctx)
				done <- result{ran, err}
			}()
			var got result
			select {
			case got = <-done:
			case <-time.After(5 * time.Second):
				require.FailNow(t, "the run waited for the lock")
			}
			left, err := st.Traces.List(ctx, record.TraceFilter{})
			require.NoError(t, err)

			assert.NoError(t, got.err)
			assert.Equal(t, tc.want, want{ran: got.ran, left: len(left)})
		})
	}
}
