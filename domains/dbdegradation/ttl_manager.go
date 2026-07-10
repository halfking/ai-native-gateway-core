package dbdegradation

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/session"
	"github.com/redis/go-redis/v9"
)

// TTLManager Redis TTL 管理器
type TTLManager struct {
	redis          *session.RedisClient
	normalTTL      time.Duration
	degradedTTL    time.Duration
	extendInterval time.Duration
	mode           atomic.Value // string: "normal" | "degraded"
	stopCh         chan struct{}
	doneCh         chan struct{}
}

// NewTTLManager 创建 TTL 管理器
func NewTTLManager(redis *session.RedisClient, normalTTL, degradedTTL time.Duration) *TTLManager {
	if normalTTL <= 0 {
		normalTTL = 7 * 24 * time.Hour
	}
	if degradedTTL <= 0 {
		degradedTTL = 30 * 24 * time.Hour
	}

	tm := &TTLManager{
		redis:          redis,
		normalTTL:      normalTTL,
		degradedTTL:    degradedTTL,
		extendInterval: 1 * time.Hour, // 每小时延长一次
		stopCh:         make(chan struct{}),
		doneCh:         make(chan struct{}),
	}
	tm.mode.Store("normal")
	return tm
}

// EnterDegradedMode 进入降级模式
func (tm *TTLManager) EnterDegradedMode(ctx context.Context) error {
	if tm.GetMode() == "degraded" {
		return nil // 已经在降级模式
	}

	slog.Info("ttl_manager: entering degraded mode")
	tm.mode.Store("degraded")

	// 立即延长所有会话 TTL
	if err := tm.extendAllSessionTTLs(ctx); err != nil {
		slog.Warn("ttl_manager: failed to extend TTLs", "error", err)
	}

	// 启动定期延长循环
	go tm.runExtendLoop()

	return nil
}

// ExitDegradedMode 退出降级模式
func (tm *TTLManager) ExitDegradedMode(ctx context.Context) error {
	if tm.GetMode() == "normal" {
		return nil // 已经在正常模式
	}

	slog.Info("ttl_manager: exiting degraded mode")

	// 停止延长循环
	close(tm.stopCh)
	<-tm.doneCh

	// 恢复正常 TTL
	tm.mode.Store("normal")

	// 重置 channel（为下次使用）
	tm.stopCh = make(chan struct{})
	tm.doneCh = make(chan struct{})

	slog.Info("ttl_manager: returned to normal mode")
	return nil
}

// GetMode 获取当前模式
func (tm *TTLManager) GetMode() string {
	return tm.mode.Load().(string)
}

// runExtendLoop 运行定期延长循环
func (tm *TTLManager) runExtendLoop() {
	defer close(tm.doneCh)
	ticker := time.NewTicker(tm.extendInterval)
	defer ticker.Stop()

	for {
		select {
		case <-tm.stopCh:
			slog.Info("ttl_manager: extend loop stopped")
			return
		case <-ticker.C:
			if err := tm.extendAllSessionTTLs(context.Background()); err != nil {
				slog.Warn("ttl_manager: failed to extend TTLs", "error", err)
			}
		}
	}
}

// extendAllSessionTTLs 延长所有会话的 TTL
func (tm *TTLManager) extendAllSessionTTLs(ctx context.Context) error {
	client := tm.redis.Client()
	if client == nil {
		return nil
	}

	patterns := []string{
		"session:*",         // 会话主键
		"session:key:*",     // 会话密钥映射
		"session:apiKey:*",  // API 密钥索引
		"session:stopped:*", // 停止会话索引
		"ursm:*",            // URSM 路由状态
	}

	totalExtended := 0

	for _, pattern := range patterns {
		extended, err := tm.extendKeysByPattern(ctx, client, pattern)
		if err != nil {
			slog.Warn("ttl_manager: failed to extend keys",
				"pattern", pattern,
				"error", err,
			)
			continue
		}
		totalExtended += extended
	}

	slog.Info("ttl_manager: extended TTLs",
		"count", totalExtended,
		"ttl", tm.degradedTTL.String(),
	)

	return nil
}

// extendKeysByPattern 按模式延长键的 TTL
func (tm *TTLManager) extendKeysByPattern(ctx context.Context, client *redis.Client, pattern string) (int, error) {
	var cursor uint64
	count := 0
	batchSize := 100

	for {
		// 使用 SCAN 遍历键
		keys, newCursor, err := client.Scan(ctx, cursor, pattern, int64(batchSize)).Result()
		if err != nil {
			return count, err
		}

		if len(keys) > 0 {
			// 使用 Pipeline 批量设置 TTL
			pipe := client.Pipeline()
			for _, key := range keys {
				pipe.Expire(ctx, key, tm.degradedTTL)
			}

			if _, err := pipe.Exec(ctx); err != nil {
				slog.Warn("ttl_manager: pipeline exec failed", "error", err)
			} else {
				count += len(keys)
			}
		}

		cursor = newCursor
		if cursor == 0 {
			break // 遍历完成
		}
	}

	return count, nil
}

// GetTTLStats 获取 TTL 统计信息
func (tm *TTLManager) GetTTLStats(ctx context.Context) map[string]interface{} {
	client := tm.redis.Client()
	if client == nil {
		return map[string]interface{}{
			"mode":  tm.GetMode(),
			"error": "redis client not available",
		}
	}

	stats := map[string]interface{}{
		"mode":            tm.GetMode(),
		"normal_ttl":      tm.normalTTL.String(),
		"degraded_ttl":    tm.degradedTTL.String(),
		"extend_interval": tm.extendInterval.String(),
	}

	// 统计各类键的数量
	patterns := map[string]string{
		"sessions":      "session:*",
		"session_keys":  "session:key:*",
		"api_key_index": "session:apiKey:*",
		"ursm_cache":    "ursm:*",
	}

	for name, pattern := range patterns {
		count := tm.countKeys(ctx, client, pattern)
		stats[name+"_count"] = count
	}

	return stats
}

// countKeys 统计匹配模式的键数量
func (tm *TTLManager) countKeys(ctx context.Context, client *redis.Client, pattern string) int {
	var cursor uint64
	count := 0

	for {
		keys, newCursor, err := client.Scan(ctx, cursor, pattern, 1000).Result()
		if err != nil {
			return count
		}
		count += len(keys)
		cursor = newCursor
		if cursor == 0 {
			break
		}
	}

	return count
}
