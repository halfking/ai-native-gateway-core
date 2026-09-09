package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Model 模型目录条目（对齐 domains/streaming 和 admin 的模型概念）。
type Model struct {
	ID              string
	CanonicalName   string
	Family          string
	Modality        string
	ContextWindow   int
	SupportsStream  bool
	SupportsTools   bool
	InputCostPer1K  float64
	OutputCostPer1K float64
	Metadata        map[string]any
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// SQLiteModelStore 基于 SQLite 的模型目录存储。
type SQLiteModelStore struct {
	db *sql.DB
}

// NewSQLiteModelStore 创建 model store，db 一般来自 OpenSQLite。
func NewSQLiteModelStore(db *sql.DB) *SQLiteModelStore {
	return &SQLiteModelStore{db: db}
}

// Save 保存或更新模型（UPSERT 语义，以 canonical_name 唯一）。
func (s *SQLiteModelStore) Save(m *Model) error {
	if m == nil || m.ID == "" || m.CanonicalName == "" {
		return errors.New("sqlite: model 不能为空且必须有 ID 和 CanonicalName")
	}

	now := time.Now().Unix()
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Unix(now, 0)
	}
	m.UpdatedAt = time.Unix(now, 0)

	metadataJSON, err := json.Marshal(m.Metadata)
	if err != nil {
		return fmt.Errorf("sqlite: 序列化 metadata 失败: %w", err)
	}

	supportsStream := 0
	if m.SupportsStream {
		supportsStream = 1
	}
	supportsTools := 0
	if m.SupportsTools {
		supportsTools = 1
	}

	_, err = s.db.Exec(`
		INSERT INTO models (
			id, canonical_name, family, modality, context_window,
			supports_stream, supports_tools, input_cost_per1k, output_cost_per1k,
			metadata_json, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(canonical_name) DO UPDATE SET
			family = excluded.family,
			modality = excluded.modality,
			context_window = excluded.context_window,
			supports_stream = excluded.supports_stream,
			supports_tools = excluded.supports_tools,
			input_cost_per1k = excluded.input_cost_per1k,
			output_cost_per1k = excluded.output_cost_per1k,
			metadata_json = excluded.metadata_json,
			updated_at = excluded.updated_at
	`, m.ID, m.CanonicalName, m.Family, m.Modality, m.ContextWindow,
		supportsStream, supportsTools, m.InputCostPer1K, m.OutputCostPer1K,
		string(metadataJSON), m.CreatedAt.Unix(), m.UpdatedAt.Unix())

	if err != nil {
		return fmt.Errorf("sqlite: 保存 model %s 失败: %w", m.CanonicalName, err)
	}
	return nil
}

// Get 按 ID 查询模型。
func (s *SQLiteModelStore) Get(id string) (*Model, bool, error) {
	row := s.db.QueryRow(`
		SELECT id, canonical_name, family, modality, context_window,
		       supports_stream, supports_tools, input_cost_per1k, output_cost_per1k,
		       metadata_json, created_at, updated_at
		FROM models WHERE id = ?
	`, id)

	return s.scanModel(row)
}

// GetByCanonicalName 按 canonical_name 查询模型。
func (s *SQLiteModelStore) GetByCanonicalName(name string) (*Model, bool, error) {
	row := s.db.QueryRow(`
		SELECT id, canonical_name, family, modality, context_window,
		       supports_stream, supports_tools, input_cost_per1k, output_cost_per1k,
		       metadata_json, created_at, updated_at
		FROM models WHERE canonical_name = ?
	`, name)

	return s.scanModel(row)
}

// Delete 删除模型（硬删除，外键级联会删除关联的 bindings）。
func (s *SQLiteModelStore) Delete(id string) error {
	result, err := s.db.Exec(`DELETE FROM models WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("sqlite: 删除 model %s 失败: %w", id, err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("sqlite: model %s 不存在", id)
	}
	return nil
}

// List 列出所有模型（按 family 和更新时间排序）。
func (s *SQLiteModelStore) List() ([]*Model, error) {
	rows, err := s.db.Query(`
		SELECT id, canonical_name, family, modality, context_window,
		       supports_stream, supports_tools, input_cost_per1k, output_cost_per1k,
		       metadata_json, created_at, updated_at
		FROM models
		ORDER BY family ASC, canonical_name ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("sqlite: 列出 models 失败: %w", err)
	}
	defer rows.Close()

	var models []*Model
	for rows.Next() {
		m, err := s.scanModelRow(rows)
		if err != nil {
			return nil, err
		}
		models = append(models, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: 遍历 models 失败: %w", err)
	}

	return models, nil
}

// ListByFamily 列出指定 family 的所有模型。
func (s *SQLiteModelStore) ListByFamily(family string) ([]*Model, error) {
	rows, err := s.db.Query(`
		SELECT id, canonical_name, family, modality, context_window,
		       supports_stream, supports_tools, input_cost_per1k, output_cost_per1k,
		       metadata_json, created_at, updated_at
		FROM models
		WHERE family = ?
		ORDER BY canonical_name ASC
	`, family)
	if err != nil {
		return nil, fmt.Errorf("sqlite: 列出 family %s models 失败: %w", family, err)
	}
	defer rows.Close()

	var models []*Model
	for rows.Next() {
		m, err := s.scanModelRow(rows)
		if err != nil {
			return nil, err
		}
		models = append(models, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: 遍历 models 失败: %w", err)
	}

	return models, nil
}

// scanModel 从单行扫描模型。
func (s *SQLiteModelStore) scanModel(row *sql.Row) (*Model, bool, error) {
	m := &Model{}
	var metadataJSON string
	var createdAt, updatedAt int64
	var supportsStream, supportsTools int

	err := row.Scan(
		&m.ID, &m.CanonicalName, &m.Family, &m.Modality, &m.ContextWindow,
		&supportsStream, &supportsTools, &m.InputCostPer1K, &m.OutputCostPer1K,
		&metadataJSON, &createdAt, &updatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("sqlite: 查询 model 失败: %w", err)
	}

	m.SupportsStream = supportsStream != 0
	m.SupportsTools = supportsTools != 0
	m.CreatedAt = time.Unix(createdAt, 0)
	m.UpdatedAt = time.Unix(updatedAt, 0)

	if err := json.Unmarshal([]byte(metadataJSON), &m.Metadata); err != nil {
		return nil, false, fmt.Errorf("sqlite: 反序列化 metadata 失败: %w", err)
	}

	return m, true, nil
}

// scanModelRow 从多行游标扫描模型。
func (s *SQLiteModelStore) scanModelRow(rows *sql.Rows) (*Model, error) {
	m := &Model{}
	var metadataJSON string
	var createdAt, updatedAt int64
	var supportsStream, supportsTools int

	if err := rows.Scan(
		&m.ID, &m.CanonicalName, &m.Family, &m.Modality, &m.ContextWindow,
		&supportsStream, &supportsTools, &m.InputCostPer1K, &m.OutputCostPer1K,
		&metadataJSON, &createdAt, &updatedAt,
	); err != nil {
		return nil, fmt.Errorf("sqlite: 扫描 model 行失败: %w", err)
	}

	m.SupportsStream = supportsStream != 0
	m.SupportsTools = supportsTools != 0
	m.CreatedAt = time.Unix(createdAt, 0)
	m.UpdatedAt = time.Unix(updatedAt, 0)

	if err := json.Unmarshal([]byte(metadataJSON), &m.Metadata); err != nil {
		return nil, fmt.Errorf("sqlite: 反序列化 metadata 失败: %w", err)
	}

	return m, nil
}

// ListWithContext 支持 context 的列表查询（暂未实现取消，预留签名）。
func (s *SQLiteModelStore) ListWithContext(ctx context.Context) ([]*Model, error) {
	return s.List()
}
