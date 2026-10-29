package sanitizerules

// PostgresStore 对 ReviewPersistence 的实现（迁移 917 的三张表）。
//
// 纪律（与 postgres.go 既有实现同一条线）：
//   - 不跑 DDL（表由迁移 917 建）；
//   - 错误清洗：返回前剥掉驱动错误里的原文片段（pgx 把 SQL 参数带进
//     message），与 TestPostgresDoesNotRunDDLAndSanitizesErrors 的口径一致；
//   - 记录里没有载荷原文——列集与迁移 917 一一对应。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func sanitizeDBErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	msg := err.Error()
	// 驱动错误可能携带参数原文（含被拦载荷）；只留错误类型描述。
	if idx := strings.Index(msg, " - "); idx > 0 {
		return errors.New(msg[:idx])
	}
	return errors.New(msg)
}

func (s *PostgresStore) FindPendingBlockReview(ctx context.Context, tenantID, scope, rule, digest, requestID string) (BlockReview, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, tenant_id, client_type, scope, rule_id, rule_name, reason, field_path, payload_digest, request_id, match_detail, status, created_at, decided_by, decided_at
FROM sanitize_block_reviews
WHERE status='pending' AND tenant_id=$1 AND scope=$2 AND rule_id=$3 AND payload_digest=$4 AND request_id=$5
ORDER BY id DESC LIMIT 1`, tenantID, scope, rule, digest, requestID)
	r, err := scanBlockReview(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return BlockReview{}, false, nil
	}
	if err != nil {
		return BlockReview{}, false, sanitizeDBErr(err)
	}
	return r, true, nil
}

func (s *PostgresStore) InsertBlockReview(ctx context.Context, r BlockReview) (BlockReview, error) {
	row := s.db.QueryRowContext(ctx, `INSERT INTO sanitize_block_reviews
 (tenant_id, client_type, scope, rule_id, rule_name, reason, field_path, payload_digest, request_id, match_detail, status, created_at)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
 RETURNING id, tenant_id, client_type, scope, rule_id, rule_name, reason, field_path, payload_digest, request_id, match_detail, status, created_at, decided_by, decided_at`,
		r.TenantID, r.ClientType, r.Scope, r.Rule, r.RuleName, r.Reason, r.Field,
		r.Digest, r.RequestID, r.MatchDetail, BlockReviewPending, r.CreatedAt)
	out, err := scanBlockReview(row)
	if err != nil {
		return BlockReview{}, sanitizeDBErr(err)
	}
	return out, nil
}

func (s *PostgresStore) ListBlockReviewsDB(ctx context.Context, status, tenantID string, limit int) ([]BlockReview, error) {
	q := `SELECT id, tenant_id, client_type, scope, rule_id, rule_name, reason, field_path, payload_digest, request_id, match_detail, status, created_at, decided_by, decided_at
FROM sanitize_block_reviews WHERE TRUE`
	args := []any{}
	if status != "" {
		args = append(args, status)
		q += fmt.Sprintf(" AND status=$%d", len(args))
	}
	if tenantID != "" {
		args = append(args, tenantID)
		q += fmt.Sprintf(" AND tenant_id=$%d", len(args))
	}
	args = append(args, limit)
	q += fmt.Sprintf(" ORDER BY created_at DESC, id DESC LIMIT $%d", len(args))
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, sanitizeDBErr(err)
	}
	defer rows.Close()
	out := make([]BlockReview, 0, limit)
	for rows.Next() {
		r, err := scanBlockReview(rows)
		if err != nil {
			return nil, sanitizeDBErr(err)
		}
		out = append(out, r)
	}
	return out, sanitizeDBErr(rows.Err())
}

func (s *PostgresStore) GetBlockReviewDB(ctx context.Context, id int64) (BlockReview, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, tenant_id, client_type, scope, rule_id, rule_name, reason, field_path, payload_digest, request_id, match_detail, status, created_at, decided_by, decided_at
FROM sanitize_block_reviews WHERE id=$1`, id)
	r, err := scanBlockReview(row)
	if err != nil {
		return BlockReview{}, sanitizeDBErr(err)
	}
	return r, nil
}

func (s *PostgresStore) DecideBlockReviewDB(ctx context.Context, id int64, status, decidedBy string, decidedAt time.Time) (BlockReview, error) {
	tag, err := s.db.ExecContext(ctx, `UPDATE sanitize_block_reviews
SET status=$2, decided_by=$3, decided_at=$4
WHERE id=$1 AND status='pending'`, id, status, decidedBy, decidedAt)
	if err != nil {
		return BlockReview{}, sanitizeDBErr(err)
	}
	if affectedRows, _ := tag.RowsAffected(); affectedRows == 0 {
		// 不存在，或已被决定（幂等语义：重复决定不再改写）。
		row, getErr := s.GetBlockReviewDB(ctx, id)
		if getErr != nil {
			return BlockReview{}, getErr
		}
		return row, ErrConflict
	}
	return s.GetBlockReviewDB(ctx, id)
}

func (s *PostgresStore) UpsertEgressReleaseDB(ctx context.Context, r EgressRelease) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO sanitize_egress_releases
 (tenant_id, scope, rule_id, payload_digest, client_type, approved_by, source, created_at, expires_at)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
 ON CONFLICT (tenant_id, scope, rule_id, payload_digest)
 DO UPDATE SET client_type=EXCLUDED.client_type, approved_by=EXCLUDED.approved_by,
   source=EXCLUDED.source, created_at=EXCLUDED.created_at, expires_at=EXCLUDED.expires_at`,
		r.TenantID, r.Scope, r.Rule, r.PayloadDigest, r.ClientType, r.ApprovedBy,
		releaseSourceOrManual(r.Source), time.Now().UTC(), r.ExpiresAt.UTC())
	return sanitizeDBErr(err)
}

func releaseSourceOrManual(source string) string {
	if source != "review" {
		return "manual"
	}
	return source
}

func (s *PostgresStore) DeleteEgressReleaseDB(ctx context.Context, tenantID, scope, rule, digest string) (bool, error) {
	var err error
	var deleted int64
	if digest == "" {
		// digest 为空 = 撤销该键下全部摘要粒度的行（管理面撤销语义）。
		res, execErr := s.db.ExecContext(ctx, `DELETE FROM sanitize_egress_releases
WHERE tenant_id=$1 AND scope=$2 AND rule_id=$3`, tenantID, scope, rule)
		if execErr == nil {
			deleted, _ = res.RowsAffected()
		}
		err = execErr
	} else {
		res, execErr := s.db.ExecContext(ctx, `DELETE FROM sanitize_egress_releases
WHERE tenant_id=$1 AND scope=$2 AND rule_id=$3 AND payload_digest=$4`, tenantID, scope, rule, digest)
		if execErr == nil {
			deleted, _ = res.RowsAffected()
		}
		err = execErr
	}
	if err != nil {
		return false, sanitizeDBErr(err)
	}
	return deleted > 0, nil
}

func (s *PostgresStore) LoadEgressReleasesDB(ctx context.Context) ([]EgressRelease, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT tenant_id, scope, rule_id, payload_digest, client_type, approved_by, source, created_at, expires_at
FROM sanitize_egress_releases`)
	if err != nil {
		return nil, sanitizeDBErr(err)
	}
	defer rows.Close()
	out := []EgressRelease{}
	now := time.Now().UTC()
	for rows.Next() {
		var r EgressRelease
		var createdAt, expiresAt sql.NullTime
		if err := rows.Scan(&r.TenantID, &r.Scope, &r.Rule, &r.PayloadDigest, &r.ClientType, &r.ApprovedBy, &r.Source, &createdAt, &expiresAt); err != nil {
			return nil, sanitizeDBErr(err)
		}
		r.ExpiresAt = expiresAt.Time
		// 已过期行不进内存表（写侧也不再保留）。
		if !r.ExpiresAt.IsZero() && now.After(r.ExpiresAt) {
			continue
		}
		out = append(out, r)
	}
	return out, sanitizeDBErr(rows.Err())
}

func (s *PostgresStore) LoadHardBlockState(ctx context.Context) ([]HardBlockAllow, bool, error) {
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT rules FROM sanitize_hardblock_state WHERE singleton=TRUE`).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, sanitizeDBErr(err)
	}
	var items []string
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, false, sanitizeDBErr(err)
	}
	allows, err := ParseHardBlockAllows(strings.Join(items, ","))
	if err != nil {
		return nil, false, fmt.Errorf("stored hardblock state invalid: %w", err)
	}
	return allows, true, nil
}

func (s *PostgresStore) SaveHardBlockState(ctx context.Context, allows []HardBlockAllow, updatedBy string) error {
	items := make([]string, 0, len(allows))
	for _, a := range allows {
		items = append(items, a.Scope+":"+a.Rule)
	}
	raw, err := json.Marshal(items)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO sanitize_hardblock_state (singleton, rules, updated_by, updated_at)
VALUES (TRUE, $1::jsonb, $2, now())
ON CONFLICT (singleton) DO UPDATE SET rules=EXCLUDED.rules, updated_by=EXCLUDED.updated_by, updated_at=now()`,
		string(raw), updatedBy)
	return sanitizeDBErr(err)
}

// scanBlockReview 是列序的唯一真值源：查询列序与这里逐字对应。
// （rowScanner 由 postgres.go 声明，这里复用同一份。）
func scanBlockReview(row rowScanner) (BlockReview, error) {
	var r BlockReview
	var decidedAt sql.NullTime
	err := row.Scan(&r.ID, &r.TenantID, &r.ClientType, &r.Scope, &r.Rule, &r.RuleName,
		&r.Reason, &r.Field, &r.Digest, &r.RequestID, &r.MatchDetail, &r.Status,
		&r.CreatedAt, &r.DecidedBy, &decidedAt)
	if err != nil {
		return BlockReview{}, err
	}
	r.DecidedAt = decidedAt.Time
	return r, nil
}
