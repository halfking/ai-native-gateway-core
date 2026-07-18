package session

import (
	"context"
	"fmt"

	"github.com/kaixuan/llm-gateway-go/security/sanitize"
)

const sanitizeRedisPrefix = "session:%s:sanitize"

// SaveSanitizeMap 将脱敏映射表持久化到 Redis（与 session 同 TTL）。
//
// 每个占位符→原始值作为 Redis Hash 的一个 field，key 为：
//
//	session:{sessionID}:sanitize
//
// 多轮对话场景下：同一 session 的每次请求追加新的 field 到已有 hash。
// 如果 key 不存在则自动创建，并设置与 session 一致的 TTL。
func (sm *Manager) SaveSanitizeMap(ctx context.Context, sessionID string, data sanitize.SanitizeMap) error {
	if sm == nil || sm.redis == nil {
		return nil
	}
	if len(data) == 0 {
		return nil
	}

	key := fmt.Sprintf(sanitizeRedisPrefix, sessionID)

	fields := make(map[string]any, len(data))
	for k, v := range data {
		fields[k] = v
	}

	if err := sm.redis.HSet(ctx, key, fields); err != nil {
		return fmt.Errorf("save sanitize map failed: %w (session=%s)", err, sessionID)
	}

	if err := sm.redis.Expire(ctx, key, sm.ttl); err != nil {
		return fmt.Errorf("save sanitize map expire failed: %w (session=%s)", err, sessionID)
	}

	return nil
}

// GetSanitizeMap 从 Redis 加载脱敏映射表。
//
// 返回的 SanitizeMap 可能为空（没有脱敏数据或 session 已过期）。
func (sm *Manager) GetSanitizeMap(ctx context.Context, sessionID string) (sanitize.SanitizeMap, error) {
	if sm == nil || sm.redis == nil {
		return make(sanitize.SanitizeMap), nil
	}

	key := fmt.Sprintf(sanitizeRedisPrefix, sessionID)
	result, err := sm.redis.HGetAll(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("get sanitize map failed: %w (session=%s)", err, sessionID)
	}

	smCopy := make(sanitize.SanitizeMap, len(result))
	for k, v := range result {
		smCopy[k] = v
	}

	return smCopy, nil
}

// DeleteSanitizeMap 删除脱敏映射表（session 主动关闭时使用）。
func (sm *Manager) DeleteSanitizeMap(ctx context.Context, sessionID string) error {
	if sm == nil || sm.redis == nil {
		return nil
	}

	key := fmt.Sprintf(sanitizeRedisPrefix, sessionID)
	if err := sm.redis.Del(ctx, key); err != nil {
		return fmt.Errorf("delete sanitize map failed: %w (session=%s)", err, sessionID)
	}

	return nil
}
