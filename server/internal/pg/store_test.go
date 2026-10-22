package pg_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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
		o, err := feedback.NewOutcome(runs["checked"].ID, feedback.ResultConfirmed, "", "", "ann", "", time.Now())
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

// An append of knowledge waits while a decision of the tenant holds the lock
// One whose context ends meanwhile fails with ErrAppend and writes nothing
func TestKnowledgeAppendWaitsForDecision(t *testing.T) {
	db, tenant := open(t)
	ctx := context.Background()
	store := db.Knowledge(tenant)
	deciding, release := make(chan struct{}), make(chan struct{})
	decided := make(chan error, 1)
	go func() {
		decided <- store.AppendDecided(ctx, func([]record.Knowledge) ([]record.Knowledge, error) {
			close(deciding)
			<-release
			return []record.Knowledge{{ID: "decided", Version: 1}}, nil
		})
	}()
	select {
	case <-deciding:
	case err := <-decided:
		t.Fatalf("the decision ended before it decided: %v", err)
	}
	timed, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()

	waitErr := store.Append(timed, record.Knowledge{ID: "gave-up", Version: 1})
	close(release)
	require.NoError(t, <-decided)
	all, err := store.List(ctx)
	require.NoError(t, err)

	assert.ErrorIs(t, waitErr, pg.ErrAppend)
	assert.Equal(t, []record.Knowledge{{ID: "decided", Version: 1}}, all)
}

// Servers that open one empty database at once all create or find the tables
// Each run uses a schema of its own so the tables are missing when the opens race
func TestOpenConcurrent(t *testing.T) {
	_, tenant := open(t)
	schema := "s" + strings.ToLower(strings.ReplaceAll(tenant, "-", "_"))
	dsn := os.Getenv("NODLOOP_TEST_POSTGRES")
	conn, err := pgx.Connect(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		_ = conn.Close(context.Background())
	})
	_, err = conn.Exec(context.Background(), "CREATE SCHEMA "+schema)
	require.NoError(t, err)
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

// Outcomes read in append order from the position the read before answered
func TestOutcomeStoreAfter(t *testing.T) {
	db, tenant := open(t)
	ctx := context.Background()
	store := db.Outcomes(tenant)
	base := time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)
	outcomes := []record.Outcome{
		{TraceID: "t1", Time: base, Result: "refuted", Reviewer: "ann"},
		{TraceID: "t2", Time: base.Add(-time.Minute), Result: "confirmed", Reviewer: "ann"},
	}
	for _, o := range outcomes {
		require.NoError(t, store.Append(ctx, o))
	}

	first, at, err := store.After(ctx, "", 1)
	require.NoError(t, err)
	rest, end, err := store.After(ctx, at, 0)
	require.NoError(t, err)
	none, again, err := store.After(ctx, end, 0)
	require.NoError(t, err)
	_, _, goneErr := store.After(ctx, "x", 0)
	_, _, zeroErr := store.After(ctx, "0", 0)

	assert.Equal(t, outcomes[:1], first)
	assert.Equal(t, outcomes[1:], rest)
	assert.Empty(t, none)
	assert.Equal(t, end, again)
	assert.ErrorIs(t, goneErr, record.ErrPositionGone)
	assert.ErrorIs(t, zeroErr, record.ErrPositionGone, "no read answers the position before every row")
}

// An append that cannot land fails with ErrAppend
// 1. a record that does not encode fails before the database
// 2. a canceled append fails before it takes the lock
func TestAppendFails(t *testing.T) {
	db, tenant := open(t)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	tcs := []struct {
		name string
		args func() error
	}{
		{"a trace that does not encode", func() error {
			return db.Traces(tenant).Append(context.Background(), record.Trace{ID: "r1", Input: json.RawMessage(`{`)})
		}},
		{"a canceled trace", func() error {
			return db.Traces(tenant).Append(canceled, record.Trace{ID: "r1", Input: json.RawMessage(`{}`)})
		}},
		{"a canceled verdict", func() error {
			return db.Feedback(tenant).Append(canceled, record.Feedback{TraceID: "r1"})
		}},
		{"a canceled outcome", func() error {
			return db.Outcomes(tenant).Append(canceled, record.Outcome{TraceID: "r1"})
		}},
		{"a canceled knowledge record", func() error {
			return db.Knowledge(tenant).Append(canceled, record.Knowledge{ID: "k"})
		}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.ErrorIs(t, tc.args(), pg.ErrAppend)
		})
	}
}

// A read in append order that cannot go on fails with ErrRead
// 1. a canceled read fails before any row
// 2. a row whose data does not decode fails the read
func TestAfterFails(t *testing.T) {
	db, tenant := open(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, os.Getenv("NODLOOP_TEST_POSTGRES"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close(ctx) })
	_, err = conn.Exec(ctx, `INSERT INTO nodloop_outcomes (tenant, trace_id, data) VALUES ($1, 't1', '{"time":"noon"}')`, tenant)
	require.NoError(t, err)
	canceled, cancel := context.WithCancel(ctx)
	cancel()

	_, _, tracesErr := db.Traces(tenant).After(canceled, record.TraceFilter{}, "")
	_, _, outcomesErr := db.Outcomes(tenant).After(canceled, "", 0)
	_, _, decodeErr := db.Outcomes(tenant).After(ctx, "", 0)

	assert.ErrorIs(t, tracesErr, pg.ErrRead)
	assert.ErrorIs(t, outcomesErr, pg.ErrRead)
	assert.ErrorIs(t, decodeErr, pg.ErrRead)
}

// Appends of one tenant from two connections at once reach a reader paging meanwhile
// 1. every row comes once and none is passed over
// 2. the rows of each writer come in the order it appended them
func TestAfterTakesConcurrentAppendsOnce(t *testing.T) {
	db, tenant := open(t)
	ctx := context.Background()
	other, err := pg.Open(ctx, os.Getenv("NODLOOP_TEST_POSTGRES"))
	require.NoError(t, err)
	t.Cleanup(other.Close)
	writers := map[string]*pg.TraceStore{"a": db.Traces(tenant), "b": other.Traces(tenant)}
	want := map[string][]string{}
	errs := make(chan error, len(writers))
	var wg sync.WaitGroup
	for name, store := range writers {
		var ids []string
		for i := range 200 {
			ids = append(ids, fmt.Sprintf("%s-%03d", name, i))
		}
		want[name] = ids
		wg.Go(func() {
			for _, id := range ids {
				err := store.Append(ctx, record.Trace{ID: id, Name: "run", Input: json.RawMessage(`{}`)})
				if err != nil {
					errs <- err
					return
				}
			}
		})
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	got := map[string][]string{}
	from := ""
	for finished := false; ; {
		select {
		case <-done:
			finished = true
		default:
		}
		page, next, err := db.Traces(tenant).After(ctx, record.TraceFilter{Limit: 7}, from)
		require.NoError(t, err)
		for _, tr := range page {
			got[tr.ID[:1]] = append(got[tr.ID[:1]], tr.ID)
		}
		from = next
		if finished && len(page) == 0 {
			break
		}
	}
	close(errs)

	require.NoError(t, <-errs)
	assert.Equal(t, want, got)
}

// A transaction left open in another database of the cluster never holds a page back
// It takes a transaction id since a snapshot of the cluster counts only such a transaction as open
// The other database is postgres since every cluster has it
func TestAfterIgnoresOtherDatabases(t *testing.T) {
	db, tenant := open(t)
	ctx := context.Background()
	cfg, err := pgx.ParseConfig(os.Getenv("NODLOOP_TEST_POSTGRES"))
	require.NoError(t, err)
	cfg.Database = "postgres"
	conn, err := pgx.ConnectConfig(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close(ctx) })
	tx, err := conn.Begin(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	_, err = tx.Exec(ctx, `SELECT pg_current_xact_id()`)
	require.NoError(t, err)
	at := time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)
	run := record.Trace{ID: "r1", Name: "run", Time: at, Input: json.RawMessage(`{}`)}
	require.NoError(t, db.Traces(tenant).Append(ctx, run))

	page, _, err := db.Traces(tenant).After(ctx, record.TraceFilter{}, "")

	require.NoError(t, err)
	assert.Equal(t, []record.Trace{run}, page)
}
