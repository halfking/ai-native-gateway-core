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

// Tag 是一行网关 session_tags（键值标签）。
//
// 它与 Feature **同构**但**来源不同**：session_tags 是网关 tagger
// （domains/analysis/tagger.go）落库的行；Feature 是 extractor 产出的
// 会话级"特征"。memora 契约 v1.1 把这两者收进**两个不同的字段**
// （tags / features），**不互相映射** ⇒ 本类型存在的意义就是让
// 「标签从哪来」在类型上就分得开。
type Tag struct {
	Key        string
	Value      string
	Source     string
	Confidence float64
}

// SessionTags 返回该会话的 session_tags 行（按 tag_key、tag_value 排序）。
//
// ★ 它是 memora 契约里 **tags** 字段的来源（契约 v1.1 §3）：
//
//	tags = 网关 session_tags 的行（key=tag_key，value=tag_value）
//
// 而 **features** 来自 Result.Features —— 两者**不互相映射**。
//
// 读取失败**不**阻断调用方（调用方把它当作「没有标签」）：标签是附加信息，
// 缺了不影响分析产出本身落地。
func (s *MetadataStore) SessionTags(ctx context.Context, tenantID, sessionID string) ([]Tag, error) {
	if s == nil || s.pool == nil {
		return nil, ErrStoreNotConfigured
	}
	tenantID = strings.TrimSpace(tenantID)
	sessionID = strings.TrimSpace(sessionID)
	if tenantID == "" || sessionID == "" {
		return nil, nil
	}
	// ★ 用一条 json_agg 查出全部标签，而不是给 dbPool 接口加 Query 方法：
	//   那个接口被测试替身实现了，加方法会让所有替身一起改（本次改动不该
	//   扩大那个面）。一条聚合查询同样是一次往返。
	var raw []byte
	if err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(json_agg(json_build_object(
		       'key', tag_key,
		       'value', tag_value,
		       'source', tag_source,
		       'confidence', COALESCE(confidence, 0)
		     ) ORDER BY tag_key, tag_value), '[]'::jsonb)
		  FROM public.session_tags
		 WHERE tenant_id = $1 AND gw_session_id = $2
	`, tenantID, sessionID).Scan(&raw); err != nil {
		return nil, fmt.Errorf("sessionmeta: read session tags: %w", err)
	}
	var out []Tag
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("sessionmeta: decode session tags: %w", err)
	}
	return out, nil
}

// upsert writes the analysis metadata row for one (tenant, session, status).
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
