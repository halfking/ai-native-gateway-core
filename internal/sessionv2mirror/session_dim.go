// Package sessionv2mirror — session_dim.go
//
// session_dim 维度表的 best-effort 维护（2026-08-15）。
//
// 背景：session_dim（350/358/407 定义）承载会话的 任务/项目/属主/客户端
// 维度，/api/admin/turns/sessions 的 项目→任务→会话 层级分组、属主/客户端
// 筛选都依赖它。旧的 350/358 触发器链路在 245/154 从未落地（见
// scripts/hotfix-20260815-turns-schema/README.md），导致表一直为空，
// 层级视图全部落入「未分类」。
//
// 本写入器随 V2 shadow write 的 PersistHook 在请求终态时 UPSERT：
//   - 首选从 request_context_attrs 按 request_id 取上下文（含 project_id，
//     即 X-Gw-Project-Id，主表 entry 不携带该字段）；
//   - 侧表无行时回退用 entry 自身字段（无 project）。
// 字段语义对齐 350/358：首值优先（owner/client/project/task 的第一个
// 非空值固化），last_active_at 取最大值，closed 会话被新请求唤醒为 active。
package sessionv2mirror

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

// DimWriter 是 session_dim 维护面。接口化便于 PersistHook 测试注入。
type DimWriter interface {
	UpsertSessionDim(ctx context.Context, entry *telemetry.RequestLogEntry) error
}

// SessionDimWriter 基于 pgxpool 的 session_dim 维护器。
type SessionDimWriter struct {
	pool *pgxpool.Pool
}

// NewSessionDimWriter 创建维度写入器；pool 为 nil 时返回的写入器所有调用
// 都是 no-op error，便于装配端不做 nil 分支。
func NewSessionDimWriter(pool *pgxpool.Pool) *SessionDimWriter {
	return &SessionDimWriter{pool: pool}
}

// upsert 冲突子句：首值优先 + last_active 取最大 + closed 唤醒为 active。
const sessionDimOnConflict = `
ON CONFLICT (gw_session_id) DO UPDATE SET
  last_active_at = GREATEST(session_dim.last_active_at, EXCLUDED.last_active_at),
  status = CASE WHEN session_dim.status = 'closed' THEN 'active' ELSE session_dim.status END,
  task_id = COALESCE(session_dim.task_id, EXCLUDED.task_id),
  project_id = COALESCE(session_dim.project_id, EXCLUDED.project_id),
  owner_user = COALESCE(session_dim.owner_user, EXCLUDED.owner_user),
  api_key_owner_user = COALESCE(session_dim.api_key_owner_user, EXCLUDED.api_key_owner_user),
  end_user_id = COALESCE(session_dim.end_user_id, EXCLUDED.end_user_id),
  client_id = COALESCE(session_dim.client_id, EXCLUDED.client_id),
  application_code = COALESCE(session_dim.application_code, EXCLUDED.application_code),
  application_id = COALESCE(session_dim.application_id, EXCLUDED.application_id),
  api_key_id = COALESCE(session_dim.api_key_id, EXCLUDED.api_key_id)`

// UpsertSessionDim 以请求终态 entry 为触发点 UPSERT session_dim。
func (w *SessionDimWriter) UpsertSessionDim(ctx context.Context, entry *telemetry.RequestLogEntry) error {
	if entry == nil || entry.GwSessionID == nil || *entry.GwSessionID == "" {
		return nil
	}
	if w == nil || w.pool == nil {
		return errors.New("sessionv2mirror: session dim writer has no pool")
	}

	// 首选：request_context_attrs 按 request_id 取全量上下文（含 project_id）。
	tag, err := w.pool.Exec(ctx, `
		INSERT INTO session_dim (
			gw_session_id, session_key, tenant_id, task_id, status,
			first_request_at, last_active_at, created_at,
			api_key_id, application_id, application_code,
			owner_user, api_key_owner_user, end_user_id, client_id, project_id
		)
		SELECT ca.gw_session_id, ca.gw_session_id, ca.tenant_id, ca.gw_task_id, 'active',
			ca.ts, ca.ts, NOW(),
			ca.api_key_id, ca.application_id, ca.application_code,
			ca.owner_user, ca.owner_user, ca.end_user_id, ca.application_code, ca.project_id
		FROM public.request_context_attrs ca
		WHERE ca.request_id = $1
			AND ca.gw_session_id IS NOT NULL AND ca.gw_session_id <> ''
		`+sessionDimOnConflict,
		entry.RequestID,
	)
	if err == nil && tag.RowsAffected() > 0 {
		return nil
	}
	if err != nil {
		// 侧表查询失败不阻断回退路径，但记录原因。
		slog.Warn("sessionv2mirror: session_dim upsert via context_attrs failed",
			"request_id", entry.RequestID, "error", err.Error())
	}

	// 回退：entry 自身字段（无 project_id；owner_user = api_key_owner_user，
	// client_id = COALESCE(application_code, api_key_prefix)，对齐 358 语义）。
	ts := entry.EventAt
	if ts == nil {
		now := timeNow()
		ts = &now
	}
	_, err = w.pool.Exec(ctx, `
		INSERT INTO session_dim (
			gw_session_id, session_key, tenant_id, task_id, status,
			first_request_at, last_active_at, created_at,
			api_key_id, application_id, application_code,
			owner_user, api_key_owner_user, end_user_id, client_id
		) VALUES (
			$1, $1, $2, $3, 'active',
			$4, $4, NOW(),
			$5, $6, $7,
			$8, $8, $9, $10
		)`+sessionDimOnConflict,
		*entry.GwSessionID, entry.TenantID, entry.GwTaskID, ts,
		entry.APIKeyID, entry.ApplicationID, entry.ApplicationCode,
		entry.APIKeyOwnerUser, entry.EndUserID, coalesceStr(entry.ApplicationCode, entry.APIKeyPrefix),
	)
	if err != nil {
		return err
	}
	return nil
}

func timeNow() time.Time { return time.Now() }

func coalesceStr(a, b *string) *string {
	if a != nil && *a != "" {
		return a
	}
	return b
}
