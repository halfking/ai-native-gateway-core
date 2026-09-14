// Package sessionv2mirror — outbox.go
//
// 存储优化方案 v2 §4-S4 前置（spec §12 GAP 2 闭环，2026-09-15）：mirror
// shadow write 的失败行（写超时/DB 错/8 槽满）此前只进 in-process backlog
// （backlog.go，cap 10000、重启即丢、无重放器），本机实测 24h 终态 v1 行缺
// turns 3.71%（258/6956），credits 随行丢失。本文件把失败路径持久化到
// session_mirror_outbox（迁移 712）：登记行 payload = 完整 RequestLogEntry
// JSON（struct 全量 json tag），重放器（replay.go）unmarshal 后走
// entryToProcessedRequest 同一桥接 → v2.Write（request_id 幂等）。
//
// 设计取舍（plan §4-S4 方案对比定案）：仅失败路径 outbox 化——正常路径与
// 8 槽/2000ms 前台预算零改动；全量 outbox 化被否（100% 写两遍与存储优化
// 目标相悖，且 session_aggregate_outbox 的 payload 是 SessionUpdate 聚合
// 快照，重放不写 turns）。payload 自带全量事实，S4 停写 request_logs 后
// 重放不依赖 v1 反查。
//
// 前台预算契约：登记是单行轻量 INSERT，走独立 500ms 短预算、不占
// shadowWriteSema 的 8 槽（那 8 槽保护的是完整 V2 写的连接占用）；登记
// 失败（DB 不可用等）返回 false，调用方降级回 in-process appendBacklog，
// 行为与 GAP-2 修复前一致。
package sessionv2mirror

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// mirrorOutboxEnqueueBudgetMs bounds the failure-registration INSERT. It
// runs on the telemetry worker (semaphore-full branch) or a shadow-write
// goroutine (write-failed branch); 500ms keeps either path well inside the
// worker contract while absorbing transient contention. Not tunable for
// now — the row is a single INSERT into a small heap table.
const mirrorOutboxEnqueueBudgetMs = 500

// mirrorOutbox holds the PG pool used for failure registration. Package-
// level (like the in-process backlog) because PersistHook's signature is
// frozen by existing call sites/tests; production wires it once from
// main.go via InitMirrorOutbox. nil = registration unavailable, callers
// fall back to the in-process backlog.
var mirrorOutbox atomic.Pointer[pgxpool.Pool]

// InitMirrorOutbox wires the failure-registration pool. Safe to call with
// nil (disables registration). Idempotent — last writer wins.
func InitMirrorOutbox(pool *pgxpool.Pool) {
	mirrorOutbox.Store(pool)
}

// mirrorOutboxEnabled reads the registration kill switch. Default on:
// losing the persistence surface silently would recreate GAP 2. When off,
// EnqueueMirrorFailure reports failure so callers degrade to the in-process
// backlog (the pre-GAP-2 behaviour).
func mirrorOutboxEnabled() bool {
	return settings.GetPlatformBool("sessions_v2.mirror_outbox", true)
}

// EnqueueMirrorFailure persists one failed shadow-write entry to
// session_mirror_outbox as a replayable payload (the full entry JSON).
//
// Returns true iff the row is durably registered. Duplicate request_ids
// converge via ON CONFLICT DO UPDATE (latest payload/fail_reason win) —
// the reaper state machine, not re-registration, owns retry bookkeeping.
func EnqueueMirrorFailure(entry *telemetry.RequestLogEntry, sessionID, reason string) bool {
	pool := mirrorOutbox.Load()
	if pool == nil || entry == nil || sessionID == "" {
		return false
	}
	if !mirrorOutboxEnabled() {
		return false
	}

	payload, err := json.Marshal(entry)
	if err != nil {
		// RequestLogEntry marshals via struct tags; failure here would be a
		// programming error (unsupported type), not a data condition.
		slog.Warn("sessionv2mirror: outbox payload marshal failed",
			"request_id", entry.RequestID, "error", err)
		return false
	}

	tenantID := entry.TenantID
	if tenantID == "" {
		tenantID = "default"
	}

	ctx, cancel := context.WithTimeout(context.Background(), mirrorOutboxEnqueueBudgetMs*time.Millisecond)
	defer cancel()
	// FORCE RLS (712) means a bare INSERT is rejected by WITH CHECK for any
	// non-'default' tenant — the GUC lift must wrap the INSERT (replay.go
	// execBypass contract; 630 reaper precedent).
	tx, err := pool.Begin(ctx)
	if err != nil {
		slog.Warn("sessionv2mirror: outbox registration begin failed, degrading to in-process backlog",
			"request_id", entry.RequestID, "error", err)
		return false
	}
	defer tx.Rollback(ctx)
	if err := setBypassGUCs(ctx, tx); err != nil {
		slog.Warn("sessionv2mirror: outbox registration GUC lift failed, degrading to in-process backlog",
			"request_id", entry.RequestID, "error", err)
		return false
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO public.session_mirror_outbox
		    (tenant_id, request_id, session_id, source, fail_reason, payload)
		VALUES ($1, $2, $3, 'hook', $4, $5::jsonb)
		ON CONFLICT (request_id) DO UPDATE
		SET fail_reason = EXCLUDED.fail_reason,
		    payload     = EXCLUDED.payload,
		    updated_at  = NOW()
	`, tenantID, entry.RequestID, sessionID, reason, string(payload)); err != nil {
		slog.Warn("sessionv2mirror: outbox registration failed, degrading to in-process backlog",
			"request_id", entry.RequestID, "error", err)
		return false
	}
	if err := tx.Commit(ctx); err != nil {
		slog.Warn("sessionv2mirror: outbox registration commit failed, degrading to in-process backlog",
			"request_id", entry.RequestID, "error", err)
		return false
	}
	return true
}
