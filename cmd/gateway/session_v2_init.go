// Package main — session_v2_init.go
//
// SessionWriterV2 的 boot 接线（Phase 4, 2026-07-21）
//
// 设计目标：
//  1. 无侵入接续 — 创建 V2 子 writer（TurnWriter / BodiesWriter / Aggregator /
//     TurnLogsWriter）并组装为 SessionWriterV2，随后将其注入 telemetry 的
//     AddOnRequestLogPersisted hook 实现 shadow write。
//  2. Feature-flagged — 只有 sessions_v2.enabled && sessions_v2.shadow_write
//     同时为 true 时才执行 V2 写入（hook 内部判断）。启动期不阻塞。
//  3. Fail-safe — V2 写入失败只 log WARN 不阻塞主 request_logs INSERT。
//  4. Schema 先决 — V2 表 (public.sessions / public.session_turns /
//     public.session_bodies / public.session_turn_logs) 必须在 252 上已跑
//     430_sessions_v2_schema.sql。init 不自动建表，启动期失败会 log WARN
//     但不 fatal（退化 = shadow write 静默关闭）。
//  5. 配置驱动 — timeout / retention 等参数从 settings（hot-reload 平台配置）
//     读取而非硬编码。
package main

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	v2 "github.com/kaixuan/llm-gateway-go/domains/session/v2"
)

// initSessionV2Writer creates SessionWriterV2 and all of its sub-writers.
//
// 返回 nil 当 pool 为 nil 时（no DB mode），调用方负责 nil-check。
//
// Hot-reload note (V2-P2.3): the master flag (sessions_v2.enabled) and
// shadow flag (sessions_v2.shadow_write) are read LIVE by PersistHook on every
// request. We therefore construct the writer whenever a DB pool is available,
// even when both flags are off at startup; otherwise an off → on hot reload
// would have no registered writer/hook and would silently require a restart.
func initSessionV2Writer(pool *pgxpool.Pool) *v2.SessionWriterV2 {
	if pool == nil {
		slog.Warn("session V2: nil pool, shadow write disabled")
		return nil
	}

	turnWriter := v2.NewTurnWriter(pool)
	bodiesWriter := v2.NewSessionBodiesWriter(pool)
	aggregator := v2.NewSessionAggregator(pool)
	turnLogsWriter := v2.NewTurnLogsWriter(pool)

	writer := v2.NewSessionWriterV2(turnWriter, bodiesWriter, aggregator, turnLogsWriter)
	slog.Info("session V2 writer initialized (shadow hook remains feature-gated)")
	return writer
}

// startSessionAggregateOutboxReaper boots the durable retry queue for
// session aggregate snapshot updates (audit-data-closure-C, 2026-08-31).
//
// The writer enqueues an outbox row in the SAME transaction as the turn
// + bodies insert; this reaper drains it with FOR UPDATE SKIP LOCKED and
// exponential backoff. Returning *v2.SessionAggregateOutboxReaper (the
// concrete return of StartSessionAggregateOutboxReaper) is nil when the
// pool is unavailable. The returned handle is what stopSessionAggregateOutboxReaper
// uses to drain in-flight ticks before the DB pool is closed.
func startSessionAggregateOutboxReaper(ctx context.Context, pool *pgxpool.Pool) any {
	if pool == nil {
		slog.Warn("session aggregate outbox reaper: nil pool, reaper disabled")
		return nil
	}
	aggregator := v2.NewSessionAggregator(pool)
	return v2.StartSessionAggregateOutboxReaper(ctx, pool, aggregator)
}

// stopSessionAggregateOutboxReaper drains the reaper's in-flight tick.
// Nil-safe. Idempotent (Stop closes doneCh at most once).
//
// MUST be called BEFORE pools.CloseAll() and AFTER telemetryClient.Stop so
// that (a) the DB connection is still usable while the final tick finishes
// and (b) no new outbox rows are being enqueued concurrently.
func stopSessionAggregateOutboxReaper(reaper any) {
	if reaper == nil {
		return
	}
	type stopper interface{ Stop() }
	if s, ok := reaper.(stopper); ok {
		s.Stop()
		slog.Info("session aggregate outbox reaper stopped")
	}
}

// stopSessionV2Writer drains the writer's lifecycle-managed aggregate
// goroutine (spec §6.3). Nil-safe. Idempotent (writer.Stop is itself
// sync.Once-guarded).
//
// MUST be called AFTER telemetryClient.Stop in the shutdown sequence: the
// aggregate goroutine's work is triggered by the telemetry onPersisted hook,
// so only after telemetry has drained can we be sure no new aggregate work
// will be enqueued.
func stopSessionV2Writer(writer *v2.SessionWriterV2) {
	if writer == nil {
		return
	}
	// No deadline on the wait itself — Stop bounds each aggregate call with
	// its own 30s timeout via lifecycleCtx, so this returns promptly once the
	// in-flight snapshot update finishes or is cancelled.
	if err := writer.Stop(context.Background()); err != nil {
		slog.Warn("session V2 writer stop returned error", "error", err)
	}
	slog.Info("session V2 writer stopped (aggregate goroutine drained)")
}
