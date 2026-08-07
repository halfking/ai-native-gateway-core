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

// mergeLuaScript 原子合并映射表的Lua脚本
// 参数：KEYS[1]=redis key, ARGV[1]=新映射JSON, ARGV[2]=TTL秒数
// 行为：读取现有JSON → 反序列化 → 合并（不覆盖已有键）→ 序列化 → SET with TTL
// 返回：合并后的总条目数
const mergeLuaScript = `
local key = KEYS[1]
local new_json = ARGV[1]
local ttl = tonumber(ARGV[2])

local existing = redis.call('GET', key)
local merged = {}

-- 解析现有映射表
if existing then
  local ok, parsed = pcall(cjson.decode, existing)
  if ok and type(parsed) == "table" then
    for k, v in pairs(parsed) do
      merged[k] = v
    end
  end
end

-- 解析新映射表并合并（不覆盖已有键）
local ok2, new_map = pcall(cjson.decode, new_json)
if ok2 and type(new_map) == "table" then
  for k, v in pairs(new_map) do
    if merged[k] == nil then
      merged[k] = v
    end
  end
end

-- 序列化并写回
local out_json = cjson.encode(merged)
if ttl > 0 then
  redis.call('SET', key, out_json, 'EX', ttl)
else
  redis.call('SET', key, out_json)
end

return #merged
`

// MergeSanitizeMap 将当前轮次的映射表合并到会话级映射表
//
// 策略（原子操作，通过Lua脚本实现）：
//   - 读取现有映射表
//   - 合并新映射（键已存在则保留旧值，避免覆盖）
//   - 写回Redis并刷新TTL
//
// 并发安全：同一会话的并发请求不会导致数据丢失
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

	// 序列化新映射表
	newJSON, err := json.Marshal(currentMap)
	if err != nil {
		return fmt.Errorf("marshal current map: %w", err)
	}

	// 执行原子合并
	ttlSeconds := int64(m.ttl.Seconds())
	result, err := m.redis.Eval(ctx, mergeLuaScript, []string{key}, string(newJSON), ttlSeconds).Int()
	if err != nil {
		return fmt.Errorf("redis eval merge script: %w", err)
	}

	m.logger.DebugContext(ctx, "merged sanitize map to session cache",
		"tenant_id", tenantID,
		"session_id", sessionID,
		"new_count", len(currentMap),
		"total_count", result,
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
