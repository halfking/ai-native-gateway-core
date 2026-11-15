package casdoor

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DB is an interface for database operations (used for testing).
type DB interface {
	QueryRow(ctx context.Context, sql string, args ...interface{}) pgx.Row
	Query(ctx context.Context, sql string, args ...interface{}) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...interface{}) (pgconn.CommandTag, error)
}

// IDMapper handles bidirectional mapping between external provider IDs
// (Casdoor UUIDs, LDAP DNs, etc.) and internal sequential user IDs.
//
// This enables:
// - Multiple authentication providers for the same user
// - Stable internal IDs across provider migrations
// - Consistent audit logs and foreign key references
type IDMapper struct {
	db DB
}

// NewIDMapper creates an ID mapper instance.
func NewIDMapper(db DB) *IDMapper {
	return &IDMapper{db: db}
}

// NewIDMapperFromPool creates an ID mapper from a connection pool.
func NewIDMapperFromPool(pool *pgxpool.Pool) *IDMapper {
	return &IDMapper{db: pool}
}

// ★ 2026-11-16 12h 修订审计轮 P2 根修：db 未装配时的**哨兵错误**。
//
//	背景：db 是接口字段，零值 IDMapper{} 的 db == nil ⇒ 调用 m.db.QueryRow
//	是在**nil 接口上调用方法** ⇒ nil 指针 panic。
//	实测后果：pkg/authplugin/casdoor 的 cached_id_mapper_test.go 用
//	`mapper := &IDMapper{}` 构造 mock ⇒ 测试进程 SIGSEGV ⇒ **整个包的测试
//	二进制中止** ⇒ 同包其它用例一条都跑不到（一个 panic 掩盖全包结果）。
//	现在：所有公开方法先经 requireDB() ⇒ 返回哨兵错误而不是 panic。
var errIDMapperNoDB = errors.New("casdoor: IDMapper has no database handle configured")

// requireDB 返回装配好的 db；未装配时返回哨兵错误。
func (m *IDMapper) requireDB() (DB, error) {
	if m == nil || m.db == nil {
		return nil, errIDMapperNoDB
	}
	return m.db, nil
}

// MapOrCreate finds or creates a mapping for an external identity.
// Returns the internal user ID.
//
// If the mapping already exists, updates cached fields (display_name, email)
// and last_sync_at timestamp.
//
// If the mapping doesn't exist, creates a new entry and assigns a new
// internal ID.
func (m *IDMapper) MapOrCreate(ctx context.Context, provider, externalID, tenantID, username, displayName, email string) (int, error) {
	db, err := m.requireDB()
	if err != nil {
		return 0, err
	}
	if provider == "" || externalID == "" || tenantID == "" {
		return 0, fmt.Errorf("provider, externalID, and tenantID are required")
	}

	// Try to find existing mapping
	var internalID int
	err = db.QueryRow(ctx, `
		SELECT internal_id FROM auth_id_mapping
		WHERE provider = $1 AND external_id = $2 AND tenant_id = $3
	`, provider, externalID, tenantID).Scan(&internalID)

	if err == nil {
		// Mapping exists - update cached fields and sync timestamp
		_, _ = db.Exec(ctx, `
			UPDATE auth_id_mapping 
			SET last_sync_at = now(), 
			    display_name = $4, 
			    email = $5,
			    username = $6
			WHERE internal_id = $1 
			  AND provider = $2 
			  AND external_id = $3
		`, internalID, provider, externalID, displayName, email, username)
		return internalID, nil
	}

	if err != pgx.ErrNoRows {
		return 0, fmt.Errorf("query mapping: %w", err)
	}

	// Mapping doesn't exist - create new entry
	err = db.QueryRow(ctx, `
		INSERT INTO auth_id_mapping (
			external_id, provider, tenant_id, username, 
			display_name, email, last_sync_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, now())
		RETURNING internal_id
	`, externalID, provider, tenantID, username, displayName, email).Scan(&internalID)

	if err != nil {
		return 0, fmt.Errorf("create mapping: %w", err)
	}

	return internalID, nil
}

// GetByInternalID looks up external identity by internal ID.
// Returns provider, externalID, and tenantID.
func (m *IDMapper) GetByInternalID(ctx context.Context, internalID int) (provider, externalID, tenantID string, err error) {
	db, dbErr := m.requireDB()
	if dbErr != nil {
		return "", "", "", dbErr
	}
	err = db.QueryRow(ctx, `
		SELECT provider, external_id, tenant_id 
		FROM auth_id_mapping 
		WHERE internal_id = $1
	`, internalID).Scan(&provider, &externalID, &tenantID)

	if err == pgx.ErrNoRows {
		err = fmt.Errorf("mapping not found for internal ID %d", internalID)
	}
	return
}

// GetByExternalID looks up internal ID by external identity.
func (m *IDMapper) GetByExternalID(ctx context.Context, provider, externalID, tenantID string) (int, error) {
	db, dbErr := m.requireDB()
	if dbErr != nil {
		return 0, dbErr
	}
	var internalID int
	err := db.QueryRow(ctx, `
		SELECT internal_id FROM auth_id_mapping
		WHERE provider = $1 AND external_id = $2 AND tenant_id = $3
	`, provider, externalID, tenantID).Scan(&internalID)

	if err == pgx.ErrNoRows {
		return 0, fmt.Errorf("mapping not found for %s:%s in tenant %s", provider, externalID, tenantID)
	}
	return internalID, err
}

// GetByUsername looks up mapping by username within a provider and tenant.
// Useful when external_id is not known but username is.
func (m *IDMapper) GetByUsername(ctx context.Context, provider, username, tenantID string) (internalID int, externalID string, err error) {
	db, dbErr := m.requireDB()
	if dbErr != nil {
		return 0, "", dbErr
	}
	err = db.QueryRow(ctx, `
		SELECT internal_id, external_id FROM auth_id_mapping
		WHERE provider = $1 AND username = $2 AND tenant_id = $3
	`, provider, username, tenantID).Scan(&internalID, &externalID)

	if err == pgx.ErrNoRows {
		err = fmt.Errorf("mapping not found for username %s@%s in tenant %s", username, provider, tenantID)
	}
	return
}

// Delete removes a mapping (used when user is deleted from external provider).
func (m *IDMapper) Delete(ctx context.Context, internalID int) error {
	db, dbErr := m.requireDB()
	if dbErr != nil {
		return dbErr
	}
	result, err := db.Exec(ctx, `
		DELETE FROM auth_id_mapping WHERE internal_id = $1
	`, internalID)

	if err != nil {
		return fmt.Errorf("delete mapping: %w", err)
	}

	if result.RowsAffected() == 0 {
		return fmt.Errorf("mapping not found for internal ID %d", internalID)
	}

	return nil
}

// DeleteByExternal removes a mapping by external identity.
func (m *IDMapper) DeleteByExternal(ctx context.Context, provider, externalID, tenantID string) error {
	db, dbErr := m.requireDB()
	if dbErr != nil {
		return dbErr
	}
	result, err := db.Exec(ctx, `
		DELETE FROM auth_id_mapping 
		WHERE provider = $1 AND external_id = $2 AND tenant_id = $3
	`, provider, externalID, tenantID)

	if err != nil {
		return fmt.Errorf("delete mapping: %w", err)
	}

	if result.RowsAffected() == 0 {
		return fmt.Errorf("mapping not found for %s:%s in tenant %s", provider, externalID, tenantID)
	}

	return nil
}

// ListByProvider returns all mappings for a specific provider and tenant.
// Useful for migration or auditing.
func (m *IDMapper) ListByProvider(ctx context.Context, provider, tenantID string, limit int) ([]Mapping, error) {
	db, dbErr := m.requireDB()
	if dbErr != nil {
		return nil, dbErr
	}
	if limit <= 0 {
		limit = 100
	}

	rows, err := db.Query(ctx, `
		SELECT internal_id, external_id, username, display_name, email, created_at, last_sync_at
		FROM auth_id_mapping
		WHERE provider = $1 AND tenant_id = $2
		ORDER BY internal_id
		LIMIT $3
	`, provider, tenantID, limit)

	if err != nil {
		return nil, fmt.Errorf("query mappings: %w", err)
	}
	defer rows.Close()

	var mappings []Mapping
	for rows.Next() {
		var m Mapping
		var lastSyncAt *time.Time
		err := rows.Scan(&m.InternalID, &m.ExternalID, &m.Username, &m.DisplayName, &m.Email, &m.CreatedAt, &lastSyncAt)
		if err != nil {
			return nil, fmt.Errorf("scan mapping: %w", err)
		}
		if lastSyncAt != nil {
			m.LastSyncAt = *lastSyncAt
		}
		m.Provider = provider
		m.TenantID = tenantID
		mappings = append(mappings, m)
	}

	return mappings, rows.Err()
}

// Mapping represents an ID mapping entry.
type Mapping struct {
	InternalID  int
	ExternalID  string
	Provider    string
	TenantID    string
	Username    string
	DisplayName string
	Email       string
	CreatedAt   time.Time
	LastSyncAt  time.Time
}
