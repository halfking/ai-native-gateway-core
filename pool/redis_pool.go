// Package pool provides distributed connection pool management with Redis backing.
package pool

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	redisPoolTTL                = 5 * time.Minute
	redisPoolKeyPrefix          = "llmgw:pool:"
	redisPoolActiveKeyPrefix    = "llmgw:pool:act:"
	redisPoolFailKeySuffix      = ":fail"
	redisFailTTL                = 10 * time.Minute
	maxPoolRetries              = 3
	poolRetryBackoff            = 10 * time.Millisecond
)

// Lua scripts for atomic pool operations
var (
	// acquirePoolScript: atomically increment and check limit
	// Returns: 1 if acquired, 0 if limit exceeded
	acquirePoolScript = redis.NewScript(`
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

	// releasePoolScript: atomically decrement
	releasePoolScript = redis.NewScript(`
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

	// statsPoolScript: atomically get current count
	statsPoolScript = redis.NewScript(`
		local key = KEYS[1]
		local val = redis.call('GET', key)
		if not val then
			return 0
		end
		return tonumber(val)
	`)

	// recordFailScript: atomically increment failure count
	recordFailScript = redis.NewScript(`
		local key = KEYS[1]
		local ttl = tonumber(ARGV[1])
		
		local new_val = redis.call('INCR', key)
		redis.call('EXPIRE', key, ttl)
		
		return new_val
	`)

	// recordSuccessScript: atomically decrement failure count (min 0)
	recordSuccessScript = redis.NewScript(`
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
)

type RedisPoolManager struct {
	client *redis.Client
}

func NewRedisPoolManager(client *redis.Client) *RedisPoolManager {
	return &RedisPoolManager{client: client}
}

func (r *RedisPoolManager) poolKey(key PoolKey) string {
	return fmt.Sprintf("%s%s", redisPoolKeyPrefix, key.String())
}

func (r *RedisPoolManager) activeKey(key PoolKey) string {
	return fmt.Sprintf("%s%s", redisPoolActiveKeyPrefix, key.String())
}

func (r *RedisPoolManager) Enabled() bool {
	return r.client != nil
}

// Acquire atomically acquires connection pool slot with retry
func (r *RedisPoolManager) Acquire(ctx context.Context, key PoolKey, maxActive int) (bool, error) {
	if !r.Enabled() {
		return false, fmt.Errorf("redis not enabled")
	}

	activeKey := r.activeKey(key)

	for attempt := 0; attempt < maxPoolRetries; attempt++ {
		result, err := acquirePoolScript.Run(ctx, r.client, []string{activeKey}, maxActive, int(redisPoolTTL.Seconds())).Int()
		if err != nil {
			if attempt < maxPoolRetries-1 {
				select {
				case <-ctx.Done():
					return false, ctx.Err()
				case <-time.After(poolRetryBackoff * time.Duration(attempt+1)):
				}
				continue
			}
			return false, fmt.Errorf("redis acquire failed: %w", err)
		}

		if result == 1 {
			slog.Debug("redis pool acquired",
				"key", key.String(),
				"max", maxActive,
			)
			return true, nil
		}

		slog.Debug("redis pool saturated",
			"key", key.String(),
			"max", maxActive,
		)
		return false, nil
	}

	return false, fmt.Errorf("max retries exceeded")
}

// Release atomically releases connection pool slot
func (r *RedisPoolManager) Release(ctx context.Context, key PoolKey) error {
	if !r.Enabled() {
		return nil
	}

	activeKey := r.activeKey(key)
	_, err := releasePoolScript.Run(ctx, r.client, []string{activeKey}, int(redisPoolTTL.Seconds())).Result()
	if err != nil {
		return fmt.Errorf("redis release failed: %w", err)
	}
	return nil
}

// Stats returns current active connection count
func (r *RedisPoolManager) Stats(ctx context.Context, key PoolKey) (active int, err error) {
	if !r.Enabled() {
		return 0, nil
	}

	activeKey := r.activeKey(key)
	val, err := statsPoolScript.Run(ctx, r.client, []string{activeKey}).Int()
	if err == redis.Nil {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("redis stats failed: %w", err)
	}
	return val, nil
}

// RecordFailure atomically increments failure count
func (r *RedisPoolManager) RecordFailure(ctx context.Context, key PoolKey) error {
	if !r.Enabled() {
		return nil
	}

	failKey := r.poolKey(key) + redisPoolFailKeySuffix
	_, err := recordFailScript.Run(ctx, r.client, []string{failKey}, int(redisFailTTL.Seconds())).Result()
	return err
}

// RecordSuccess atomically decrements failure count
func (r *RedisPoolManager) RecordSuccess(ctx context.Context, key PoolKey) error {
	if !r.Enabled() {
		return nil
	}

	failKey := r.poolKey(key) + redisPoolFailKeySuffix
	_, err := recordSuccessScript.Run(ctx, r.client, []string{failKey}, int(redisFailTTL.Seconds())).Result()
	return err
}