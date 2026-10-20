package pg_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/feedback/feedbacktest"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge/knowledgetest"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	"github.com/jeon-jihyeon/nodloop/internal/trace/tracetest"
	"github.com/jeon-jihyeon/nodloop/server/internal/pg"
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
	require.NoError(t, db.Traces(tenant+"-acme").Append(ctx, run.Record()))

	other, err := db.Traces(tenant+"-globex").List(ctx, record.TraceFilter{})

	require.NoError(t, err)
	assert.Empty(t, other)
}

// A prune of one tenant never removes the trace of the same id another tenant holds
func TestPruneKeepsOtherTenants(t *testing.T) {
	db, tenant := open(t)
	ctx := context.Background()
	run, err := trace.NewRun("bot", "", nil, []byte(`{"applied":[]}`), []byte(`"x"`), time.Now())
	require.NoError(t, err)
	require.NoError(t, db.Traces(tenant+"-acme").Append(ctx, run.Record()))
	require.NoError(t, db.Traces(tenant+"-globex").Append(ctx, run.Record()))

	removed, err := db.Traces(tenant+"-acme").Prune(ctx, func(context.Context) ([]string, error) { return []string{run.ID}, nil })

	require.NoError(t, err)
	assert.Equal(t, 1, removed)
	left, err := db.Traces(tenant+"-globex").Get(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, run.ID, left.ID)
}

// A citation committed after decide read the citations keeps its run when the DELETE runs
// The ids span two batches so every batch checks the citations
func TestPruneKeepsRunsCitedAfterDecide(t *testing.T) {
	db, tenant := open(t)
	ctx := context.Background()
	traces := db.Traces(tenant)
	runs := map[string]trace.Trace{}
	for _, name := range []string{"judged", "checked", "taught", "unjudged"} {
		run, err := trace.NewRun("bot", name, nil, []byte(`{"applied":[]}`), []byte(`"x"`), time.Now())
		require.NoError(t, err)
		require.NoError(t, traces.Append(ctx, run.Record()))
		runs[name] = run
	}
	classify := trace.Trace{ID: trace.NewID(time.Now()), Name: trace.NameClassify, Ref: runs["judged"].ID, Time: time.Now(), Input: []byte(`{}`)}
	require.NoError(t, traces.Append(ctx, classify.Record()))
	ids := []string{runs["judged"].ID, runs["checked"].ID, runs["taught"].ID, classify.ID}
	for i := range 5000 {
		ids = append(ids, fmt.Sprintf("missing-%d", i))
	}
	ids = append(ids, runs["unjudged"].ID)
	decide := func(context.Context) ([]string, error) {
		fb, err := feedback.New(runs["judged"].ID, feedback.VerdictApprove, "", "", nil, "ann", time.Now())
		require.NoError(t, err)
		require.NoError(t, db.Feedback(tenant).Append(ctx, fb.Record()))
		o, err := feedback.NewOutcome(runs["checked"].ID, feedback.ResultConfirmed, "", "", "ann", time.Now())
		require.NoError(t, err)
		require.NoError(t, db.Outcomes(tenant).Append(ctx, o.Record()))
		require.NoError(t, db.Knowledge(tenant).Append(ctx, record.Knowledge{
			ID: "k", Version: 1, Evidence: record.Evidence{FeedbackTraceIDs: []string{runs["taught"].ID}},
		}))
		return ids, nil
	}

	removed, err := traces.Prune(ctx, decide)

	require.NoError(t, err)
	assert.Equal(t, 1, removed, "only the run nothing cites goes")
	left, err := traces.List(ctx, record.TraceFilter{})
	require.NoError(t, err)
	var got []string
	for _, tr := range left {
		got = append(got, tr.ID)
	}
	assert.ElementsMatch(t, []string{runs["judged"].ID, runs["checked"].ID, runs["taught"].ID, classify.ID}, got)
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
			err := store.AppendDecided(ctx, func(all []record.Knowledge) ([]record.Knowledge, error) {
				if len(all) > 0 {
					return nil, knowledge.ErrNotFound
				}
				return []record.Knowledge{{ID: "once", Version: 1}}, nil
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

// Servers that open one empty database at once all create or find the tables
// Each run uses a schema of its own so the tables are missing when the opens race
func TestOpenConcurrent(t *testing.T) {
	_, tenant := open(t)
	schema := "s" + strings.ToLower(strings.ReplaceAll(tenant, "-", "_"))
	dsn := os.Getenv("NODLOOP_TEST_POSTGRES")
	conn, err := pgx.Connect(context.Background(), dsn)
	require.NoError(t, err)
	_, err = conn.Exec(context.Background(), "CREATE SCHEMA "+schema)
	require.NoError(t, errors.Join(err, conn.Close(context.Background())))
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	errs := make([]error, 8)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Go(func() {
			opened, err := pg.Open(context.Background(), dsn+sep+"search_path="+schema)
			if err == nil {
				opened.Close()
			}
			errs[i] = err
		})
	}
	wg.Wait()

	assert.NoError(t, errors.Join(errs...))
}
