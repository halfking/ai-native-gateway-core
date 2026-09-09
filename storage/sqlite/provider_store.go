package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/provider"
)

// SQLiteProviderStore 基于 SQLite 的 provider 存储，实现 provider.Store。
type SQLiteProviderStore struct {
	db *sql.DB
}

var _ provider.Store = (*SQLiteProviderStore)(nil)

// NewSQLiteProviderStore 创建 provider store，db 一般来自 OpenSQLite。
func NewSQLiteProviderStore(db *sql.DB) *SQLiteProviderStore {
	return &SQLiteProviderStore{db: db}
}

// Save 保存或更新 provider（UPSERT 语义）。
func (s *SQLiteProviderStore) Save(p *provider.Provider) error {
	if p == nil || p.ID == "" {
		return errors.New("sqlite: provider 不能为空且必须有 ID")
	}
	now := time.Now().Unix()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Unix(now, 0)
	}
	p.UpdatedAt = time.Unix(now, 0)

	modelsJSON, err := json.Marshal(p.Models)
	if err != nil {
		return fmt.Errorf("sqlite: 序列化 models 失败: %w", err)
	}
	headersJSON, err := json.Marshal(p.Headers)
	if err != nil {
		return fmt.Errorf("sqlite: 序列化 headers 失败: %w", err)
	}
	metadataJSON, err := json.Marshal(p.Metadata)
	if err != nil {
		return fmt.Errorf("sqlite: 序列化 metadata 失败: %w", err)
	}

	disabled := 0
	if p.Disabled {
		disabled = 1
	}

	var lastHealthCheck *int64
	if !p.LastHealthCheck.IsZero() {
		ts := p.LastHealthCheck.Unix()
		lastHealthCheck = &ts
	}

	_, err = s.db.Exec(`
		INSERT INTO providers (
			id, name, base_url, protocol, auth_type,
			models_json, headers_json, timeout_sec, disabled, metadata_json,
			last_health_check, consecutive_fails, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			base_url = excluded.base_url,
			protocol = excluded.protocol,
			auth_type = excluded.auth_type,
			models_json = excluded.models_json,
			headers_json = excluded.headers_json,
			timeout_sec = excluded.timeout_sec,
			disabled = excluded.disabled,
			metadata_json = excluded.metadata_json,
			last_health_check = excluded.last_health_check,
			consecutive_fails = excluded.consecutive_fails,
			updated_at = excluded.updated_at
	`, p.ID, p.Name, p.BaseURL, string(p.Protocol), p.AuthType,
		string(modelsJSON), string(headersJSON), p.TimeoutSec, disabled, string(metadataJSON),
		lastHealthCheck, p.ConsecutiveFails, p.CreatedAt.Unix(), p.UpdatedAt.Unix())

	if err != nil {
		return fmt.Errorf("sqlite: 保存 provider %s 失败: %w", p.ID, err)
	}
	return nil
}

// Get 按 ID 查询 provider。
func (s *SQLiteProviderStore) Get(id string) (*provider.Provider, bool, error) {
	row := s.db.QueryRow(`
		SELECT id, name, base_url, protocol, auth_type,
		       models_json, headers_json, timeout_sec, disabled, metadata_json,
		       last_health_check, consecutive_fails, created_at, updated_at
		FROM providers WHERE id = ?
	`, id)

	p := &provider.Provider{}
	var modelsJSON, headersJSON, metadataJSON string
	var disabled int
	var lastHealthCheck *int64
	var createdAt, updatedAt int64
	var protocol string

	err := row.Scan(
		&p.ID, &p.Name, &p.BaseURL, &protocol, &p.AuthType,
		&modelsJSON, &headersJSON, &p.TimeoutSec, &disabled, &metadataJSON,
		&lastHealthCheck, &p.ConsecutiveFails, &createdAt, &updatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("sqlite: 查询 provider %s 失败: %w", id, err)
	}

	p.Protocol = provider.Protocol(protocol)
	p.Disabled = disabled != 0
	p.CreatedAt = time.Unix(createdAt, 0)
	p.UpdatedAt = time.Unix(updatedAt, 0)
	if lastHealthCheck != nil {
		p.LastHealthCheck = time.Unix(*lastHealthCheck, 0)
	}

	if err := json.Unmarshal([]byte(modelsJSON), &p.Models); err != nil {
		return nil, false, fmt.Errorf("sqlite: 反序列化 models 失败: %w", err)
	}
	if err := json.Unmarshal([]byte(headersJSON), &p.Headers); err != nil {
		return nil, false, fmt.Errorf("sqlite: 反序列化 headers 失败: %w", err)
	}
	if err := json.Unmarshal([]byte(metadataJSON), &p.Metadata); err != nil {
		return nil, false, fmt.Errorf("sqlite: 反序列化 metadata 失败: %w", err)
	}

	return p, true, nil
}

// Delete 删除 provider（硬删除，外键级联会删除关联的 credentials）。
func (s *SQLiteProviderStore) Delete(id string) error {
	result, err := s.db.Exec(`DELETE FROM providers WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("sqlite: 删除 provider %s 失败: %w", id, err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("sqlite: provider %s 不存在", id)
	}
	return nil
}

// List 列出所有 providers（不过滤禁用状态，由调用方决定）。
func (s *SQLiteProviderStore) List() ([]*provider.Provider, error) {
	rows, err := s.db.Query(`
		SELECT id, name, base_url, protocol, auth_type,
		       models_json, headers_json, timeout_sec, disabled, metadata_json,
		       last_health_check, consecutive_fails, created_at, updated_at
		FROM providers
		ORDER BY disabled ASC, updated_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("sqlite: 列出 providers 失败: %w", err)
	}
	defer rows.Close()

	var providers []*provider.Provider
	for rows.Next() {
		p := &provider.Provider{}
		var modelsJSON, headersJSON, metadataJSON string
		var disabled int
		var lastHealthCheck *int64
		var createdAt, updatedAt int64
		var protocol string

		if err := rows.Scan(
			&p.ID, &p.Name, &p.BaseURL, &protocol, &p.AuthType,
			&modelsJSON, &headersJSON, &p.TimeoutSec, &disabled, &metadataJSON,
			&lastHealthCheck, &p.ConsecutiveFails, &createdAt, &updatedAt,
		); err != nil {
			return nil, fmt.Errorf("sqlite: 扫描 provider 行失败: %w", err)
		}

		p.Protocol = provider.Protocol(protocol)
		p.Disabled = disabled != 0
		p.CreatedAt = time.Unix(createdAt, 0)
		p.UpdatedAt = time.Unix(updatedAt, 0)
		if lastHealthCheck != nil {
			p.LastHealthCheck = time.Unix(*lastHealthCheck, 0)
		}

		if err := json.Unmarshal([]byte(modelsJSON), &p.Models); err != nil {
			return nil, fmt.Errorf("sqlite: 反序列化 models 失败: %w", err)
		}
		if err := json.Unmarshal([]byte(headersJSON), &p.Headers); err != nil {
			return nil, fmt.Errorf("sqlite: 反序列化 headers 失败: %w", err)
		}
		if err := json.Unmarshal([]byte(metadataJSON), &p.Metadata); err != nil {
			return nil, fmt.Errorf("sqlite: 反序列化 metadata 失败: %w", err)
		}

		providers = append(providers, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: 遍历 providers 失败: %w", err)
	}

	return providers, nil
}

// FindByModel 查找支持指定模型的 providers。
func (s *SQLiteProviderStore) FindByModel(model string) ([]*provider.Provider, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}

	var matched []*provider.Provider
	for _, p := range all {
		if p.Disabled {
			continue
		}
		for _, m := range p.Models {
			if m.Name == model {
				matched = append(matched, p)
				break
			}
		}
	}
	return matched, nil
}

// ListWithContext 支持 context 的列表查询（暂未实现取消，预留签名）。
func (s *SQLiteProviderStore) ListWithContext(ctx context.Context) ([]*provider.Provider, error) {
	return s.List()
}
