// Package assets 是 V4 治理平台的资产沉淀层（PR-V4-07 引入）。
//
// 当前只承载 IntentAggregateStore：按 (tenant_id, intent_kind) 累计请求数。
// 后续 PR 将引入：
//   - SessionSummaryStore  会话摘要
//   - PromptTemplateBank   高质量提示词模板
//   - FailurePatternDB     失败模式
//   - ToolPolicyRegistry   工具执行策略
//   - SuggestionStore      优化建议
//
// 本包的所有 store 接口都遵守相同的语义约定：
//   - 增量写入是 upsert（ON CONFLICT）
//   - 读路径按 tenant_id 隔离
package assets

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/domain/analysis"
)

// IntentAggregate 单条 (tenant, kind) 累计记录。
type IntentAggregate struct {
	TenantID    string
	IntentKind  analysis.IntentKind
	Count       int64
	LastUpdated time.Time
}

// IntentAggregateStore 意图聚合 store 接口。
type IntentAggregateStore interface {
	// Increment 把指定 (tenant, kind) 计数增加 delta（delta 应 >= 0）。
	Increment(ctx context.Context, tenantID string, kind analysis.IntentKind, delta int64) error

	// Get 返回该 tenant 下所有 intent 累计；按 IntentKind 字典序排序。
	Get(ctx context.Context, tenantID string) ([]IntentAggregate, error)
}

// PGIntentAggregateStore 基于 pgxpool.Pool 的实现。
//
// 表 schema 见 db/migrations/307_intent_aggregates.sql。
type PGIntentAggregateStore struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

// NewPGIntentAggregateStore 构造 store。
func NewPGIntentAggregateStore(pool *pgxpool.Pool, logger *slog.Logger) *PGIntentAggregateStore {
	if logger == nil {
		logger = slog.Default()
	}
	return &PGIntentAggregateStore{pool: pool, logger: logger}
}

// Increment upsert delta 到 (tenant_id, intent_kind) 计数。
func (s *PGIntentAggregateStore) Increment(ctx context.Context, tenantID string, kind analysis.IntentKind, delta int64) error {
	if delta <= 0 {
		// 无意义的调用直接跳过，避免向 DB 写入 0/负数。
		return nil
	}
	if s == nil || s.pool == nil {
		return errors.New("assets: nil store")
	}
	if tenantID == "" {
		return errors.New("assets: empty tenant_id")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO intent_aggregates (tenant_id, intent_kind, count, last_updated)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (tenant_id, intent_kind) DO UPDATE
		SET count = intent_aggregates.count + EXCLUDED.count,
		    last_updated = NOW()
	`, tenantID, string(kind), delta)
	if err != nil {
		s.logger.Warn("assets.Increment failed",
			"tenant_id", tenantID, "kind", kind, "delta", delta, "error", err)
	}
	return err
}

// Get 读取该 tenant 下所有累计；按 intent_kind 排序。
func (s *PGIntentAggregateStore) Get(ctx context.Context, tenantID string) ([]IntentAggregate, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("assets: nil store")
	}
	if tenantID == "" {
		return nil, errors.New("assets: empty tenant_id")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT tenant_id, intent_kind, count, last_updated
		FROM intent_aggregates
		WHERE tenant_id = $1
		ORDER BY intent_kind
	`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]IntentAggregate, 0, 8)
	for rows.Next() {
		var a IntentAggregate
		var kind string
		if err := rows.Scan(&a.TenantID, &kind, &a.Count, &a.LastUpdated); err != nil {
			return nil, err
		}
		a.IntentKind = analysis.IntentKind(kind)
		out = append(out, a)
	}
	return out, rows.Err()
}

// Close 释放 pool（如调用方拥有）。
func (s *PGIntentAggregateStore) Close() error {
	if s == nil || s.pool == nil {
		return nil
	}
	s.pool.Close()
	return nil
}

// NoopIntentAggregateStore 测试与禁用资产沉淀时使用。
type NoopIntentAggregateStore struct{}

// Increment 始终 nil。
func (NoopIntentAggregateStore) Increment(_ context.Context, _ string, _ analysis.IntentKind, _ int64) error {
	return nil
}

// Get 始终返回空切片。
func (NoopIntentAggregateStore) Get(_ context.Context, _ string) ([]IntentAggregate, error) {
	return nil, nil
}
