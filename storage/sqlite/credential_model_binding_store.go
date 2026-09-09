package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// CredentialModelBinding 凭据-模型绑定关系。
type CredentialModelBinding struct {
	CredentialID string
	ModelID      string
	Available    bool
	CreatedAt    time.Time
}

// SQLiteBindingStore 基于 SQLite 的凭据-模型绑定存储。
type SQLiteBindingStore struct {
	db *sql.DB
}

// NewSQLiteBindingStore 创建 binding store，db 一般来自 OpenSQLite。
func NewSQLiteBindingStore(db *sql.DB) *SQLiteBindingStore {
	return &SQLiteBindingStore{db: db}
}

// Save 保存或更新绑定（UPSERT 语义）。
func (s *SQLiteBindingStore) Save(b *CredentialModelBinding) error {
	if b == nil || b.CredentialID == "" || b.ModelID == "" {
		return fmt.Errorf("sqlite: binding 不能为空且必须有 CredentialID 和 ModelID")
	}

	now := time.Now().Unix()
	if b.CreatedAt.IsZero() {
		b.CreatedAt = time.Unix(now, 0)
	}

	available := 0
	if b.Available {
		available = 1
	}

	_, err := s.db.Exec(`
		INSERT INTO credential_model_bindings (credential_id, model_id, available, created_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(credential_id, model_id) DO UPDATE SET
			available = excluded.available
	`, b.CredentialID, b.ModelID, available, b.CreatedAt.Unix())

	if err != nil {
		return fmt.Errorf("sqlite: 保存 binding %s-%s 失败: %w", b.CredentialID, b.ModelID, err)
	}
	return nil
}

// Get 查询单个绑定。
func (s *SQLiteBindingStore) Get(credentialID, modelID string) (*CredentialModelBinding, bool, error) {
	row := s.db.QueryRow(`
		SELECT credential_id, model_id, available, created_at
		FROM credential_model_bindings
		WHERE credential_id = ? AND model_id = ?
	`, credentialID, modelID)

	b := &CredentialModelBinding{}
	var available int
	var createdAt int64

	err := row.Scan(&b.CredentialID, &b.ModelID, &available, &createdAt)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("sqlite: 查询 binding %s-%s 失败: %w", credentialID, modelID, err)
	}

	b.Available = available != 0
	b.CreatedAt = time.Unix(createdAt, 0)
	return b, true, nil
}

// Delete 删除绑定。
func (s *SQLiteBindingStore) Delete(credentialID, modelID string) error {
	result, err := s.db.Exec(`
		DELETE FROM credential_model_bindings
		WHERE credential_id = ? AND model_id = ?
	`, credentialID, modelID)
	if err != nil {
		return fmt.Errorf("sqlite: 删除 binding %s-%s 失败: %w", credentialID, modelID, err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("sqlite: binding %s-%s 不存在", credentialID, modelID)
	}
	return nil
}

// ListByCredential 列出指定凭据的所有模型绑定。
func (s *SQLiteBindingStore) ListByCredential(credentialID string) ([]*CredentialModelBinding, error) {
	rows, err := s.db.Query(`
		SELECT credential_id, model_id, available, created_at
		FROM credential_model_bindings
		WHERE credential_id = ?
		ORDER BY model_id ASC
	`, credentialID)
	if err != nil {
		return nil, fmt.Errorf("sqlite: 列出 credential %s bindings 失败: %w", credentialID, err)
	}
	defer rows.Close()

	var bindings []*CredentialModelBinding
	for rows.Next() {
		b := &CredentialModelBinding{}
		var available int
		var createdAt int64

		if err := rows.Scan(&b.CredentialID, &b.ModelID, &available, &createdAt); err != nil {
			return nil, fmt.Errorf("sqlite: 扫描 binding 行失败: %w", err)
		}

		b.Available = available != 0
		b.CreatedAt = time.Unix(createdAt, 0)
		bindings = append(bindings, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: 遍历 bindings 失败: %w", err)
	}

	return bindings, nil
}

// ListByModel 列出指定模型的所有凭据绑定。
func (s *SQLiteBindingStore) ListByModel(modelID string) ([]*CredentialModelBinding, error) {
	rows, err := s.db.Query(`
		SELECT credential_id, model_id, available, created_at
		FROM credential_model_bindings
		WHERE model_id = ?
		ORDER BY credential_id ASC
	`, modelID)
	if err != nil {
		return nil, fmt.Errorf("sqlite: 列出 model %s bindings 失败: %w", modelID, err)
	}
	defer rows.Close()

	var bindings []*CredentialModelBinding
	for rows.Next() {
		b := &CredentialModelBinding{}
		var available int
		var createdAt int64

		if err := rows.Scan(&b.CredentialID, &b.ModelID, &available, &createdAt); err != nil {
			return nil, fmt.Errorf("sqlite: 扫描 binding 行失败: %w", err)
		}

		b.Available = available != 0
		b.CreatedAt = time.Unix(createdAt, 0)
		bindings = append(bindings, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: 遍历 bindings 失败: %w", err)
	}

	return bindings, nil
}

// ListAvailableModels 列出指定凭据的所有可用模型（available=1）。
func (s *SQLiteBindingStore) ListAvailableModels(credentialID string) ([]string, error) {
	rows, err := s.db.Query(`
		SELECT model_id
		FROM credential_model_bindings
		WHERE credential_id = ? AND available = 1
		ORDER BY model_id ASC
	`, credentialID)
	if err != nil {
		return nil, fmt.Errorf("sqlite: 列出 credential %s available models 失败: %w", credentialID, err)
	}
	defer rows.Close()

	var modelIDs []string
	for rows.Next() {
		var modelID string
		if err := rows.Scan(&modelID); err != nil {
			return nil, fmt.Errorf("sqlite: 扫描 model_id 失败: %w", err)
		}
		modelIDs = append(modelIDs, modelID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: 遍历 model_ids 失败: %w", err)
	}

	return modelIDs, nil
}

// DeleteAllByCredential 删除指定凭据的所有绑定（用于凭据删除或批量重置）。
func (s *SQLiteBindingStore) DeleteAllByCredential(credentialID string) error {
	_, err := s.db.Exec(`
		DELETE FROM credential_model_bindings
		WHERE credential_id = ?
	`, credentialID)
	if err != nil {
		return fmt.Errorf("sqlite: 删除 credential %s 所有 bindings 失败: %w", credentialID, err)
	}
	return nil
}

// DeleteAllByModel 删除指定模型的所有绑定（用于模型删除）。
func (s *SQLiteBindingStore) DeleteAllByModel(modelID string) error {
	_, err := s.db.Exec(`
		DELETE FROM credential_model_bindings
		WHERE model_id = ?
	`, modelID)
	if err != nil {
		return fmt.Errorf("sqlite: 删除 model %s 所有 bindings 失败: %w", modelID, err)
	}
	return nil
}

// ListByCredentialWithContext 支持 context 的列表查询（暂未实现取消，预留签名）。
func (s *SQLiteBindingStore) ListByCredentialWithContext(ctx context.Context, credentialID string) ([]*CredentialModelBinding, error) {
	return s.ListByCredential(credentialID)
}
