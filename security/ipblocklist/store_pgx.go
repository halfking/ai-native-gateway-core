package ipblocklist

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PgxStore implements Store on PostgreSQL.
type PgxStore struct {
	pool *pgxpool.Pool
}

func NewPgxStore(pool *pgxpool.Pool) *PgxStore {
	return &PgxStore{pool: pool}
}

func (s *PgxStore) List(ctx context.Context, scope string, limit, offset int) ([]Entry, int, error) {
	if limit <= 0 {
		limit = 50
	}
	where := "WHERE 1=1"
	countArgs := []any{}
	listArgs := []any{limit, offset}
	if scope != "" {
		where += " AND scope = $1"
		countArgs = append(countArgs, scope)
		listArgs = append(listArgs, scope)
	}
	var total int
	countQ := "SELECT COUNT(*)::int FROM ip_blocklist " + where
	if err := s.pool.QueryRow(ctx, countQ, countArgs...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limitIdx := 1
	offsetIdx := 2
	scopeIdx := 3
	q := `SELECT id, ip_or_cidr, reason, scope, source, enabled, expires_at, hit_count,
	             created_by, created_at, updated_at
	      FROM ip_blocklist ` + where
	if scope != "" {
		q += fmt.Sprintf(" ORDER BY updated_at DESC LIMIT $%d OFFSET $%d", limitIdx, offsetIdx)
	} else {
		q += fmt.Sprintf(" ORDER BY updated_at DESC LIMIT $%d OFFSET $%d", 1, 2)
		listArgs = []any{limit, offset}
	}
	if scope != "" {
		_ = scopeIdx
	}
	rows, err := s.pool.Query(ctx, q, listArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	return scanEntries(rows), total, rows.Err()
}

func (s *PgxStore) Get(ctx context.Context, id int64) (*Entry, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, ip_or_cidr, reason, scope, source, enabled, expires_at, hit_count,
		       created_by, created_at, updated_at
		FROM ip_blocklist WHERE id = $1`, id)
	e, err := scanEntry(row)
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func (s *PgxStore) Create(ctx context.Context, in CreateInput) (*Entry, error) {
	scope := strings.TrimSpace(in.Scope)
	if scope == "" {
		scope = ScopeGlobal
	}
	source := strings.TrimSpace(in.Source)
	if source == "" {
		source = SourceManual
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO ip_blocklist (ip_or_cidr, reason, scope, source, expires_at, created_by)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, ip_or_cidr, reason, scope, source, enabled, expires_at, hit_count,
		          created_by, created_at, updated_at`,
		strings.TrimSpace(in.IPOrCIDR), strings.TrimSpace(in.Reason), scope, source, in.ExpiresAt, strings.TrimSpace(in.CreatedBy))
	e, err := scanEntry(row)
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func (s *PgxStore) Update(ctx context.Context, id int64, in UpdateInput) (*Entry, error) {
	cur, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	reason := cur.Reason
	if in.Reason != nil {
		reason = strings.TrimSpace(*in.Reason)
	}
	enabled := cur.Enabled
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	expiresAt := cur.ExpiresAt
	if in.ExpiresAt != nil {
		expiresAt = in.ExpiresAt
	}
	row := s.pool.QueryRow(ctx, `
		UPDATE ip_blocklist
		SET reason = $2, enabled = $3, expires_at = $4, updated_at = now()
		WHERE id = $1
		RETURNING id, ip_or_cidr, reason, scope, source, enabled, expires_at, hit_count,
		          created_by, created_at, updated_at`,
		id, reason, enabled, expiresAt)
	e, err := scanEntry(row)
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func (s *PgxStore) Delete(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM ip_blocklist WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("not found")
	}
	return nil
}

func (s *PgxStore) ListActive(ctx context.Context, scope string) ([]Entry, error) {
	q := `
		SELECT id, ip_or_cidr, reason, scope, source, enabled, expires_at, hit_count,
		       created_by, created_at, updated_at
		FROM ip_blocklist
		WHERE enabled = true
		  AND (expires_at IS NULL OR expires_at > now())`
	args := []any{}
	if scope != "" {
		q += " AND (scope = $1 OR scope = $2)"
		args = append(args, scope, ScopeGlobal)
	}
	q += " ORDER BY id"
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEntries(rows), rows.Err()
}

func (s *PgxStore) IncrementHit(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE ip_blocklist SET hit_count = hit_count + 1, updated_at = now() WHERE id = $1`, id)
	return err
}

func (s *PgxStore) IsBlocked(ctx context.Context, ip net.IP, scope string) (bool, *Entry, error) {
	if ip == nil {
		return false, nil, nil
	}
	entries, err := s.ListActive(ctx, scope)
	if err != nil {
		return false, nil, err
	}
	for i := range entries {
		if MatchIP(entries[i].IPOrCIDR, ip) {
			e := entries[i]
			return true, &e, nil
		}
	}
	return false, nil, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanEntry(row rowScanner) (Entry, error) {
	var e Entry
	err := row.Scan(&e.ID, &e.IPOrCIDR, &e.Reason, &e.Scope, &e.Source, &e.Enabled, &e.ExpiresAt,
		&e.HitCount, &e.CreatedBy, &e.CreatedAt, &e.UpdatedAt)
	return e, err
}

func scanEntries(rows pgx.Rows) []Entry {
	var out []Entry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			continue
		}
		out = append(out, e)
	}
	if out == nil {
		out = []Entry{}
	}
	return out
}
