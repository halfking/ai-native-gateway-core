package session

import (
	"context"
	"errors"
	"fmt"

	redissafe "github.com/kaixuan/llm-gateway-go/internal/redis"
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
// 键类型损坏（非 hash）按空映射处理并记录告警：脱敏映射丢失时
// 调用方会走全量脱敏路径，不能因类型错误而中断请求。
func (sm *Manager) GetSanitizeMap(ctx context.Context, sessionID string) (sanitize.SanitizeMap, error) {
	if sm == nil || sm.redis == nil {
		return make(sanitize.SanitizeMap), nil
	}

	client := sm.redis.Client()
	if client == nil {
		return make(sanitize.SanitizeMap), nil
	}

	key := fmt.Sprintf(sanitizeRedisPrefix, sessionID)
	result, err := redissafe.SafeHGetAll(ctx, client, key)
	if err != nil {
		if errors.Is(err, redissafe.ErrKeyNotFound) {
			return make(sanitize.SanitizeMap), nil
		}
		if errors.Is(err, redissafe.ErrWrongType) {
			logSessionTypeMismatch("get_sanitize_map", sessionID, err)
			return make(sanitize.SanitizeMap), nil
		}
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
