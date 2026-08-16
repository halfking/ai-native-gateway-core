package credentialquota

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PGQuerier is the minimal subset of pgx required to load quota policies.
// Defined here so tests can inject pgxmock without depending on the
// production *pgxpool.Pool.
type PGQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (PGRows, error)
}

// PGRows abstracts pgx.Rows for testability.
type PGRows interface {
	Next() bool
	Close()
	Err() error
	Scan(dest ...any) error
}

// PGSource loads quota policies from public.credential_client_quota.
type PGSource struct {
	pool PGQuerier
}

// NewPGSource wraps a *pgxpool.Pool into the minimal PGQuerier contract.
func NewPGSource(pool *pgxpool.Pool) *PGSource {
	if pool == nil {
		return &PGSource{}
	}
	return &PGSource{pool: pgxPoolQuerier{pool}}
}

type pgxPoolQuerier struct{ p *pgxpool.Pool }

func (q pgxPoolQuerier) Query(ctx context.Context, sql string, args ...any) (PGRows, error) {
	rows, err := q.p.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgxRowsAdapter{rows}, nil
}

type pgxRowsAdapter struct {
	r interface {
		Next() bool
		Close()
		Err() error
		Scan(...any) error
	}
}

func (a pgxRowsAdapter) Next() bool          { return a.r.Next() }
func (a pgxRowsAdapter) Close()              { a.r.Close() }
func (a pgxRowsAdapter) Err() error          { return a.r.Err() }
func (a pgxRowsAdapter) Scan(d ...any) error { return a.r.Scan(d...) }

const loadPoliciesSQL = `
SELECT credential_id, owner_tenant_id, client_type,
       max_concurrent, max_fp_slots, fp_enforce_after
FROM public.credential_client_quota
WHERE max_concurrent IS NOT NULL OR max_fp_slots IS NOT NULL`

// LoadPolicies implements PolicySource. Returns ErrRedis (semantic) only on
// infrastructure failure; an empty result is a valid "no rows" answer.
func (s *PGSource) LoadPolicies(ctx context.Context) ([]Policy, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("credential client quota: nil pg pool")
	}
	rows, err := s.pool.Query(ctx, loadPoliciesSQL)
	if err != nil {
		return nil, fmt.Errorf("credential client quota query: %w", err)
	}
	defer rows.Close()
	var out []Policy
	for rows.Next() {
		var p Policy
		if err := rows.Scan(
			&p.CredentialID,
			&p.OwnerTenantID,
			&p.ClientType,
			&p.MaxConcurrent,
			&p.MaxFPSlots,
			&p.FPEnforceAfter,
		); err != nil {
			return nil, fmt.Errorf("credential client quota scan: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("credential client quota rows: %w", err)
	}
	return out, nil
}
