package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jeon-jihyeon/nodloop/internal/record"
)

// The knowledge of one tenant
// A decision holds the lock every append of the knowledge of the tenant takes so two approvals of one candidate never both land
type KnowledgeStore struct {
	pool   *pgxpool.Pool
	tenant string
}

func (s *KnowledgeStore) Append(ctx context.Context, k record.Knowledge) error {
	return insert(ctx, s.pool, "knowledge", s.tenant, `INSERT INTO nodloop_knowledge (tenant, data) VALUES ($1, $2)`, k)
}

func (s *KnowledgeStore) List(ctx context.Context) ([]record.Knowledge, error) {
	return s.list(ctx, s.pool)
}

// decide gets the records newest first and its records land in their order
// Its refusal comes back as it is and only a database failure becomes ErrAppend so a caller can tell the two apart
func (s *KnowledgeStore) AppendDecided(ctx context.Context, decide func(all []record.Knowledge) ([]record.Knowledge, error)) error {
	var refused error
	err := locked(ctx, s.pool, "knowledge", s.tenant, func(tx pgx.Tx) error {
		all, err := s.list(ctx, tx)
		if err != nil {
			return err
		}
		records, err := decide(all)
		if err != nil {
			refused = err
			return err
		}
		for _, k := range records {
			data, err := json.Marshal(k)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO nodloop_knowledge (tenant, data) VALUES ($1, $2)`, s.tenant, data); err != nil {
				return err
			}
		}
		return nil
	})
	switch {
	case err == nil:
		return nil
	case refused != nil:
		return refused
	}
	return fmt.Errorf("%w: %w", ErrAppend, err)
}

// Stores the rules render returns as the approved.md text of the tenant
// An empty render while the tenant has no rules writes nothing
func (s *KnowledgeStore) ReplaceRules(ctx context.Context, render func(all []record.Knowledge) string) error {
	err := locked(ctx, s.pool, "knowledge", s.tenant, func(tx pgx.Tx) error {
		all, err := s.list(ctx, tx)
		if err != nil {
			return err
		}
		body := render(all)
		if body == "" {
			_, err = tx.Exec(ctx, `UPDATE nodloop_rules SET body = '' WHERE tenant = $1`, s.tenant)
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO nodloop_rules (tenant, body) VALUES ($1, $2) ON CONFLICT (tenant) DO UPDATE SET body = $2`, s.tenant, body)
		return err
	})
	if err != nil {
		return fmt.Errorf("%w: %w", ErrWrite, err)
	}
	return nil
}

// The approved.md text of the tenant and empty before any approval
func (s *KnowledgeStore) Rules(ctx context.Context) (string, error) {
	var body string
	err := s.pool.QueryRow(ctx, `SELECT body FROM nodloop_rules WHERE tenant = $1`, s.tenant).Scan(&body)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrRead, err)
	}
	return body, nil
}

// What both a pool and a transaction query through
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func (s *KnowledgeStore) list(ctx context.Context, q querier) ([]record.Knowledge, error) {
	rows, err := q.Query(ctx, `SELECT data FROM nodloop_knowledge WHERE tenant = $1 ORDER BY seq DESC`, s.tenant)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRead, err)
	}
	return collect(rows, 0, func(record.Knowledge) bool { return true })
}

// The records after the position oldest first with the position after the last row read
// A position no read answered fails with record.ErrPositionGone
func (s *KnowledgeStore) After(ctx context.Context, from string, limit int) ([]record.Knowledge, string, error) {
	return after[record.Knowledge](ctx, s.pool, "nodloop_knowledge", s.tenant, from, limit)
}
