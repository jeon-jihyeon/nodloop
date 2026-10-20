//go:build unix

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/jeon-jihyeon/nodloop/internal/consolidate"
	"github.com/jeon-jihyeon/nodloop/internal/llm/llmmock"
)

// A consolidate while another holds the mark asks nothing
// The manual run says so and the daily one stays silent
func TestRunKnowledgeConsolidateWhileAnotherRuns(t *testing.T) {
	tcs := []struct {
		name string
		args []string
		want string
	}{
		{"a manual run says another runs", []string{"consolidate"}, "nodloop knowledge: another consolidate is running\n"},
		{"a daily run stays silent", []string{"consolidate", "--daily"}, ""},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			getenv, records, _ := consolidateHome(t, 2, "")
			held, err := os.OpenFile(filepath.Join(records, consolidate.MarkFile), os.O_CREATE|os.O_RDWR, 0o600)
			require.NoError(t, err)
			t.Cleanup(func() { _ = held.Close() })
			require.NoError(t, syscall.Flock(int(held.Fd()), syscall.LOCK_EX))
			client := llmmock.NewMockClient(gomock.NewController(t))
			var stderr bytes.Buffer

			runKnowledge(tc.args, getenv, client, func() time.Time { return consolidateNow }, &bytes.Buffer{}, &stderr)

			assert.Equal(t, tc.want, stderr.String())
		})
	}
}
