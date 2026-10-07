package pg

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Every record is a JSON row of its tenant so the record types stay the ones the file stores keep
// 1. json and not jsonb so a record reads back byte for byte as written since jsonb reorders keys and spacing
// 2. seq orders the rows of a table so newest first is seq descending as in a jsonl file
const schema = `
CREATE TABLE IF NOT EXISTS nodloop_traces (
	seq bigserial PRIMARY KEY, tenant text NOT NULL, id text NOT NULL, name text NOT NULL, session_id text NOT NULL, data json NOT NULL
);
CREATE INDEX IF NOT EXISTS nodloop_traces_id ON nodloop_traces (tenant, id);
CREATE INDEX IF NOT EXISTS nodloop_traces_name ON nodloop_traces (tenant, name, session_id);
CREATE TABLE IF NOT EXISTS nodloop_feedback (
	seq bigserial PRIMARY KEY, tenant text NOT NULL, trace_id text NOT NULL, data json NOT NULL
);
CREATE INDEX IF NOT EXISTS nodloop_feedback_trace ON nodloop_feedback (tenant, trace_id);
CREATE TABLE IF NOT EXISTS nodloop_outcomes (
	seq bigserial PRIMARY KEY, tenant text NOT NULL, trace_id text NOT NULL, data json NOT NULL
);
CREATE INDEX IF NOT EXISTS nodloop_outcomes_trace ON nodloop_outcomes (tenant, trace_id);
CREATE TABLE IF NOT EXISTS nodloop_knowledge (
	seq bigserial PRIMARY KEY, tenant text NOT NULL, data json NOT NULL
);
CREATE INDEX IF NOT EXISTS nodloop_knowledge_tenant ON nodloop_knowledge (tenant);
CREATE TABLE IF NOT EXISTS nodloop_rules (
	tenant text PRIMARY KEY, body text NOT NULL
);
`

// The advisory lock key that serializes the schema of every process opening one database
// Two CREATE TABLE IF NOT EXISTS at once can both miss the table and one fails on a duplicate type
const schemaLock int64 = 0x6e6f646c6f6f70

// A pool over one database whose tables Open creates when they are missing
type DB struct {
	pool *pgxpool.Pool
}

func Open(ctx context.Context, dsn string) (*DB, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrOpen, err)
	}
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", schemaLock); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, schema)
		return err
	})
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("%w: %w", ErrOpen, err)
	}
	return &DB{pool: pool}, nil
}

func (db *DB) Close() {
	db.pool.Close()
}

func (db *DB) Traces(tenant string) *TraceStore {
	return &TraceStore{pool: db.pool, tenant: tenant}
}

func (db *DB) Feedback(tenant string) *FeedbackStore {
	return &FeedbackStore{pool: db.pool, tenant: tenant}
}

func (db *DB) Outcomes(tenant string) *OutcomeStore {
	return &OutcomeStore{pool: db.pool, tenant: tenant}
}

func (db *DB) Knowledge(tenant string) *KnowledgeStore {
	return &KnowledgeStore{pool: db.pool, tenant: tenant}
}
