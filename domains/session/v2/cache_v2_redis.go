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
	"fmt"
	"strconv"
	"time"

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
//
// Returns:
//   - *RedisGovernanceCache: nil client = disabled cache (fail-open)
func NewRedisGovernanceCache(redisAddr string, ttl time.Duration) *RedisGovernanceCache {
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
		DB:           0,
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

	// Use HGETALL to retrieve all fields
	data, err := r.client.HGetAll(ctx, key).Result()
	if err == redis.Nil || len(data) == 0 {
		return nil, nil // Cache miss (not an error)
	}
	if err != nil {
		// Redis error, log and return nil (fail-open)
		return nil, fmt.Errorf("redis hgetall: %w", err)
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
