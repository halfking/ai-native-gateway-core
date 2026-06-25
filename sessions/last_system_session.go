package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// LastSystemSessionEntry 是 Redis 中 client:<apiKeyID>:last_system_session 的值。
//
// 设计目的（2026-06-26 V2 校准）：
//   当客户端在短时间内（默认 5 分钟）未提供会话 ID、但请求来自同一 client
//   （同一 apiKeyID），且上一次系统赋值的 session 仍在 Redis 中时，复用该
//   sessionID，认为是"同一会话的延续"，避免每次无 id 请求都创建新 session。
//
// 这是"会话连续性"在客户端无法提供稳定 ID 时的兜底机制。
type LastSystemSessionEntry struct {
	SessionID      string    `json:"session_id"`
	LastAssignedAt time.Time `json:"last_assigned_at"`
	DeviceSeed     string    `json:"device_seed,omitempty"`
	TaskID         string    `json:"task_id,omitempty"`
}

// LastSystemSessionTTL 默认 5 分钟——超过此时间不再复用。
// 与 credentialfpslot.ActiveGateSeconds 同值（5 min），保持配置一致。
const LastSystemSessionTTL = 5 * time.Minute

// lastSystemSessionRedisKey 返回 Redis key。
func lastSystemSessionRedisKey(apiKeyID int) string {
	return fmt.Sprintf("client:%d:last_system_session", apiKeyID)
}

// LastSystemSessionIndex 提供"系统赋值 session 在 5 分钟内的复用"能力。
//
// 行为：
//   - Get：返回 (entry, found)。found=true 表示 5 分钟内有系统赋值的 session。
//   - Set：写入 entry，TTL=LastSystemSessionTTL（5 分钟）。
//   - Touch：仅刷新 TTL（不更新 entry 内容），用于"复用旧 entry 时延长窗口"。
//
// 该索引是 best-effort 的：Redis 不可用时所有调用应降级返回 (nil, false)，
// 上层处理为"未命中"（即创建新 session）。
type LastSystemSessionIndex struct {
	client *redis.Client
	ttl    time.Duration
}

// NewLastSystemSessionIndex 创建一个 LastSystemSessionIndex 实例。
// redisClient=nil 时所有操作返回空结果（禁用索引）。
func NewLastSystemSessionIndex(redisClient *redis.Client) *LastSystemSessionIndex {
	return &LastSystemSessionIndex{
		client: redisClient,
		ttl:    LastSystemSessionTTL,
	}
}

// Get 查询指定 client 的最近一次系统赋值 session。
//
// 返回值：
//   - entry: 命中的 entry（5 分钟内）
//   - found: 是否命中（同时校验 Redis 返回值非空）
//
// 错误处理：Redis 不可达/超时 → 返回 (nil, false, nil)，调用方视为"未命中"。
func (idx *LastSystemSessionIndex) Get(ctx context.Context, apiKeyID int) (*LastSystemSessionEntry, bool, error) {
	if idx == nil || idx.client == nil {
		return nil, false, nil
	}

	key := lastSystemSessionRedisKey(apiKeyID)
	val, err := idx.client.Get(ctx, key).Result()
	if err == redis.Nil {
		return nil, false, nil
	}
	if err != nil {
		// Redis 错误：降级为未命中，不阻断请求
		return nil, false, nil
	}

	var entry LastSystemSessionEntry
	if err := json.Unmarshal([]byte(val), &entry); err != nil {
		// 数据损坏：视为未命中，调用方会创建新 session
		return nil, false, nil
	}

	// 二次校验：即使 Redis 返回了 TTL 内的 entry，也校验 last_assigned_at
	// 防止时钟漂移或人为篡改
	if time.Since(entry.LastAssignedAt) > idx.ttl {
		return nil, false, nil
	}

	return &entry, true, nil
}

// Set 写入/覆盖 entry，TTL 自动重置为 LastSystemSessionTTL。
//
// 在 CreateV2 后调用，确保"系统赋值 session"被记录用于后续复用。
func (idx *LastSystemSessionIndex) Set(ctx context.Context, apiKeyID int, entry *LastSystemSessionEntry) error {
	if idx == nil || idx.client == nil || entry == nil {
		return nil
	}

	entry.LastAssignedAt = time.Now()
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}

	key := lastSystemSessionRedisKey(apiKeyID)
	return idx.client.Set(ctx, key, data, idx.ttl).Err()
}

// Touch 刷新现有 entry 的 TTL（不修改内容）。
//
// 用于"复用旧 entry 后延长窗口"的场景——复用成功意味着这仍是同一会话，
// 应当把 5 分钟窗口从这次复用开始重新计时。
func (idx *LastSystemSessionIndex) Touch(ctx context.Context, apiKeyID int) error {
	if idx == nil || idx.client == nil {
		return nil
	}
	key := lastSystemSessionRedisKey(apiKeyID)
	return idx.client.Expire(ctx, key, idx.ttl).Err()
}

// Delete 主动删除（用于异常路径清理）。
func (idx *LastSystemSessionIndex) Delete(ctx context.Context, apiKeyID int) error {
	if idx == nil || idx.client == nil {
		return nil
	}
	key := lastSystemSessionRedisKey(apiKeyID)
	return idx.client.Del(ctx, key).Err()
}