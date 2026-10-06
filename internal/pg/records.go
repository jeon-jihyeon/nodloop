package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// The traces of one tenant
type TraceStore struct {
	pool   *pgxpool.Pool
	tenant string
}

func (s *TraceStore) Append(ctx context.Context, t trace.Trace) error {
	data, err := json.Marshal(t)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrAppend, err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO nodloop_traces (tenant, id, name, session_id, data) VALUES ($1, $2, $3, $4, $5)`,
		s.tenant, t.ID, string(t.Name), t.SessionID, data); err != nil {
		return fmt.Errorf("%w: %w", ErrAppend, err)
	}
	return nil
}

// The newest trace of the id
func (s *TraceStore) Get(ctx context.Context, id string) (trace.Trace, error) {
	found, err := s.List(ctx, trace.Filter{ID: id, Limit: 1})
	if err != nil {
		return trace.Trace{}, err
	}
	if len(found) == 0 {
		return trace.Trace{}, fmt.Errorf("trace %q: %w", id, trace.ErrNotFound)
	}
	return found[0], nil
}

// Newest first
// The query narrows by the indexed fields and Matches decides the rest so the store answers exactly what the file store does
func (s *TraceStore) List(ctx context.Context, f trace.Filter) (trace.Traces, error) {
	rows, err := s.pool.Query(ctx, `SELECT data FROM nodloop_traces
		WHERE tenant = $1 AND ($2 = '' OR id = $2) AND ($3 = '' OR name = $3) AND ($4 = '' OR session_id = $4)
		ORDER BY seq DESC`, s.tenant, f.ID, string(f.Name), f.SessionID)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRead, err)
	}
	return collect(rows, f.Limit, f.Matches)
}

// The feedback of one tenant
type FeedbackStore struct {
	pool   *pgxpool.Pool
	tenant string
}

func (s *FeedbackStore) Append(ctx context.Context, fb feedback.Feedback) error {
	return insert(ctx, s.pool, `INSERT INTO nodloop_feedback (tenant, trace_id, data) VALUES ($1, $2, $3)`, s.tenant, fb.TraceID, fb)
}

// Newest first
func (s *FeedbackStore) List(ctx context.Context, f feedback.Filter) ([]feedback.Feedback, error) {
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

func (s *OutcomeStore) Append(ctx context.Context, o feedback.Outcome) error {
	return insert(ctx, s.pool, `INSERT INTO nodloop_outcomes (tenant, trace_id, data) VALUES ($1, $2, $3)`, s.tenant, o.TraceID, o)
}

// Newest first and every outcome for an empty trace id
func (s *OutcomeStore) List(ctx context.Context, traceID string) ([]feedback.Outcome, error) {
	rows, err := s.pool.Query(ctx, `SELECT data FROM nodloop_outcomes WHERE tenant = $1 AND ($2 = '' OR trace_id = $2) ORDER BY seq DESC`,
		s.tenant, traceID)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRead, err)
	}
	return collect(rows, 0, feedback.OutcomeFilter{TraceID: traceID}.Matches)
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
