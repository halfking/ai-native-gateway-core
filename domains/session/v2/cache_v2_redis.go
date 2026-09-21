// Package v2: Redis L2 governance cache implementation
//
// 职责：
//   1. 在 Redis 中缓存治理元数据（verdicts, audit results）
//   2. 使用 Hash 结构存储，key 格式：session:v2:{tenantID}:{sessionID}
//   3. 30分钟 TTL，可配置
//
// 设计参考：docs/会话优化v2/44-V2压缩层集成实施方案.md §2.2

package v2

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	redissafe "github.com/kaixuan/llm-gateway-go/internal/redis"
	"github.com/redis/go-redis/v9"
)

const (
	// Default TTL for Redis governance cache
	defaultGovernanceTTL = 30 * time.Minute

	// Redis key prefix for V2 governance cache
	redisKeyPrefixV2 = "session:v2"
)

// RedisGovernanceCache implements L2 Redis-based governance cache
type RedisGovernanceCache struct {
	client  *redis.Client
	ttl     time.Duration
	enabled bool
}

// NewRedisGovernanceCache creates a new Redis governance cache
//
// Parameters:
//   - redisAddr: Redis server address (e.g., "localhost:6379")
//   - ttl: Cache TTL (0 = use default 30min)
//   - redisDB: Redis logical database index (e.g., 2 for llmgw main).
//     2026-08-25: 之前硬编码 0, session:v2 governance cache 污染了 PMS 共享的
//     db0, 违反 252 pms-redis 多租户隔离原则. 强制要求调用方传入 cfg.RedisDB.
//
// Returns:
//   - *RedisGovernanceCache: nil client = disabled cache (fail-open)
func NewRedisGovernanceCache(redisAddr string, ttl time.Duration, redisDB int) *RedisGovernanceCache {
	if ttl == 0 {
		ttl = defaultGovernanceTTL
	}

	if redisAddr == "" {
		// Disabled cache (fail-open)
		return &RedisGovernanceCache{
			client:  nil,
			ttl:     ttl,
			enabled: false,
		}
	}

	client := redis.NewClient(&redis.Options{
		Addr:         redisAddr,
		DB:           redisDB,
		DialTimeout:  2 * time.Second,
		ReadTimeout:  1 * time.Second,
		WriteTimeout: 1 * time.Second,
		PoolSize:     10,
		MinIdleConns: 2,
	})

	// Test connection (non-blocking, fail-open if Redis unavailable)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		// Redis unavailable, return disabled cache (fail-open)
		return &RedisGovernanceCache{
			client:  nil,
			ttl:     ttl,
			enabled: false,
		}
	}

	return &RedisGovernanceCache{
		client:  client,
		ttl:     ttl,
		enabled: true,
	}
}

// Get retrieves governance metadata from Redis
//
// Returns:
//   - *GovernanceMeta: metadata if found
//   - nil: cache miss (not an error)
//   - error: only for unexpected errors (caller should log and continue)
func (r *RedisGovernanceCache) Get(ctx context.Context, tenantID, sessionID string) (*GovernanceMeta, error) {
	if !r.enabled || r.client == nil {
		return nil, nil // Disabled cache, return nil (not an error)
	}

	key := redisKeyV2(tenantID, sessionID)

	// audit-24h-20260828-r4 P2: Use SafeHGetAll to prevent WRONGTYPE errors
	// when the manifest key collides with a non-hash Redis type (some admin
	// tools SET the same key during live debugging). SafeHGetAll performs
	// a TYPE guard before HGETALL and surfaces a TypedError on type mismatch.
	// Both ErrKeyNotFound (key absent) and empty map (key present but empty)
	// are valid cache-miss signals — preserved from the original code path.
	data, err := redissafe.SafeHGetAll(ctx, r.client, key)
	if err != nil {
		if errors.Is(err, redissafe.ErrKeyNotFound) {
			return nil, nil // Cache miss (not an error)
		}
		// TypedError (WRONGTYPE) or genuine network error — log and
		// return nil (fail-open semantics preserved from the original).
		return nil, fmt.Errorf("redis safe hgetall: %w", err)
	}
	if len(data) == 0 {
		return nil, nil // Cache miss (not an error)
	}

	// Parse fields into GovernanceMeta
	meta := &GovernanceMeta{
		LastInjectionVerdict: data["injection_verdict"],
		LastOutputVerdict:    data["output_verdict"],
		SensitiveDetected:    data["sensitive"] == "true",
	}

	// Parse timestamp
	if auditedAtStr := data["audited_at"]; auditedAtStr != "" {
		if ts, err := strconv.ParseInt(auditedAtStr, 10, 64); err == nil {
			meta.AuditedAt = time.Unix(ts, 0)
		}
	}

	return meta, nil
}

// Set stores governance metadata in Redis
//
// Returns:
//   - error: only for unexpected errors (caller should log and continue)
func (r *RedisGovernanceCache) Set(ctx context.Context, tenantID, sessionID string, meta *GovernanceMeta) error {
	if !r.enabled || r.client == nil {
		return nil // Disabled cache, no-op
	}

	if meta == nil {
		return nil // Nothing to store
	}

	key := redisKeyV2(tenantID, sessionID)

	// Convert GovernanceMeta to map[string]interface{}
	data := map[string]interface{}{
		"injection_verdict": meta.LastInjectionVerdict,
		"output_verdict":    meta.LastOutputVerdict,
		"audited_at":        meta.AuditedAt.Unix(),
		"sensitive":         strconv.FormatBool(meta.SensitiveDetected),
	}

	// Use pipeline for atomicity
	pipe := r.client.Pipeline()
	pipe.HSet(ctx, key, data)
	pipe.Expire(ctx, key, r.ttl)

	_, err := pipe.Exec(ctx)
	if err != nil {
		// Redis error, log and return (fail-open)
		return fmt.Errorf("redis pipeline: %w", err)
	}

	return nil
}

// Delete removes governance metadata from Redis
//
// Returns:
//   - error: only for unexpected errors (caller should log and continue)
func (r *RedisGovernanceCache) Delete(ctx context.Context, tenantID, sessionID string) error {
	if !r.enabled || r.client == nil {
		return nil // Disabled cache, no-op
	}

	key := redisKeyV2(tenantID, sessionID)

	err := r.client.Del(ctx, key).Err()
	if err == redis.Nil {
		return nil // Key doesn't exist (not an error)
	}
	if err != nil {
		return fmt.Errorf("redis del: %w", err)
	}

	return nil
}

// Close closes the Redis connection
func (r *RedisGovernanceCache) Close() error {
	if r.client != nil {
		return r.client.Close()
	}
	return nil
}

// redisKeyV2 generates Redis key for V2 governance cache
//
// Format: session:v2:{tenantID}:{sessionID}
// Example: session:v2:default:gw_abc123
func redisKeyV2(tenantID, sessionID string) string {
	return fmt.Sprintf("%s:%s:%s", redisKeyPrefixV2, tenantID, sessionID)
}
