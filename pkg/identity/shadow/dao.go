package shadow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

// DAO 是影子表的访问入口。
type DAO struct {
	db *sql.DB
}

// NewDAO 构造 DAO。
func NewDAO(db *sql.DB) *DAO { return &DAO{db: db} }

// GetByProvider 用 (provider, subject, tenant_id) 三元组查询 shadow_user。
//
// 未命中返回 nil, nil（不是错误）；调用方决定是否创建。
func (d *DAO) GetByProvider(ctx context.Context, provider, subject, tenantID string) (*ShadowUser, error) {
	if d == nil || d.db == nil {
		return nil, errors.New("identity-go: shadow.DAO not initialized")
	}
	if provider == "" || subject == "" {
		return nil, errors.New("identity-go: provider and subject required")
	}
	if tenantID == "" {
		tenantID = "default"
	}

	const q = `
SELECT su.shadow_user_id, su.canonical_user_id, su.status,
       su.display_name, su.primary_email, su.created_at, su.updated_at
FROM shadow_user_providers sp
JOIN shadow_users su ON su.shadow_user_id = sp.shadow_user_id
WHERE sp.provider = $1 AND sp.subject = $2 AND sp.tenant_id = $3
LIMIT 1`
	row := d.db.QueryRowContext(ctx, q, provider, subject, tenantID)
	var su ShadowUser
	err := row.Scan(&su.ShadowUserID, &su.CanonicalUserID, &su.Status,
		&su.DisplayName, &su.PrimaryEmail, &su.CreatedAt, &su.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("identity-go: GetByProvider failed: %w", err)
	}
	return &su, nil
}

// Record 记录或刷新一条 (provider, subject, tenant_id) → shadow_user_id 映射。
//
// 行为：
//  1. 若 (provider, subject, tenant_id) 已有记录 → 刷新 last_seen_at + external_id + metadata
//  2. 若无记录 + ShadowUserID 空 → 自动创建 shadow_users 行 + shadow_user_providers 行
//  3. 若无记录 + ShadowUserID 非空 → 用指定 ID（外键约束有效时）
//
// displayName / primaryEmail 为空时自动创建场景会用空字符串占位，后续由调用方 UpdateProfile 刷新。
//
// 返回：shadow_user_id, canonical_user_id, isNewlyCreated, error
func (d *DAO) Record(ctx context.Context, sp ShadowProvider) (shadowID, canonicalID string, isNew bool, err error) {
	if d == nil || d.db == nil {
		return "", "", false, errors.New("identity-go: shadow.DAO not initialized")
	}
	if sp.Provider == "" || sp.Subject == "" {
		return "", "", false, errors.New("identity-go: provider and subject required")
	}
	if sp.TenantID == "" {
		sp.TenantID = "default"
	}
	if sp.Metadata == "" {
		sp.Metadata = "{}"
	}
	if sp.LinkedAt.IsZero() {
		sp.LinkedAt = time.Now().UTC()
	}
	sp.LastSeenAt = time.Now().UTC()

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", false, fmt.Errorf("identity-go: begin tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	// Serialize first-link creation for the same logical identity key. The
	// database primary key remains the final invariant under concurrent writers.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1 || ':' || $2 || ':' || $3, 0))`, sp.Provider, sp.Subject, sp.TenantID); err != nil {
		return "", "", false, fmt.Errorf("identity-go: lock mapping: %w", err)
	}

	// 1. 检查现有映射
	var existingShadowID string
	row := tx.QueryRowContext(ctx, `
SELECT shadow_user_id FROM shadow_user_providers
WHERE provider = $1 AND subject = $2 AND tenant_id = $3`, sp.Provider, sp.Subject, sp.TenantID)
	switch err = row.Scan(&existingShadowID); {
	case errors.Is(err, sql.ErrNoRows):
		err = nil // 未命中，正常情况
	case err != nil:
		return "", "", false, fmt.Errorf("identity-go: scan existing mapping: %w", err)
	}

	if existingShadowID != "" {
		// 已有映射 → 刷新 last_seen_at
		_, err = tx.ExecContext(ctx, `
UPDATE shadow_user_providers
SET last_seen_at = $1, external_id = NULLIF($2, ''), metadata = $3::jsonb
WHERE provider = $4 AND subject = $5 AND tenant_id = $6`,
			sp.LastSeenAt, sp.ExternalID, sp.Metadata,
			sp.Provider, sp.Subject, sp.TenantID)
		if err != nil {
			return "", "", false, fmt.Errorf("identity-go: update mapping: %w", err)
		}
		// 取 canonical_user_id
		var canonical string
		if err = tx.QueryRowContext(ctx,
			`SELECT canonical_user_id FROM shadow_users WHERE shadow_user_id = $1`,
			existingShadowID).Scan(&canonical); err != nil {
			return "", "", false, fmt.Errorf("identity-go: fetch canonical: %w", err)
		}
		if err = tx.Commit(); err != nil {
			return "", "", false, fmt.Errorf("identity-go: commit: %w", err)
		}
		if err = d.audit(ctx, sp.Provider, "link", sp.Provider, sp.Subject, existingShadowID, false); err != nil {
			// audit 失败不阻断主流程
			_ = err
		}
		return existingShadowID, canonical, false, nil
	}

	// 2. 创建新映射
	shadowID = sp.ShadowUserID
	canonicalID = uuid.NewString() // 每次新建 shadow_user 配一个新 canonical
	if shadowID == "" {
		shadowID = uuid.NewString()
	}

	// 2a. INSERT shadow_users（如已存在则跳过）
	_, err = tx.ExecContext(ctx, `
INSERT INTO shadow_users (shadow_user_id, canonical_user_id, status)
VALUES ($1, $2, 'active')
ON CONFLICT (shadow_user_id) DO NOTHING`,
		shadowID, canonicalID)
	if err != nil {
		// 如果是 FK 冲突（canonical_user_id 已被别的 shadow_user 占用），重新生成
		var pqErr *pq.Error
		if errors.As(err, &pqErr) && pqErr.Code.Name() == "unique_violation" {
			canonicalID = uuid.NewString()
			_, err = tx.ExecContext(ctx, `
INSERT INTO shadow_users (shadow_user_id, canonical_user_id, status)
VALUES ($1, $2, 'active')
ON CONFLICT (shadow_user_id) DO NOTHING`,
				shadowID, canonicalID)
		}
		if err != nil {
			return "", "", false, fmt.Errorf("identity-go: insert shadow_users: %w", err)
		}
	}

	// 2b. INSERT shadow_user_providers
	_, err = tx.ExecContext(ctx, `
INSERT INTO shadow_user_providers
(provider, subject, tenant_id, shadow_user_id, external_id, metadata, linked_at, last_seen_at)
VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6::jsonb, $7, $8)`,
		sp.Provider, sp.Subject, sp.TenantID, shadowID, sp.ExternalID, sp.Metadata,
		sp.LinkedAt, sp.LastSeenAt)
	if err != nil {
		return "", "", false, fmt.Errorf("identity-go: insert shadow_user_providers: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return "", "", false, fmt.Errorf("identity-go: commit: %w", err)
	}

	if err = d.audit(ctx, sp.Provider, "auto_create", sp.Provider, sp.Subject, shadowID, true); err != nil {
		_ = err
	}

	return shadowID, canonicalID, true, nil
}

// UpdateProfile 更新 shadow_users 的展示字段（display_name / primary_email / status）。
func (d *DAO) UpdateProfile(ctx context.Context, shadowUserID, displayName, primaryEmail, status string) error {
	if d == nil || d.db == nil {
		return errors.New("identity-go: shadow.DAO not initialized")
	}
	if shadowUserID == "" {
		return errors.New("identity-go: shadow_user_id required")
	}
	_, err := d.db.ExecContext(ctx, `
UPDATE shadow_users
SET display_name = COALESCE(NULLIF($2, ''), display_name),
    primary_email = COALESCE(NULLIF($3, ''), primary_email),
    status = COALESCE(NULLIF($4, ''), status),
    updated_at = NOW()
WHERE shadow_user_id = $1`,
		shadowUserID, displayName, primaryEmail, status)
	return err
}

// ReconcileOrphans 标记 last_seen_at 早于 olderThan 的映射为孤儿（写入 audit，不删除）。
//
// 返回被标记的 (provider, subject) 列表。
func (d *DAO) ReconcileOrphans(ctx context.Context, olderThan time.Duration) ([]ShadowProvider, error) {
	if d == nil || d.db == nil {
		return nil, errors.New("identity-go: shadow.DAO not initialized")
	}
	if olderThan <= 0 {
		return nil, errors.New("identity-go: olderThan must be > 0")
	}
	cutoff := time.Now().UTC().Add(-olderThan)
	rows, err := d.db.QueryContext(ctx, `
SELECT provider, subject, tenant_id, shadow_user_id, external_id, metadata, linked_at, last_seen_at
FROM shadow_user_providers
WHERE last_seen_at < $1
ORDER BY last_seen_at ASC LIMIT 1000`, cutoff)
	if err != nil {
		return nil, fmt.Errorf("identity-go: query orphans: %w", err)
	}
	defer rows.Close()
	var orphans []ShadowProvider
	for rows.Next() {
		var sp ShadowProvider
		if err := rows.Scan(&sp.Provider, &sp.Subject, &sp.TenantID, &sp.ShadowUserID,
			&sp.ExternalID, &sp.Metadata, &sp.LinkedAt, &sp.LastSeenAt); err != nil {
			return nil, fmt.Errorf("identity-go: scan orphan: %w", err)
		}
		orphans = append(orphans, sp)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 写 audit
	for _, sp := range orphans {
		_ = d.audit(ctx, sp.Provider, "reconcile_orphan", sp.Provider, sp.Subject, sp.ShadowUserID, true)
	}
	return orphans, nil
}

// ListProvidersByShadow 返回某 shadow_user 的所有 (provider, subject, tenant_id) 映射。
func (d *DAO) ListProvidersByShadow(ctx context.Context, shadowUserID string) ([]ShadowProvider, error) {
	if d == nil || d.db == nil {
		return nil, errors.New("identity-go: shadow.DAO not initialized")
	}
	rows, err := d.db.QueryContext(ctx, `
SELECT provider, subject, tenant_id, shadow_user_id, external_id, metadata, linked_at, last_seen_at
FROM shadow_user_providers
WHERE shadow_user_id = $1
ORDER BY linked_at ASC`, shadowUserID)
	if err != nil {
		return nil, fmt.Errorf("identity-go: list providers: %w", err)
	}
	defer rows.Close()
	var out []ShadowProvider
	for rows.Next() {
		var sp ShadowProvider
		if err := rows.Scan(&sp.Provider, &sp.Subject, &sp.TenantID, &sp.ShadowUserID,
			&sp.ExternalID, &sp.Metadata, &sp.LinkedAt, &sp.LastSeenAt); err != nil {
			return nil, err
		}
		out = append(out, sp)
	}
	return out, rows.Err()
}

// audit 写入 shadow_audit；失败不阻断主流程（best-effort）。
func (d *DAO) audit(ctx context.Context, actorProject, action, targetProvider, targetSubject, targetShadowID string, includeMetadata bool) error {
	meta := "{}"
	if includeMetadata {
		if b, err := json.Marshal(map[string]any{
			"actor_project": actorProject,
			"ts":            time.Now().UTC(),
		}); err == nil {
			meta = string(b)
		}
	}
	_, err := d.db.ExecContext(ctx, `
INSERT INTO shadow_audit (actor_project, action, target_provider, target_subject, target_shadow_id, metadata)
VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6::jsonb)`,
		actorProject, action, targetProvider, targetSubject, targetShadowID, meta)
	return err
}
