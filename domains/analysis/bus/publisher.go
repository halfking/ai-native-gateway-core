// Package bus 是 V4 治理平台异步分析层的事件总线（PR-V4-06 引入）。
//
// 核心组件：
//   - Publisher：把 AnalysisEvent 落到 analysis_events 表
//   - Loop：worker 拉取 + 处理 + 回写 processed_at 的调度循环
//
// 本包与 DB schema 解耦：Publisher 接口可被 NoopPublisher / PGPublisher /
// 其他实现替换；Loop 的 poll/mark 回调也可被测试桩替换。
package bus

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/domain/analysis"
)

// Publisher 把分析事件落库（或其他持久层）。
//
// Publish 失败不视为致命错误：调用方应记录日志并继续主流程；
// 异步 worker 会基于已落库的事件做后续处理。
type Publisher interface {
	Publish(ctx context.Context, evt analysis.AnalysisEvent) error
	Close() error
}

// PGPublisher 基于 pgxpool.Pool 的 Publisher 实现。
//
// 依赖：services/llm-gateway-go/db/migrations/306_analysis_events.sql 已建表。
type PGPublisher struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

// NewPGPublisher 构造 PG publisher。
func NewPGPublisher(pool *pgxpool.Pool, logger *slog.Logger) *PGPublisher {
	if logger == nil {
		logger = slog.Default()
	}
	return &PGPublisher{pool: pool, logger: logger}
}

// Publish 写入 analysis_events；event_id 唯一约束下重复幂等。
func (p *PGPublisher) Publish(ctx context.Context, evt analysis.AnalysisEvent) error {
	if p == nil || p.pool == nil {
		return nil
	}
	payload := []byte("null")
	if evt.Payload != nil {
		raw, err := json.Marshal(evt.Payload)
		if err != nil {
			p.logger.Warn("analysis.Publish: marshal payload failed",
				"event_id", evt.EventID, "type", evt.Type, "error", err)
			return err
		}
		payload = raw
	}
	occurredAt := evt.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = time.Now()
	}
	_, err := p.pool.Exec(ctx, `
		INSERT INTO analysis_events
			(event_id, type, tenant_id, session_id, request_id, payload, occurred_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (event_id) DO NOTHING
	`, evt.EventID, string(evt.Type), evt.TenantID, evt.SessionID, evt.RequestID, payload, occurredAt)
	return err
}

// Close 释放 pool（如有）。
func (p *PGPublisher) Close() error {
	if p == nil || p.pool == nil {
		return nil
	}
	p.pool.Close()
	return nil
}

// NoopPublisher 测试与禁用异步分析时使用。
type NoopPublisher struct{}

// Publish 始终返回 nil。
func (NoopPublisher) Publish(_ context.Context, _ analysis.AnalysisEvent) error { return nil }

// Close 始终返回 nil。
func (NoopPublisher) Close() error { return nil }
