package pg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jeon-jihyeon/nodloop/internal/retain"
)

// The last prune of one tenant so every replica of the server and every restart reads one mark
type PruneStore struct {
	pool   *pgxpool.Pool
	tenant string
}

// One statement claims the mark so two replicas never both prune a tenant within every
// A mark from the future is taken over so a clock set back never stops the prune
func (s *PruneStore) Claim(ctx context.Context, at, cutoff time.Time, every time.Duration) (bool, error) {
	tag, err := s.pool.Exec(ctx, `INSERT INTO nodloop_prunes AS p (tenant, at, cutoff) VALUES ($1, $2, $3)
		ON CONFLICT (tenant) DO UPDATE SET at = EXCLUDED.at, cutoff = EXCLUDED.cutoff
		WHERE p.at > EXCLUDED.at OR p.at <= EXCLUDED.at - make_interval(secs => $4)`,
		s.tenant, at, cutoff, every.Seconds())
	if err != nil {
		return false, fmt.Errorf("%w: %w", ErrWrite, err)
	}
	return tag.RowsAffected() == 1, nil
}

// The zero Mark before the first prune of the tenant
func (s *PruneStore) Last(ctx context.Context) (retain.Mark, error) {
	var m retain.Mark
	err := s.pool.QueryRow(ctx, `SELECT at, cutoff FROM nodloop_prunes WHERE tenant = $1`, s.tenant).Scan(&m.At, &m.Cutoff)
	if errors.Is(err, pgx.ErrNoRows) {
		return retain.Mark{}, nil
	}
	if err != nil {
		return retain.Mark{}, fmt.Errorf("%w: %w", ErrRead, err)
	}
	return m, nil
}
