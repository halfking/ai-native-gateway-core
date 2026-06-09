// Package limiter implements distributed concurrency control with Redis backing.
package limiter

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	redisIdentityTTL         = 10 * time.Minute
	redisIdentityKeyPrefix   = "llmgw:ident:"
	maxIdentityRetries       = 3
	identityRetryBackoff     = 10 * time.Millisecond
)

// Lua scripts for atomic operations
var (
	// acquireIdentityScript: atomically increment and check limit
	// Returns: 1 if acquired, 0 if limit exceeded, -1 on error
	acquireIdentityScript = redis.NewScript(`
		local key = KEYS[1]
		local limit = tonumber(ARGV[1])
		local ttl = tonumber(ARGV[2])
		
		local current = tonumber(redis.call('GET', key) or '0')
		if current >= limit then
			return 0
		end
		
		local new_val = redis.call('INCR', key)
		redis.call('EXPIRE', key, ttl)
		
		if new_val > limit then
			redis.call('DECR', key)
			return 0
		end
		
		return 1
	`)

	// releaseIdentityScript: atomically decrement
	releaseIdentityScript = redis.NewScript(`
		local key = KEYS[1]
		local ttl = tonumber(ARGV[1])
		
		local current = tonumber(redis.call('GET', key) or '0')
		if current <= 0 then
			return 0
		end
		
		local new_val = redis.call('DECR', key)
		redis.call('EXPIRE', key, ttl)
		
		return new_val
	`)

	// statsIdentityScript: atomically get current count
	statsIdentityScript = redis.NewScript(`
		local key = KEYS[1]
		local val = redis.call('GET', key)
		if not val then
			return 0
		end
		return tonumber(val)
	`)
)

type RedisIdentityLimiter struct {
	client *redis.Client
}

func NewRedisIdentityLimiter(client *redis.Client) *RedisIdentityLimiter {
	return &RedisIdentityLimiter{client: client}
}

func (r *RedisIdentityLimiter) identityKey(providerID, credentialID int, identityHash string) string {
	return fmt.Sprintf("%s%d/%d/%s", redisIdentityKeyPrefix, providerID, credentialID, identityHash)
}

// Acquire atomically acquires identity concurrency slot with retry
func (r *RedisIdentityLimiter) Acquire(ctx context.Context, providerID, credentialID int, identityHash string, limit int) (bool, error) {
	if !r.Enabled() {
		return false, fmt.Errorf("redis not enabled")
	}

	key := r.identityKey(providerID, credentialID, identityHash)

	for attempt := 0; attempt < maxIdentityRetries; attempt++ {
		result, err := acquireIdentityScript.Run(ctx, r.client, []string{key}, limit, int(redisIdentityTTL.Seconds())).Int()
		if err != nil {
			if attempt < maxIdentityRetries-1 {
				select {
				case <-ctx.Done():
					return false, ctx.Err()
				case <-time.After(identityRetryBackoff * time.Duration(attempt+1)):
				}
				continue
			}
			return false, fmt.Errorf("redis acquire failed: %w", err)
		}

		if result == 1 {
			slog.Debug("redis identity acquired",
				"provider", providerID,
				"credential", credentialID,
				"identity", truncateHash(identityHash),
			)
			return true, nil
		}

		slog.Debug("redis identity limit reached",
			"provider", providerID,
			"credential", credentialID,
			"identity", truncateHash(identityHash),
			"limit", limit,
		)
		return false, nil
	}

	return false, fmt.Errorf("max retries exceeded")
}

// Release atomically releases identity concurrency slot
func (r *RedisIdentityLimiter) Release(ctx context.Context, providerID, credentialID int, identityHash string) error {
	if !r.Enabled() {
		return nil
	}

	key := r.identityKey(providerID, credentialID, identityHash)
	_, err := releaseIdentityScript.Run(ctx, r.client, []string{key}, int(redisIdentityTTL.Seconds())).Result()
	if err != nil {
		return fmt.Errorf("redis release failed: %w", err)
	}
	return nil
}

// Stats returns current usage count
func (r *RedisIdentityLimiter) Stats(ctx context.Context, providerID, credentialID int, identityHash string) (used int, err error) {
	if !r.Enabled() {
		return 0, nil
	}

	key := r.identityKey(providerID, credentialID, identityHash)
	val, err := statsIdentityScript.Run(ctx, r.client, []string{key}).Int()
	if err == redis.Nil {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("redis stats failed: %w", err)
	}
	return val, nil
}

func (r *RedisIdentityLimiter) Enabled() bool {
	return r.client != nil
}

func truncateHash(hash string) string {
	if len(hash) > 8 {
		return hash[:8]
	}
	return hash
}

func parseHashLen(hash string, maxLen int) string {
	if len(hash) > maxLen {
		return hash[:maxLen]
	}
	return hash
}

func formatHashForLog(hash string) string {
	const maxDisplayLen = 16
	if len(hash) > maxDisplayLen {
		return hash[:maxDisplayLen] + "..."
	}
	return hash
}

func init() {
	// Suppress unused import warning
	_ = strconv.Itoa
}