package pg_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/feedback/feedbacktest"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge/knowledgetest"
	"github.com/jeon-jihyeon/nodloop/internal/pg"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	"github.com/jeon-jihyeon/nodloop/internal/trace/tracetest"
)

// The database the suites run against and a tenant no other test uses
// Without NODLOOP_TEST_POSTGRES the test is skipped
func open(t *testing.T) (*pg.DB, string) {
	t.Helper()
	dsn := os.Getenv("NODLOOP_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set NODLOOP_TEST_POSTGRES to a database URL to run the Postgres suites")
	}
	db, err := pg.Open(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(db.Close)
	var suffix [4]byte
	_, _ = rand.Read(suffix[:])
	return db, strings.ReplaceAll(t.Name(), "/", "-") + "-" + hex.EncodeToString(suffix[:])
}

func TestTraceStore(t *testing.T) {
	db, tenant := open(t)
	tracetest.Run(t, db.Traces(tenant))
}

func TestFeedbackStore(t *testing.T) {
	db, tenant := open(t)
	feedbacktest.Run(t, db.Feedback(tenant))
}

func TestKnowledgeStore(t *testing.T) {
	db, tenant := open(t)
	knowledgetest.Run(t, db.Knowledge(tenant))
}

// Two tenants of one database never read each other's records
func TestTenantsApart(t *testing.T) {
	db, tenant := open(t)
	ctx := context.Background()
	run, err := trace.NewRun("bot", "", trace.Labels{"tenant": {"acme"}}, []byte(`{"applied":[]}`), []byte(`"x"`), time.Now())
	require.NoError(t, err)
	require.NoError(t, db.Traces(tenant+"-acme").Append(ctx, run))

	other, err := db.Traces(tenant+"-globex").List(ctx, trace.Filter{})

	require.NoError(t, err)
	assert.Empty(t, other)
}

// Concurrent decisions of one tenant see each other so only one of two approvals of the same candidate lands
func TestKnowledgeDecisionsSerialize(t *testing.T) {
	db, tenant := open(t)
	ctx := context.Background()
	store := db.Knowledge(tenant)
	var wg sync.WaitGroup
	landed := make(chan bool, 8)
	for range 8 {
		wg.Go(func() {
			err := store.AppendDecided(ctx, func(all knowledge.Set) ([]knowledge.Knowledge, error) {
				if len(all) > 0 {
					return nil, knowledge.ErrNotFound
				}
				return []knowledge.Knowledge{{ID: "once", Version: 1}}, nil
			})
			landed <- err == nil
		})
	}
	wg.Wait()
	close(landed)
	count := 0
	for ok := range landed {
		if ok {
			count++
		}
	}

	all, err := store.List(ctx)

	require.NoError(t, err)
	assert.Equal(t, 1, count)
	assert.Len(t, all, 1)
}
