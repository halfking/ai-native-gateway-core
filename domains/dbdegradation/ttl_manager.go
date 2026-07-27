package dbdegradation

import (
	"context"
	"log/slog"
	"sync"
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
	mu             sync.Mutex   // 保护生命周期状态
	running        bool         // 是否已启动
	closed         bool         // 是否已关闭
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
		// 2026-07-27 concurrency fix: stopCh/doneCh 不再在构造时一次性创建
		// —— 每次 Enter 启动循环时重建一对（见 EnterDegradedMode）。
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

	// 先启动定期延长循环
	// 2026-07-27 concurrency fix: 每次启动都新建 stopCh/doneCh 并把这一对
	// 传给循环。之前两者只在构造函数里建一次，Enter→Exit→Enter（DB 抖动时
	// dbMonitor 每次都会回调）会启动第二个 runExtendLoop，其
	// `defer close(tm.doneCh)` 对已关闭 channel panic。
	tm.mu.Lock()
	startedHere := false
	if !tm.running && !tm.closed {
		tm.stopCh = make(chan struct{})
		tm.doneCh = make(chan struct{})
		tm.running = true
		startedHere = true
		go tm.runExtendLoop(tm.stopCh, tm.doneCh)
	}
	tm.mu.Unlock()

	// 立即延长所有会话 TTL
	if err := tm.extendAllSessionTTLs(ctx, tm.degradedTTL); err != nil {
		slog.Warn("ttl_manager: failed to extend TTLs on enter", "error", err)
		// 失败时回滚：只回滚本次调用启动的循环，避免关掉别人的 channel
		if startedHere {
			tm.mu.Lock()
			done := tm.stopLoopLocked()
			tm.mu.Unlock()
			if done != nil {
				<-done
			}
		}
		return err
	}

	tm.mode.Store("degraded")
	return nil
}

// ExitDegradedMode 退出降级模式
func (tm *TTLManager) ExitDegradedMode(ctx context.Context) error {
	if tm.GetMode() == "normal" {
		return nil // 已经在正常模式
	}

	slog.Info("ttl_manager: exiting degraded mode")

	// 停止延长循环
	tm.mu.Lock()
	done := tm.stopLoopLocked()
	tm.mu.Unlock()

	// 等待循环退出（没在跑时 done 为 nil，直接跳过）
	if done != nil {
		<-done
	}

	// 恢复正常 TTL（可选：不主动缩短 TTL，让 Redis 自然过期）
	tm.mode.Store("normal")

	slog.Info("ttl_manager: returned to normal mode")
	return nil
}

// GetMode 获取当前模式
func (tm *TTLManager) GetMode() string {
	return tm.mode.Load().(string)
}

// Stop 停止 TTL 管理器
func (tm *TTLManager) Stop(ctx context.Context) error {
	// 2026-07-27 concurrency fix: 不再持锁等待 goroutine 退出（会和
	// stopLoopLocked 的调用方互相挡住），并且 doneCh 为 nil 时直接返回。
	tm.mu.Lock()
	if tm.closed {
		tm.mu.Unlock()
		return nil // 已经关闭
	}
	done := tm.stopLoopLocked()
	tm.closed = true
	tm.mu.Unlock()

	if done == nil {
		return nil // 循环没在跑
	}

	// 等待 goroutine 退出（带超时）
	select {
	case <-done:
		slog.Info("ttl_manager: stopped gracefully")
	case <-ctx.Done():
		slog.Warn("ttl_manager: stop timeout", "error", ctx.Err())
		return ctx.Err()
	}

	return nil
}

// stopLoopLocked 关闭当前运行中的循环并返回它的 doneCh；未在运行时返回 nil。
// 调用方必须持有 tm.mu。running 标志保证 stopCh 只会被 close 一次。
func (tm *TTLManager) stopLoopLocked() chan struct{} {
	if !tm.running {
		return nil
	}
	close(tm.stopCh)
	tm.running = false
	done := tm.doneCh
	tm.stopCh = nil
	tm.doneCh = nil
	return done
}

// runExtendLoop 运行定期延长循环
//
// stopCh/doneCh 由调用方（EnterDegradedMode）为本次运行单独创建并传入，
// 保证反复 Enter/Exit 不会 close 同一个 channel 两次。
func (tm *TTLManager) runExtendLoop(stopCh, doneCh chan struct{}) {
	defer close(doneCh)
	ticker := time.NewTicker(tm.extendInterval)
	defer ticker.Stop()

	for {
		select {
		case <-stopCh:
			slog.Info("ttl_manager: extend loop stopped")
			return
		case <-ticker.C:
			if err := tm.extendAllSessionTTLs(context.Background(), tm.degradedTTL); err != nil {
				slog.Warn("ttl_manager: failed to extend TTLs", "error", err)
			}
		}
	}
}

// extendAllSessionTTLs 延长所有会话的 TTL
func (tm *TTLManager) extendAllSessionTTLs(ctx context.Context, ttl time.Duration) error {
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
		extended, err := tm.extendKeysByPattern(ctx, client, pattern, ttl)
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
		"ttl", ttl.String(),
	)

	return nil
}

// extendKeysByPattern 按模式延长键的 TTL
func (tm *TTLManager) extendKeysByPattern(ctx context.Context, client *redis.Client, pattern string, ttl time.Duration) (int, error) {
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
				pipe.Expire(ctx, key, ttl)
			}

			if _, err := pipe.Exec(ctx); err != nil {
				slog.Warn("ttl_manager: pipeline exec failed", "error", err)
				return count, err
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
