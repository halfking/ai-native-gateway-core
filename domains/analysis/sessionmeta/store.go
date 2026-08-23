package sessionmeta

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var ErrStoreNotConfigured = errors.New("sessionmeta: database not configured")

type dbPool interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
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
	return s.upsert(ctx, tenantID, sessionID, taskID, StatusProvisional, result, time.Time{})
}

// UpsertFinal writes status=final after the session close worker finishes.
// When provisional was refreshed during the worker (different input_hash and
// updated_at after workerStartedAt), the write is skipped so stale results
// cannot replace newer provisional watermarks.
func (s *MetadataStore) UpsertFinal(ctx context.Context, tenantID, sessionID, taskID string, result Result, workerStartedAt time.Time) error {
	if !workerStartedAt.IsZero() {
		var provHash string
		var provUpdated time.Time
		err := s.pool.QueryRow(ctx, `
			SELECT input_hash, updated_at
			  FROM public.session_analysis_metadata
			 WHERE tenant_id = $1 AND scoped_session_id = $2 AND status = $3
		`, tenantID, sessionID, StatusProvisional).Scan(&provHash, &provUpdated)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("sessionmeta: read provisional watermark: %w", err)
		}
		if err == nil && provHash != "" && provHash != result.InputHash && provUpdated.After(workerStartedAt) {
			return nil
		}
	}
	result.Status = StatusFinal
	return s.upsert(ctx, tenantID, sessionID, taskID, StatusFinal, result, workerStartedAt)
}

func (s *MetadataStore) upsert(ctx context.Context, tenantID, sessionID, taskID, status string, result Result, workerStartedAt time.Time) error {
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
	// Pass JSON as text: pgx encodes []byte as bytea, and PostgreSQL cannot
	// cast bytea → jsonb (`cannot cast type bytea to jsonb`).
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
	`, tenantID, sessionID, schemaVersion, status, result.InputHash, string(payload), strings.TrimSpace(taskID))
	if err != nil {
		return fmt.Errorf("sessionmeta: upsert %s: %w", status, err)
	}
	_ = workerStartedAt
	return nil
}
