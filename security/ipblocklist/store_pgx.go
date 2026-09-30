package ipblocklist

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/internal/dbrows"
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

// scanEntries 把 rows 摊平成条目切片。
//
// 两类失败分开处理：
//  1. 单行 Scan 失败 —— fail-open（该 IP 本轮不被拦），保持裸 continue
//     的容错语义，但必须留痕，否则一条本该命中的封禁条目被悄悄丢掉。
//  2. 迭代中断 —— scanEntries 的签名没有 error 通道，无法自己上抛；
//     调用方（List / ListActive）各自紧跟 `rows.Err()` 上抛，所以**数据
//     不会**被静默返回。但本函数在循环后仍显式查一次并留痕：终端检查是
//     本仓库的逐循环守卫契约（internal/rowsguard），且这里是封禁读面的
//     收敛点，截断必须在本函数内可查，不能只依赖调用方自觉。
func scanEntries(rows pgx.Rows) []Entry {
	var out []Entry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			// R66: 封禁名单的单行失败 = 该 IP 本轮不被拦。保持 fail-open
			//（改成 fail-closed 属语义变更，超出本轮 error-path 范围），
			// 但必须留痕，否则封禁静默失效无人知晓。
			dbrows.WarnRowSkip("ipblocklist.scanEntries", err)
			continue
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		// 迭代中断：截断的封禁名单 = 后续若干 IP 不再被拦。调用方会把本
		// 错误上抛（List / ListActive 紧跟 rows.Err()），此处只留痕。
		slog.Warn("ipblocklist: entry rows iteration aborted; blocklist truncated",
			"op", "ipblocklist.scanEntries", "entries", len(out), "error", err)
	}
	if out == nil {
		out = []Entry{}
	}
	return out
}
