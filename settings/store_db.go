package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// StoreDB persists settings to settings_kv / tenant_settings_kv.
// It is the write side of the priority chain (DB > env > default).
type StoreDB struct {
	pool *pgxpool.Pool
}

// NewStoreDB wires the backend to a PG connection pool.
func NewStoreDB(pool *pgxpool.Pool) *StoreDB { return &StoreDB{pool: pool} }

// Pool returns the underlying pool. Useful for tests and clean-up tasks.
func (s *StoreDB) Pool() *pgxpool.Pool { return s.pool }

// Get returns the DB value or (nil, nil) if no row.
func (s *StoreDB) Get(scope Scope, key string) (jsonRawMessage, error) {
	if scope == ScopeTenant {
		return nil, fmt.Errorf("use GetTenant for tenant scope")
	}
	var raw []byte
	err := s.pool.QueryRow(context.Background(), `
		SELECT value::text FROM settings_kv WHERE key = $1
	`, key).Scan(&raw)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return raw, nil
}

// Set writes the value, stashing the old value in prev_value for rollback.
// Returns the previous value (or nil if first write).
func (s *StoreDB) Set(scope Scope, key string, value any) (jsonRawMessage, error) {
	if scope == ScopeTenant {
		return nil, fmt.Errorf("use SetTenant for tenant scope")
	}
	newVal, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	var oldVal []byte
	// Try UPDATE first; if no row matched, INSERT.
	err = s.pool.QueryRow(context.Background(), `
		UPDATE settings_kv
		   SET value = $2::jsonb, updated_at = now(),
		       prev_value = value, prev_updated_at = updated_at
		 WHERE key = $1
		RETURNING prev_value
	`, key, newVal).Scan(&oldVal)
	if err == pgx.ErrNoRows {
		// No existing row → INSERT.
		_, err = s.pool.Exec(context.Background(), `
			INSERT INTO settings_kv (key, value, scope, category, updated_at)
			VALUES ($1, $2::jsonb, 'platform', 'general', now())
		`, key, newVal)
		if err != nil {
			return nil, fmt.Errorf("insert: %w", err)
		}
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("update: %w", err)
	}
	return oldVal, nil
}

// GetTenant is the per-tenant variant.
func (s *StoreDB) GetTenant(tenantID, key string) (jsonRawMessage, error) {
	var raw []byte
	err := s.pool.QueryRow(context.Background(), `
		SELECT value::text FROM tenant_settings_kv
		 WHERE tenant_id = $1 AND key = $2
	`, tenantID, key).Scan(&raw)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return raw, nil
}

// SetTenant writes a tenant-scoped value.
func (s *StoreDB) SetTenant(tenantID, key string, value any) (jsonRawMessage, error) {
	newVal, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	var oldVal []byte
	err = s.pool.QueryRow(context.Background(), `
		INSERT INTO tenant_settings_kv (tenant_id, key, value, scope, category, updated_at)
		VALUES ($1, $2, $3::jsonb, 'tenant', 'general', now())
		ON CONFLICT (tenant_id, key) DO UPDATE
		  SET value = EXCLUDED.value, updated_at = now(),
		      prev_value = tenant_settings_kv.value,
		      prev_updated_at = tenant_settings_kv.updated_at
		RETURNING prev_value
	`, tenantID, key, newVal).Scan(&oldVal)
	if err != nil {
		if err == pgx.ErrNoRows {
			// INSERT-only path; ON CONFLICT did not fire because prev_value
			// is NULL. This is a fresh insert → oldVal stays nil.
			return nil, nil
		}
		return nil, fmt.Errorf("upsert tenant setting: %w", err)
	}
	return oldVal, nil
}

// List returns all platform-scoped keys and their values.
func (s *StoreDB) List(scope Scope) (map[string]jsonRawMessage, error) {
	if scope == ScopeTenant {
		return nil, fmt.Errorf("use ListTenant for tenant scope")
	}
	rows, err := s.pool.Query(context.Background(), `
		SELECT key, value::text FROM settings_kv ORDER BY key
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]jsonRawMessage)
	for rows.Next() {
		var k string
		var v []byte
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, nil
}

// ListTenant returns all settings for a tenant.
func (s *StoreDB) ListTenant(tenantID string) (map[string]jsonRawMessage, error) {
	rows, err := s.pool.Query(context.Background(), `
		SELECT key, value::text FROM tenant_settings_kv
		 WHERE tenant_id = $1 ORDER BY key
	`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]jsonRawMessage)
	for rows.Next() {
		var k string
		var v []byte
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, nil
}

// Rollback reverts key to its prev_value. Returns the value that was
// written as the new "current" value.
func (s *StoreDB) Rollback(scope Scope, key, tenantID string) (jsonRawMessage, error) {
	var newVal []byte
	var err error
	if scope == ScopeTenant {
		err = s.pool.QueryRow(context.Background(), `
			UPDATE tenant_settings_kv
			   SET value = prev_value, prev_value = value,
			       updated_at = now(), prev_updated_at = updated_at
			 WHERE tenant_id = $1 AND key = $2 AND prev_value IS NOT NULL
			RETURNING value::text
		`, tenantID, key).Scan(&newVal)
	} else {
		err = s.pool.QueryRow(context.Background(), `
			UPDATE settings_kv
			   SET value = prev_value, prev_value = value,
			       updated_at = now(), prev_updated_at = updated_at
			 WHERE key = $1 AND prev_value IS NOT NULL
			RETURNING value::text
		`, key).Scan(&newVal)
	}
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("no previous value to rollback to")
		}
		return nil, err
	}
	return newVal, nil
}

// Delete removes a setting (platform or tenant).
func (s *StoreDB) Delete(scope Scope, key, tenantID string) error {
	var err error
	if scope == ScopeTenant {
		_, err = s.pool.Exec(context.Background(),
			`DELETE FROM tenant_settings_kv WHERE tenant_id = $1 AND key = $2`,
			tenantID, key)
	} else {
		_, err = s.pool.Exec(context.Background(),
			`DELETE FROM settings_kv WHERE key = $1`, key)
	}
	return err
}

// ensure PoolExec is satisfied for audit.go to compile.
var _ = time.Now
var _ = slog.Default
