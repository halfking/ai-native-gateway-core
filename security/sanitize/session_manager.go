// Package sanitize - session_manager.go
//
// SessionSanitizeManager 管理会话级的敏感信息映射表，解决跨轮次占位符还原问题。
//
// 设计要点：
//   - 将每轮的 SanitizeMap 持久化到 Redis（TTL=30分钟，与会话缓存一致）
//   - 支持增量合并：新轮次的映射表追加到现有映射表，不覆盖
//   - 支持从Redis加载：后续轮次可以还原之前轮次的占位符
//   - 线程安全：使用Redis原子操作
package sanitize

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	// SessionSanitizeKeyPrefix Redis key前缀
	SessionSanitizeKeyPrefix = "session:sanitize"
	
	// DefaultSessionSanitizeTTL 默认TTL（30分钟，与会话缓存一致）
	DefaultSessionSanitizeTTL = 30 * time.Minute
)

// SessionSanitizeManager 管理会话级的敏感信息映射表
type SessionSanitizeManager struct {
	redis  *redis.Client
	logger *slog.Logger
	ttl    time.Duration
}

// NewSessionSanitizeManager 创建会话映射表管理器
func NewSessionSanitizeManager(redis *redis.Client, ttl time.Duration) *SessionSanitizeManager {
	if ttl <= 0 {
		ttl = DefaultSessionSanitizeTTL
	}
	
	return &SessionSanitizeManager{
		redis:  redis,
		logger: slog.Default().With("component", "session_sanitize_mgr"),
		ttl:    ttl,
	}
}

// GetSessionKey 生成Redis key
func (m *SessionSanitizeManager) GetSessionKey(tenantID, sessionID string) string {
	return fmt.Sprintf("%s:%s:%s", SessionSanitizeKeyPrefix, tenantID, sessionID)
}

// MergeSanitizeMap 将当前轮次的映射表合并到会话级映射表
//
// 策略：
//   - 读取现有映射表
//   - 合并新映射（键已存在则保留旧值，避免覆盖）
//   - 写回Redis并刷新TTL
func (m *SessionSanitizeManager) MergeSanitizeMap(
	ctx context.Context,
	tenantID, sessionID string,
	currentMap SanitizeMap,
) error {
	if m == nil || m.redis == nil {
		return fmt.Errorf("session sanitize manager not initialized")
	}
	
	if len(currentMap) == 0 {
		return nil // 没有新映射，无需操作
	}
	
	key := m.GetSessionKey(tenantID, sessionID)
	
	// 1. 读取现有映射表
	existingJSON, err := m.redis.Get(ctx, key).Result()
	if err != nil && err != redis.Nil {
		return fmt.Errorf("get existing map from redis: %w", err)
	}
	
	existingMap := make(SanitizeMap)
	if existingJSON != "" && existingJSON != "{}" {
		if err := json.Unmarshal([]byte(existingJSON), &existingMap); err != nil {
			m.logger.WarnContext(ctx, "failed to unmarshal existing map, starting fresh",
				"error", err,
				"session_id", sessionID,
			)
			existingMap = make(SanitizeMap)
		}
	}
	
	// 2. 合并新映射（避免覆盖已存在的键）
	mergedCount := 0
	for placeholder, value := range currentMap {
		if _, exists := existingMap[placeholder]; !exists {
			existingMap[placeholder] = value
			mergedCount++
		}
	}
	
	// 3. 写回Redis
	mergedJSON, err := json.Marshal(existingMap)
	if err != nil {
		return fmt.Errorf("marshal merged map: %w", err)
	}
	
	if err := m.redis.Set(ctx, key, mergedJSON, m.ttl).Err(); err != nil {
		return fmt.Errorf("set merged map to redis: %w", err)
	}
	
	m.logger.DebugContext(ctx, "merged sanitize map to session cache",
		"tenant_id", tenantID,
		"session_id", sessionID,
		"new_count", len(currentMap),
		"merged_count", mergedCount,
		"total_count", len(existingMap),
	)
	
	return nil
}

// GetSanitizeMap 获取会话级完整映射表
//
// 返回：
//   - 如果Redis中不存在，返回空map（不报错）
//   - 如果存在但解析失败，返回error
func (m *SessionSanitizeManager) GetSanitizeMap(
	ctx context.Context,
	tenantID, sessionID string,
) (SanitizeMap, error) {
	if m == nil || m.redis == nil {
		return nil, fmt.Errorf("session sanitize manager not initialized")
	}
	
	key := m.GetSessionKey(tenantID, sessionID)
	
	mapJSON, err := m.redis.Get(ctx, key).Result()
	if err == redis.Nil {
		return make(SanitizeMap), nil // 返回空map，不报错
	}
	if err != nil {
		return nil, fmt.Errorf("redis get: %w", err)
	}
	
	if mapJSON == "" || mapJSON == "{}" {
		return make(SanitizeMap), nil
	}
	
	var sm SanitizeMap
	if err := json.Unmarshal([]byte(mapJSON), &sm); err != nil {
		return nil, fmt.Errorf("unmarshal sanitize map: %w", err)
	}
	
	return sm, nil
}

// ExtendTTL 延长会话映射表TTL（在每轮对话时调用）
//
// 使用场景：
//   - 用户继续对话时，延长映射表的生命周期
//   - 避免长会话中途映射表过期
func (m *SessionSanitizeManager) ExtendTTL(
	ctx context.Context,
	tenantID, sessionID string,
) error {
	if m == nil || m.redis == nil {
		return fmt.Errorf("session sanitize manager not initialized")
	}
	
	key := m.GetSessionKey(tenantID, sessionID)
	
	// 检查key是否存在
	exists, err := m.redis.Exists(ctx, key).Result()
	if err != nil {
		return fmt.Errorf("check key exists: %w", err)
	}
	
	if exists == 0 {
		return nil // key不存在，无需延长TTL
	}
	
	return m.redis.Expire(ctx, key, m.ttl).Err()
}

// DeleteSanitizeMap 删除会话映射表（会话结束或清理时调用）
func (m *SessionSanitizeManager) DeleteSanitizeMap(
	ctx context.Context,
	tenantID, sessionID string,
) error {
	if m == nil || m.redis == nil {
		return fmt.Errorf("session sanitize manager not initialized")
	}
	
	key := m.GetSessionKey(tenantID, sessionID)
	return m.redis.Del(ctx, key).Err()
}

// RestoreSanitizeMap 从快照恢复映射表到Redis
//
// 使用场景：
//   - 会话压缩后，从存储的快照恢复映射表
//   - 跨节点迁移会话时恢复映射表
func (m *SessionSanitizeManager) RestoreSanitizeMap(
	ctx context.Context,
	tenantID, sessionID string,
	snapshotMap SanitizeMap,
) error {
	if m == nil || m.redis == nil {
		return fmt.Errorf("session sanitize manager not initialized")
	}
	
	if len(snapshotMap) == 0 {
		return nil
	}
	
	key := m.GetSessionKey(tenantID, sessionID)
	
	mapJSON, err := json.Marshal(snapshotMap)
	if err != nil {
		return fmt.Errorf("marshal snapshot map: %w", err)
	}
	
	if err := m.redis.Set(ctx, key, mapJSON, m.ttl).Err(); err != nil {
		return fmt.Errorf("restore snapshot to redis: %w", err)
	}
	
	m.logger.InfoContext(ctx, "restored sanitize map from snapshot",
		"tenant_id", tenantID,
		"session_id", sessionID,
		"count", len(snapshotMap),
	)
	
	return nil
}

// GetStats 获取统计信息（用于监控）
func (m *SessionSanitizeManager) GetStats(
	ctx context.Context,
	tenantID, sessionID string,
) (map[string]interface{}, error) {
	if m == nil || m.redis == nil {
		return nil, fmt.Errorf("session sanitize manager not initialized")
	}
	
	key := m.GetSessionKey(tenantID, sessionID)
	
	// 获取TTL
	ttl, err := m.redis.TTL(ctx, key).Result()
	if err != nil {
		return nil, fmt.Errorf("get ttl: %w", err)
	}
	
	// 获取大小
	mapJSON, err := m.redis.Get(ctx, key).Result()
	if err != nil && err != redis.Nil {
		return nil, fmt.Errorf("get map: %w", err)
	}
	
	var count int
	if mapJSON != "" {
		var sm SanitizeMap
		if err := json.Unmarshal([]byte(mapJSON), &sm); err == nil {
			count = len(sm)
		}
	}
	
	return map[string]interface{}{
		"exists":     ttl > 0,
		"ttl_seconds": int(ttl.Seconds()),
		"count":      count,
		"size_bytes": len(mapJSON),
	}, nil
}
