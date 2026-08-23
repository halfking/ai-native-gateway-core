package sessionmeta

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

var ErrStoreNotConfigured = errors.New("sessionmeta: database not configured")

type dbPool interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// MetadataStore persists Extract results for provisional/final stages.
type MetadataStore struct {
	pool dbPool
}

func NewMetadataStore(pool dbPool) *MetadataStore {
	return &MetadataStore{pool: pool}
}

// UpsertProvisional writes status=provisional when input_hash changes. Same hash is a no-op.
func (s *MetadataStore) UpsertProvisional(ctx context.Context, tenantID, sessionID, taskID string, result Result) error {
	if s == nil || s.pool == nil {
		return ErrStoreNotConfigured
	}
	tenantID = strings.TrimSpace(tenantID)
	sessionID = strings.TrimSpace(sessionID)
	if tenantID == "" || sessionID == "" {
		return fmt.Errorf("sessionmeta: tenant and session are required")
	}
	if strings.TrimSpace(result.InputHash) == "" {
		return fmt.Errorf("sessionmeta: input_hash is required")
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("sessionmeta: marshal result: %w", err)
	}
	schemaVersion := strings.TrimSpace(result.SchemaVersion)
	if schemaVersion == "" {
		schemaVersion = SchemaVersion
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO public.session_analysis_metadata (
			tenant_id, scoped_session_id, schema_version, status,
			input_hash, payload, source_task_id, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6::jsonb, NULLIF($7, ''), now())
		ON CONFLICT (tenant_id, scoped_session_id, status) DO UPDATE SET
			schema_version = EXCLUDED.schema_version,
			input_hash = EXCLUDED.input_hash,
			payload = EXCLUDED.payload,
			source_task_id = EXCLUDED.source_task_id,
			updated_at = now()
		WHERE public.session_analysis_metadata.input_hash IS DISTINCT FROM EXCLUDED.input_hash
	`, tenantID, sessionID, schemaVersion, StatusProvisional, result.InputHash, payload, strings.TrimSpace(taskID))
	if err != nil {
		return fmt.Errorf("sessionmeta: upsert provisional: %w", err)
	}
	return nil
}
