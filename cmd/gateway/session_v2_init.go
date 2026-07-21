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
//  4. Schema 先决 — V2 表 (gateway.sessions / gateway.session_turns /
//     gateway.session_bodies / gateway.session_turn_logs) 必须在 252 上已跑
//     430_sessions_v2_schema.sql。init 不自动建表，启动期失败会 log WARN
//     但不 fatal（退化 = shadow write 静默关闭）。
//  5. 配置驱动 — timeout / retention 等参数从 settings（hot-reload 平台配置）
//     读取而非硬编码。
package main

import (
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/domains/session/v2"
)

// initSessionV2Writer 创建 SessionWriterV2 及其所有子 writer。
//
// 返回 nil 当 pool 为 nil 时（no DB mode），调用方负责 nil-check。
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
	slog.Info("session V2 writer initialized (shadow write ready)")
	return writer
}
