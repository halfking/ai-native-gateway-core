package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/credential"
)

// SQLiteCredentialStore 基于 SQLite 的 credential 存储，实现 credential.Store。
// 重要：只存储加密后的密文，明文 API key 永远不写入数据库。
type SQLiteCredentialStore struct {
	db *sql.DB
}

var _ credential.Store = (*SQLiteCredentialStore)(nil)

// NewSQLiteCredentialStore 创建 credential store，db 一般来自 OpenSQLite。
func NewSQLiteCredentialStore(db *sql.DB) *SQLiteCredentialStore {
	return &SQLiteCredentialStore{db: db}
}

// Save 保存或更新 credential（UPSERT 语义）。
// 注意：EncryptedKey 必须已加密，本方法不执行加密。
func (s *SQLiteCredentialStore) Save(cred *credential.Credential) error {
	if cred == nil || cred.ID == "" {
		return errors.New("sqlite: credential 不能为空且必须有 ID")
	}
	if len(cred.EncryptedKey) == 0 {
		return errors.New("sqlite: EncryptedKey 不能为空（必须预先加密）")
	}
	if cred.TenantID == "" {
		return errors.New("sqlite: TenantID 不能为空")
	}
	if cred.ProviderID == "" {
		return errors.New("sqlite: ProviderID 不能为空")
	}

	now := time.Now().Unix()
	if cred.CreatedAt.IsZero() {
		cred.CreatedAt = time.Unix(now, 0)
	}
	cred.UpdatedAt = time.Unix(now, 0)

	metadataJSON, err := json.Marshal(cred.Metadata)
	if err != nil {
		return fmt.Errorf("sqlite: 序列化 metadata 失败: %w", err)
	}

	var lastHealthCheck *int64
	if !cred.LastHealthCheck.IsZero() {
		ts := cred.LastHealthCheck.Unix()
		lastHealthCheck = &ts
	}

	_, err = s.db.Exec(`
		INSERT INTO credentials (
			id, tenant_id, provider_id, model, encrypted_key,
			priority, status, max_concurrent, metadata_json,
			last_health_check, consecutive_fails, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			tenant_id = excluded.tenant_id,
			provider_id = excluded.provider_id,
			model = excluded.model,
			encrypted_key = excluded.encrypted_key,
			priority = excluded.priority,
			status = excluded.status,
			max_concurrent = excluded.max_concurrent,
			metadata_json = excluded.metadata_json,
			last_health_check = excluded.last_health_check,
			consecutive_fails = excluded.consecutive_fails,
			updated_at = excluded.updated_at
	`, cred.ID, cred.TenantID, cred.ProviderID, cred.Model, cred.EncryptedKey,
		cred.Priority, string(cred.Status), cred.MaxConcurrent, string(metadataJSON),
		lastHealthCheck, cred.ConsecutiveFails, cred.CreatedAt.Unix(), cred.UpdatedAt.Unix())

	if err != nil {
		return fmt.Errorf("sqlite: 保存 credential %s 失败: %w", cred.ID, err)
	}
	return nil
}

// Get 按 ID 查询 credential。
func (s *SQLiteCredentialStore) Get(id string) (*credential.Credential, bool, error) {
	row := s.db.QueryRow(`
		SELECT id, tenant_id, provider_id, model, encrypted_key,
		       priority, status, max_concurrent, metadata_json,
		       last_health_check, consecutive_fails, created_at, updated_at
		FROM credentials WHERE id = ?
	`, id)

	c := &credential.Credential{}
	var metadataJSON string
	var lastHealthCheck *int64
	var createdAt, updatedAt int64
	var status string

	err := row.Scan(
		&c.ID, &c.TenantID, &c.ProviderID, &c.Model, &c.EncryptedKey,
		&c.Priority, &status, &c.MaxConcurrent, &metadataJSON,
		&lastHealthCheck, &c.ConsecutiveFails, &createdAt, &updatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("sqlite: 查询 credential %s 失败: %w", id, err)
	}

	c.Status = credential.Status(status)
	c.CreatedAt = time.Unix(createdAt, 0)
	c.UpdatedAt = time.Unix(updatedAt, 0)
	if lastHealthCheck != nil {
		c.LastHealthCheck = time.Unix(*lastHealthCheck, 0)
	}

	if err := json.Unmarshal([]byte(metadataJSON), &c.Metadata); err != nil {
		return nil, false, fmt.Errorf("sqlite: 反序列化 metadata 失败: %w", err)
	}

	return c, true, nil
}

// Delete 删除 credential（硬删除，外键级联会删除关联的 bindings）。
func (s *SQLiteCredentialStore) Delete(id string) error {
	result, err := s.db.Exec(`DELETE FROM credentials WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("sqlite: 删除 credential %s 失败: %w", id, err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("sqlite: credential %s 不存在", id)
	}
	return nil
}

// List 列出指定租户的所有 credentials（按状态和更新时间排序）。
func (s *SQLiteCredentialStore) List(tenantID string) ([]*credential.Credential, error) {
	rows, err := s.db.Query(`
		SELECT id, tenant_id, provider_id, model, encrypted_key,
		       priority, status, max_concurrent, metadata_json,
		       last_health_check, consecutive_fails, created_at, updated_at
		FROM credentials
		WHERE tenant_id = ?
		ORDER BY status ASC, updated_at DESC
	`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("sqlite: 列出 tenant %s credentials 失败: %w", tenantID, err)
	}
	defer rows.Close()

	var credentials []*credential.Credential
	for rows.Next() {
		c := &credential.Credential{}
		var metadataJSON string
		var lastHealthCheck *int64
		var createdAt, updatedAt int64
		var status string

		if err := rows.Scan(
			&c.ID, &c.TenantID, &c.ProviderID, &c.Model, &c.EncryptedKey,
			&c.Priority, &status, &c.MaxConcurrent, &metadataJSON,
			&lastHealthCheck, &c.ConsecutiveFails, &createdAt, &updatedAt,
		); err != nil {
			return nil, fmt.Errorf("sqlite: 扫描 credential 行失败: %w", err)
		}

		c.Status = credential.Status(status)
		c.CreatedAt = time.Unix(createdAt, 0)
		c.UpdatedAt = time.Unix(updatedAt, 0)
		if lastHealthCheck != nil {
			c.LastHealthCheck = time.Unix(*lastHealthCheck, 0)
		}

		if err := json.Unmarshal([]byte(metadataJSON), &c.Metadata); err != nil {
			return nil, fmt.Errorf("sqlite: 反序列化 metadata 失败: %w", err)
		}

		credentials = append(credentials, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: 遍历 credentials 失败: %w", err)
	}

	return credentials, nil
}

// ListByProvider 列出指定 provider 的所有 credentials（跨租户，用于内部管理）。
func (s *SQLiteCredentialStore) ListByProvider(providerID string) ([]*credential.Credential, error) {
	rows, err := s.db.Query(`
		SELECT id, tenant_id, provider_id, model, encrypted_key,
		       priority, status, max_concurrent, metadata_json,
		       last_health_check, consecutive_fails, created_at, updated_at
		FROM credentials
		WHERE provider_id = ?
		ORDER BY tenant_id, status ASC, updated_at DESC
	`, providerID)
	if err != nil {
		return nil, fmt.Errorf("sqlite: 列出 provider %s credentials 失败: %w", providerID, err)
	}
	defer rows.Close()

	var credentials []*credential.Credential
	for rows.Next() {
		c := &credential.Credential{}
		var metadataJSON string
		var lastHealthCheck *int64
		var createdAt, updatedAt int64
		var status string

		if err := rows.Scan(
			&c.ID, &c.TenantID, &c.ProviderID, &c.Model, &c.EncryptedKey,
			&c.Priority, &status, &c.MaxConcurrent, &metadataJSON,
			&lastHealthCheck, &c.ConsecutiveFails, &createdAt, &updatedAt,
		); err != nil {
			return nil, fmt.Errorf("sqlite: 扫描 credential 行失败: %w", err)
		}

		c.Status = credential.Status(status)
		c.CreatedAt = time.Unix(createdAt, 0)
		c.UpdatedAt = time.Unix(updatedAt, 0)
		if lastHealthCheck != nil {
			c.LastHealthCheck = time.Unix(*lastHealthCheck, 0)
		}

		if err := json.Unmarshal([]byte(metadataJSON), &c.Metadata); err != nil {
			return nil, fmt.Errorf("sqlite: 反序列化 metadata 失败: %w", err)
		}

		credentials = append(credentials, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: 遍历 credentials 失败: %w", err)
	}

	return credentials, nil
}

// ListWithContext 支持 context 的列表查询（暂未实现取消，预留签名）。
func (s *SQLiteCredentialStore) ListWithContext(ctx context.Context, tenantID string) ([]*credential.Credential, error) {
	return s.List(tenantID)
}
