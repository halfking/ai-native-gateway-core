package credential

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const rpmSlidingWindowLua = `
local key = KEYS[1]
local limit = tonumber(ARGV[1])
local now = tonumber(ARGV[2])
local window = tonumber(ARGV[3])
local cutoff = now - window
redis.call('ZREMRANGEBYSCORE', key, '-inf', cutoff)
local count = redis.call('ZCARD', key)
if count >= limit then
    return {0, count}
end
local member = string.format("%.6f:%s", now, redis.call('TIME')[2])
redis.call('ZADD', key, now, member)
redis.call('EXPIRE', key, window + 5)
return {1, count + 1}
`

const rpmRedisTimeout = 100 * time.Millisecond

// RedisRPMLimiter is the cross-process RPM implementation with memory fallback.
type RedisRPMLimiter struct {
	client   *redis.Client
	script   *redis.Script
	fallback *MemoryRPMLimiter
}

// NewRedisRPMLimiter creates a Redis-backed RPM limiter.
func NewRedisRPMLimiter(client *redis.Client) *RedisRPMLimiter {
	return &RedisRPMLimiter{client: client, script: redis.NewScript(rpmSlidingWindowLua), fallback: NewMemoryRPMLimiter()}
}

// NewRPMLimiterFromEnv selects Redis when RPM_REDIS_URL is configured.
func NewRPMLimiterFromEnv() RPMLimiter {
	url := strings.TrimSpace(os.Getenv("RPM_REDIS_URL"))
	if url == "" {
		recordRPMMode(false)
		return NewMemoryRPMLimiter()
	}
	options, err := redis.ParseURL(url)
	if err != nil {
		slog.Warn("parse rpm redis url failed, using memory limiter", "error", err)
		recordRPMMode(false)
		return NewMemoryRPMLimiter()
	}
	options.DialTimeout = 100 * time.Millisecond
	options.ReadTimeout = 100 * time.Millisecond
	options.WriteTimeout = 100 * time.Millisecond
	return NewRedisRPMLimiter(redis.NewClient(options))
}

func (r *RedisRPMLimiter) redisKey(providerID, credentialID int) string {
	return fmt.Sprintf("rpm:{%d:%d}", providerID, credentialID)
}

// CheckAndReserve atomically checks and reserves one Redis RPM slot.
func (r *RedisRPMLimiter) CheckAndReserve(ctx context.Context, providerID, credentialID int, limit int) (bool, int, error) {
	if limit <= 0 {
		return true, 0, nil
	}
	if err := ctx.Err(); err != nil {
		return false, 0, err
	}
	if r.client == nil {
		return r.fallback.CheckAndReserve(ctx, providerID, credentialID, limit)
	}

	redisCtx, cancel := context.WithTimeout(ctx, rpmRedisTimeout)
	defer cancel()
	now := float64(time.Now().UnixNano()) / float64(time.Second)
	result, err := r.script.Run(redisCtx, r.client, []string{r.redisKey(providerID, credentialID)}, limit, now, int(rpmWindowSeconds)).Slice()
	if err == nil && len(result) == 2 {
		allowed, okAllowed := redisInt(result[0])
		count, okCount := redisInt(result[1])
		if okAllowed && okCount {
			return allowed == 1, count, nil
		}
		err = fmt.Errorf("unexpected lua result")
	}
	if err == nil {
		err = fmt.Errorf("unexpected lua result length")
	}

	slog.Warn("redis rpm limiter failed, fallback to memory", "error", err, "provider_id", providerID, "credential_id", credentialID)
	if ctx.Err() != nil {
		return false, 0, ctx.Err()
	}
	return r.fallback.CheckAndReserve(ctx, providerID, credentialID, limit)
}

func redisInt(value any) (int, bool) {
	switch v := value.(type) {
	case int64:
		return int(v), true
	case int:
		return v, true
	case string:
		var parsed int
		_, err := fmt.Sscan(v, &parsed)
		return parsed, err == nil
	default:
		return 0, false
	}
}
