package credential

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
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

// NewRPMLimiterFromEnv selects Redis from the RPM-specific setting or the
// gateway-wide Redis address used by existing deployments.
func NewRPMLimiterFromEnv() RPMLimiter {
	url := strings.TrimSpace(os.Getenv("RPM_REDIS_URL"))
	dbFromGateway := 0
	if url == "" {
		// 2026-08-04: 与主网关共享 db 选择（默认 2，与 LLM_GATEWAY_REDIS_DB 一致）。
		// 防止 rpm_redis 错把限流 key 写到 db=0（与 PMS session 混在一起）。
		// 优先读 RPM_REDIS_URL（运维已可显式指定 db 段）；回退到 LLM_GATEWAY_REDIS_DB。
		if raw := strings.TrimSpace(os.Getenv("LLM_GATEWAY_REDIS_DB")); raw != "" {
			if v, err := strconv.Atoi(raw); err == nil {
				dbFromGateway = v
			}
		} else {
			dbFromGateway = 2 // 2026-08-04 默认值，与 config.go 的 cfg.RedisDB 一致
		}
		url = redisURLFromAddress(os.Getenv("LLM_GATEWAY_REDIS_ADDR"), dbFromGateway)
	}
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

// redisURLFromAddress converts a bare "host:port" into a redis:// URL,
// optionally appending a "/db" path segment so the resulting client points
// at the same Redis DB as the main gateway (cfg.RedisDB /
// LLM_GATEWAY_REDIS_DB). If address already contains a scheme, db is
// appended to the existing path; if it already contains a /db, the value
// is left untouched (explicit URLs always win).
func redisURLFromAddress(address string, db int) string {
	address = strings.TrimSpace(address)
	if address == "" {
		return ""
	}
	if !strings.Contains(address, "://") {
		address = "redis://" + address
	}
	if db <= 0 {
		return address
	}
	// Find the path component (after the host's optional /auth@ segment).
	// redis URL grammar: redis://[user:pass@]host:port[/db]
	// We only append /db if the path is empty or missing.
	slash := strings.Index(address, "://")
	rest := address[slash+3:]
	// rest is [user:pass@]host[:port][/db]
	// Find the first "/" after the authority.
	pathIdx := strings.Index(rest, "/")
	if pathIdx < 0 {
		return address + "/" + strconv.Itoa(db)
	}
	if pathIdx == len(rest)-1 {
		return address + strconv.Itoa(db)
	}
	// Existing db segment — leave it alone (explicit URL wins).
	return address
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
