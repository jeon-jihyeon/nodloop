package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jeon-jihyeon/nodloop/internal/record"
)

// The traces of one tenant
type TraceStore struct {
	pool   *pgxpool.Pool
	tenant string
}

func (s *TraceStore) Append(ctx context.Context, t record.Trace) error {
	return insert(ctx, s.pool, "traces", s.tenant, `INSERT INTO nodloop_traces (tenant, id, name, session_id, data) VALUES ($1, $2, $3, $4, $5)`,
		t, t.ID, t.Name, t.SessionID)
}

// The newest trace of the id
func (s *TraceStore) Get(ctx context.Context, id string) (record.Trace, error) {
	found, err := s.List(ctx, record.TraceFilter{ID: id, Limit: 1})
	if err != nil {
		return record.Trace{}, err
	}
	if len(found) == 0 {
		return record.Trace{}, fmt.Errorf("%w: %q", record.ErrTraceNotFound, id)
	}
	return found[0], nil
}

// The fields of a trace filter both trace queries narrow by as $2 to $5
// Matches of the filter decides the rest so the store answers exactly what the file store does
const traceNarrowed = ` AND ($2 = '' OR id = $2) AND ($3 = '' OR name = $3) AND ($4 = '' OR session_id = $4)
	AND (coalesce(cardinality($5::text[]), 0) = 0 OR name = ANY($5))`

// Newest first
// Output is left out after the decode since json has no operator that drops a key and jsonb rewrites the bytes of the input
func (s *TraceStore) List(ctx context.Context, f record.TraceFilter) ([]record.Trace, error) {
	rows, err := s.pool.Query(ctx, `SELECT data FROM nodloop_traces WHERE tenant = $1`+traceNarrowed+` ORDER BY seq DESC`,
		s.tenant, f.ID, f.Name, f.SessionID, f.Names)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRead, err)
	}
	found, err := collect(rows, f.Limit, f.Matches)
	if f.WithoutOutput {
		for i := range found {
			found[i].Output = nil
		}
	}
	return found, err
}

// The traces the filter keeps after the position oldest first with the position after the last row read
// 1. a position no read answered fails with record.ErrPositionGone
// 2. Limit counts the rows the indexed fields keep so a filter of other fields may answer fewer and the next read goes on
func (s *TraceStore) After(ctx context.Context, f record.TraceFilter, from string) ([]record.Trace, string, error) {
	at, err := parsePosition(from)
	if err != nil {
		return nil, from, err
	}
	rows, err := s.pool.Query(ctx, `SELECT seq, data FROM nodloop_traces WHERE tenant = $1`+traceNarrowed+` AND seq > $6 ORDER BY seq LIMIT $7`,
		planEach, s.tenant, f.ID, f.Name, f.SessionID, f.Names, at, limitOf(f.Limit))
	if err != nil {
		return nil, from, fmt.Errorf("%w: %w", ErrRead, err)
	}
	return appended(rows, from, f.Matches)
}

// The traces of the tenant with the names newest first without their output
// json_to_record parses each row once into the fields a prune reads so no output leaves the database
func (s *TraceStore) Heads(ctx context.Context, names ...string) ([]record.Trace, error) {
	rows, err := s.pool.Query(ctx, `SELECT h.id, h.name, h.time, coalesce(h.ref, ''), coalesce(h.session_id, ''), coalesce(h.duration_ms, 0),
		coalesce(h.error, ''), h.input
		FROM nodloop_traces t,
		json_to_record(t.data) AS h(id text, name text, time text, ref text, session_id text, duration_ms bigint, error text, input json)
		WHERE t.tenant = $1 AND t.name = ANY($2) ORDER BY t.seq DESC`, s.tenant, names)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRead, err)
	}
	defer rows.Close()
	var out []record.Trace
	for rows.Next() {
		var t record.Trace
		var at string
		var input []byte
		if err := rows.Scan(&t.ID, &t.Name, &at, &t.Ref, &t.SessionID, &t.DurationMS, &t.Error, &input); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrRead, err)
		}
		if t.Time, err = time.Parse(time.RFC3339Nano, at); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrRead, err)
		}
		t.Input = input
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRead, err)
	}
	return out, nil
}

// Ids one DELETE of a prune takes so a first prune of a large tenant never writes its whole removal in one transaction
const pruneBatch = 5000

// Removes the traces of the tenant with the ids decide returns in batches and returns how many it removed
// 1. a run stays while a verdict or an outcome or a knowledge record cites it when its DELETE runs
// So a citation committed after decide read the citations still keeps its run
// 2. a classify trace stays while its run is cited that way
// 3. no other table loses a row
func (s *TraceStore) Prune(ctx context.Context, decide func(context.Context) ([]string, error)) (int, error) {
	ids, err := decide(ctx)
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrWrite, err)
	}
	removed := 0
	for batch := range slices.Chunk(ids, pruneBatch) {
		tag, err := s.pool.Exec(ctx, pruneSQL, s.tenant, batch)
		if err != nil {
			return removed, fmt.Errorf("%w: %w", ErrWrite, err)
		}
		removed += int(tag.RowsAffected())
	}
	return removed, nil
}

// One batch of a prune
// run is the id of a run trace and the ref of a classify trace so a citation of the run keeps both
const pruneSQL = `WITH doomed AS (
	SELECT seq, CASE WHEN name = 'classify' THEN data->>'ref' ELSE id END AS run
	FROM nodloop_traces WHERE tenant = $1 AND id = ANY($2)
), cited AS (
	SELECT trace_id AS run FROM nodloop_feedback WHERE tenant = $1 AND trace_id IN (SELECT run FROM doomed)
	UNION SELECT trace_id FROM nodloop_outcomes WHERE tenant = $1 AND trace_id IN (SELECT run FROM doomed)
	UNION SELECT json_array_elements_text(data->'evidence'->'feedback_trace_ids') FROM nodloop_knowledge WHERE tenant = $1
	UNION SELECT json_array_elements_text(data->'evidence'->'outcome_trace_ids') FROM nodloop_knowledge WHERE tenant = $1
)
DELETE FROM nodloop_traces WHERE seq IN (SELECT seq FROM doomed d WHERE NOT EXISTS (SELECT 1 FROM cited c WHERE c.run = d.run))`

// The feedback of one tenant
type FeedbackStore struct {
	pool   *pgxpool.Pool
	tenant string
}

func (s *FeedbackStore) Append(ctx context.Context, fb record.Feedback) error {
	return insert(ctx, s.pool, "feedback", s.tenant, `INSERT INTO nodloop_feedback (tenant, trace_id, data) VALUES ($1, $2, $3)`, fb, fb.TraceID)
}

// Newest first
func (s *FeedbackStore) List(ctx context.Context, f record.FeedbackFilter) ([]record.Feedback, error) {
	rows, err := s.pool.Query(ctx, `SELECT data FROM nodloop_feedback WHERE tenant = $1 AND ($2 = '' OR trace_id = $2) ORDER BY seq DESC`,
		s.tenant, f.TraceID)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRead, err)
	}
	return collect(rows, f.Limit, f.Matches)
}

// The outcomes of one tenant
type OutcomeStore struct {
	pool   *pgxpool.Pool
	tenant string
}

func (s *OutcomeStore) Append(ctx context.Context, o record.Outcome) error {
	return insert(ctx, s.pool, "outcomes", s.tenant, `INSERT INTO nodloop_outcomes (tenant, trace_id, data) VALUES ($1, $2, $3)`, o, o.TraceID)
}

// Newest first and every outcome for an empty trace id
func (s *OutcomeStore) List(ctx context.Context, traceID string) ([]record.Outcome, error) {
	rows, err := s.pool.Query(ctx, `SELECT data FROM nodloop_outcomes WHERE tenant = $1 AND ($2 = '' OR trace_id = $2) ORDER BY seq DESC`,
		s.tenant, traceID)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRead, err)
	}
	return collect(rows, 0, record.OutcomeFilter{TraceID: traceID}.Matches)
}

// The verdicts after the position oldest first with the position after the last row read
// A position no read answered fails with record.ErrPositionGone
func (s *FeedbackStore) After(ctx context.Context, from string, limit int) ([]record.Feedback, string, error) {
	return after[record.Feedback](ctx, s.pool, "nodloop_feedback", s.tenant, from, limit)
}

// The outcomes after the position oldest first with the position after the last row read
// A position no read answered fails with record.ErrPositionGone
func (s *OutcomeStore) After(ctx context.Context, from string, limit int) ([]record.Outcome, string, error) {
	return after[record.Outcome](ctx, s.pool, "nodloop_outcomes", s.tenant, from, limit)
}

// The seq of the last row a read in append order took and zero before every row
// A form no read answered fails with record.ErrPositionGone
func parsePosition(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	seq, err := strconv.ParseInt(s, 10, 64)
	if err != nil || seq <= 0 {
		return 0, fmt.Errorf("%w: %q", record.ErrPositionGone, s)
	}
	return seq, nil
}

// A read in append order is planned with its values every time
// A cached generic plan walks seq through the rows of every tenant to find the few of a small tenant
const planEach = pgx.QueryExecModeCacheDescribe

// The limit of a query with null for every row
func limitOf(n int) any {
	if n <= 0 {
		return nil
	}
	return n
}

// The rows of the table of the tenant after the position in append order up to the limit
func after[T any](ctx context.Context, pool *pgxpool.Pool, table, tenant, from string, limit int) ([]T, string, error) {
	at, err := parsePosition(from)
	if err != nil {
		return nil, from, err
	}
	rows, err := pool.Query(ctx, `SELECT seq, data FROM `+table+` WHERE tenant = $1 AND seq > $2 ORDER BY seq LIMIT $3`,
		planEach, tenant, at, limitOf(limit))
	if err != nil {
		return nil, from, fmt.Errorf("%w: %w", ErrRead, err)
	}
	return appended(rows, from, func(T) bool { return true })
}

// The decoded rows keep holds for and the position after the last row read
func appended[T any](rows pgx.Rows, from string, keep func(T) bool) ([]T, string, error) {
	defer rows.Close()
	var out []T
	next := from
	for rows.Next() {
		var seq int64
		var data []byte
		if err := rows.Scan(&seq, &data); err != nil {
			return nil, from, fmt.Errorf("%w: %w", ErrRead, err)
		}
		var r T
		if err := json.Unmarshal(data, &r); err != nil {
			return nil, from, fmt.Errorf("%w: %w", ErrRead, err)
		}
		if keep(r) {
			out = append(out, r)
		}
		next = strconv.FormatInt(seq, 10)
	}
	if err := rows.Err(); err != nil {
		return nil, from, fmt.Errorf("%w: %w", ErrRead, err)
	}
	return out, next, nil
}

// Appends the record as the data of one row of the tenant under the lock of its kind
// The sql takes the tenant first then the fields and the data last
func insert(ctx context.Context, pool *pgxpool.Pool, kind, tenant, sql string, record any, fields ...any) error {
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrAppend, err)
	}
	err = locked(ctx, pool, kind, tenant, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, sql, slices.Concat([]any{tenant}, fields, []any{data})...)
		return err
	})
	if err != nil {
		return fmt.Errorf("%w: %w", ErrAppend, err)
	}
	return nil
}

// Runs fn in a transaction that holds the advisory lock of the records of one kind of the tenant until it ends
// 1. every append takes it before its row draws a seq so seq order is commit order within the tenant
// So a read in append order pages by seq alone and never passes a row that commits later
// 2. other tenants never wait and a hash collision only makes two tenants take turns
// 3. the key of knowledge is the one its decisions took in earlier releases so a server of one still takes turns
func locked(ctx context.Context, pool *pgxpool.Pool, kind, tenant string, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('nodloop:' || $1 || ':' || $2))`, kind, tenant); err != nil {
			return err
		}
		return fn(tx)
	})
}

// The decoded rows that keep matches up to limit with zero for all
func collect[T any](rows pgx.Rows, limit int, keep func(T) bool) ([]T, error) {
	defer rows.Close()
	var out []T
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrRead, err)
		}
		var record T
		if err := json.Unmarshal(data, &record); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrRead, err)
		}
		if keep(record) {
			out = append(out, record)
		}
		if limit > 0 && len(out) == limit {
			break
		}
	}
	if err := rows.Err(); err != nil && !errors.Is(err, context.Canceled) {
		return nil, fmt.Errorf("%w: %w", ErrRead, err)
	}
	return out, nil
}
