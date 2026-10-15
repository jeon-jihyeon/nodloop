package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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
	data, err := json.Marshal(t)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrAppend, err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO nodloop_traces (tenant, id, name, session_id, data) VALUES ($1, $2, $3, $4, $5)`,
		s.tenant, t.ID, t.Name, t.SessionID, data); err != nil {
		return fmt.Errorf("%w: %w", ErrAppend, err)
	}
	return nil
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

// Newest first
// The query narrows by the indexed fields and Matches decides the rest so the store answers exactly what the file store does
func (s *TraceStore) List(ctx context.Context, f record.TraceFilter) ([]record.Trace, error) {
	rows, err := s.pool.Query(ctx, `SELECT data FROM nodloop_traces
		WHERE tenant = $1 AND ($2 = '' OR id = $2) AND ($3 = '' OR name = $3) AND ($4 = '' OR session_id = $4)
		ORDER BY seq DESC`, s.tenant, f.ID, f.Name, f.SessionID)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRead, err)
	}
	return collect(rows, f.Limit, f.Matches)
}

// The traces of the tenant with the names newest first without their output
// json_to_record parses each row once into the fields a prune reads so no output leaves the database
func (s *TraceStore) Heads(ctx context.Context, names ...string) ([]record.Trace, error) {
	rows, err := s.pool.Query(ctx, `SELECT h.id, h.name, h.time, coalesce(h.ref, ''), coalesce(h.error, ''), h.input
		FROM nodloop_traces t, json_to_record(t.data) AS h(id text, name text, time text, ref text, error text, input json)
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
		if err := rows.Scan(&t.ID, &t.Name, &at, &t.Ref, &t.Error, &input); err != nil {
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
	return insert(ctx, s.pool, `INSERT INTO nodloop_feedback (tenant, trace_id, data) VALUES ($1, $2, $3)`, s.tenant, fb.TraceID, fb)
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
	return insert(ctx, s.pool, `INSERT INTO nodloop_outcomes (tenant, trace_id, data) VALUES ($1, $2, $3)`, s.tenant, o.TraceID, o)
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

func insert(ctx context.Context, pool *pgxpool.Pool, sql, tenant, traceID string, record any) error {
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrAppend, err)
	}
	if _, err := pool.Exec(ctx, sql, tenant, traceID, data); err != nil {
		return fmt.Errorf("%w: %w", ErrAppend, err)
	}
	return nil
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
