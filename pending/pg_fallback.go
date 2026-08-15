// pg_fallback.go — pending 查询的 PostgreSQL 回源路径（doc 18 §12.1）。
//
// PostgreSQL 任务表（durable_llm_tasks，Wave 2 建表）是任务状态与最终结果的
// SSoT，Redis pending_response 只是查询加速投影。Redis 丢失（连接失败或
// TTL/投影丢失）时，查询接口必须能从任务表回源返回状态与规范化结果，
// 不能出现「任务完成但结果永久丢失」。
//
// 开关语义：回源仅在 Store 以 NewStoreWithFallback 显式注入 source 时生效；
// 既有 NewStore 构造的 Store（全部现有调用点）fallback 为 nil，行为与
// main 完全一致（零行为变化）。
package pending

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// FallbackSource 是 Redis 不可用/未命中时的回源读取接口。Wave 2 的
// durable 任务表 repository 实现它；本轨提供 PGSource 参考实现
// （见 pg_source.go）。
type FallbackSource interface {
	Get(ctx context.Context, sessionID, requestID string) (*Response, bool, error)
	GetLatest(ctx context.Context, sessionID string) (*Response, string, bool, error)
}

// NewStoreWithFallback 构造带 PG 回源的 Store。rdb 可为 nil（Redis 完全
// 不可用时直接回源）。src 为 nil 时等价于 NewStore（回源开关关闭）。
func NewStoreWithFallback(rdb *redis.Client, ttl time.Duration, src FallbackSource) *Store {
	s := NewStore(rdb, ttl)
	s.fallback = src
	return s
}
