// redis_sliding.go — Redis-backed sliding window rate limiter.
//
// Uses a single Lua script executed atomically via EVALSHA so that
// "check + record" is a single Redis round-trip with no race conditions.
//
// Fallback: if Redis is unavailable the call transparently delegates to the
// in-process SlidingWindowLimiter so the gateway keeps running.
package ratelimit

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// ----------------------------------------------------------------------------
// Lua script
// ----------------------------------------------------------------------------

// rpmLua is an atomic sliding-window RPM check-and-record script.
//
// KEYS[1] = rate-limit bucket key, e.g. "rl:rpm:<keyID>"
// ARGV[1] = current Unix timestamp (ms), as string
// ARGV[2] = window size in ms (60000)
// ARGV[3] = RPM limit
//
// Returns: "1" if allowed, "0" if denied.
const rpmLua = `
local key      = KEYS[1]
local now      = tonumber(ARGV[1])
local window   = tonumber(ARGV[2])
local limit    = tonumber(ARGV[3])
local cutoff   = now - window

-- Remove entries older than the window
redis.call('ZREMRANGEBYSCORE', key, '-inf', cutoff)

-- Count current entries
local count = redis.call('ZCARD', key)
if count >= limit then
  return 0
end

-- Add current timestamp as both score and member (unique via suffix)
redis.call('ZADD', key, now, now .. ':' .. count)
-- Keep key alive for one full window
redis.call('PEXPIRE', key, window + 1000)
return 1
`

// tpmLua is the TPM variant — scores are token counts, values include
// a timestamp so we can expire them, but we use a sorted set keyed by
// insertion time and store token count in the member value.
const tpmLua = `
local key    = KEYS[1]
local now    = tonumber(ARGV[1])
local window = tonumber(ARGV[2])
local limit  = tonumber(ARGV[3])
local tokens = tonumber(ARGV[4])
local cutoff = now - window

redis.call('ZREMRANGEBYSCORE', key, '-inf', cutoff)

-- Sum current token usage (members encode "timestamp:tokens")
local members = redis.call('ZRANGE', key, 0, -1)
local used = 0
for _, m in ipairs(members) do
  local t = tonumber(string.match(m, ':(%d+)$')) or 0
  used = used + t
end
if used + tokens > limit then
  return 0
end

redis.call('ZADD', key, now, now .. ':' .. tokens)
redis.call('PEXPIRE', key, window + 1000)
return 1
`

// ----------------------------------------------------------------------------
// RedisLimiter
// ----------------------------------------------------------------------------

// RedisLimiter wraps a Redis client and a fallback in-memory limiter.
// It also caches the loaded Lua SHA to avoid re-loading on every call.
type RedisLimiter struct {
	client    *redis.Client
	fallback  *SlidingWindowLimiter
	rpmSHA    string
	tpmSHA    string
	shaOnce   sync.Once
	mu        sync.Mutex
	unhealthy bool // Redis circuit-open flag
}

// NewRedisLimiter creates a limiter backed by the given Redis client.
// If rdb is nil, the limiter falls back to pure in-memory operation.
func NewRedisLimiter(rdb *redis.Client) *RedisLimiter {
	return &RedisLimiter{
		client:   rdb,
		fallback: NewSlidingWindowLimiter(),
	}
}

// NewRedisLimiterFromEnv creates a limiter using the RATE_LIMIT_REDIS_URL
// environment variable (e.g. "redis://localhost:6379/0").  Falls back to
// in-memory if the variable is empty.
func NewRedisLimiterFromEnv() *RedisLimiter {
	url := strings.TrimSpace(os.Getenv("RATE_LIMIT_REDIS_URL"))
	if url == "" {
		url = strings.TrimSpace(os.Getenv("REDIS_URL"))
	}
	if url == "" {
		slog.Warn("RATE_LIMIT_REDIS_URL not set, using in-process rate limiter")
		return NewRedisLimiter(nil)
	}
	opt, err := redis.ParseURL(url)
	if err != nil {
		slog.Error("invalid RATE_LIMIT_REDIS_URL, falling back to in-process", "error", err)
		return NewRedisLimiter(nil)
	}
	rdb := redis.NewClient(opt)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		slog.Warn("Redis ping failed, rate limiter will use in-memory fallback until Redis recovers", "error", err)
	}
	return NewRedisLimiter(rdb)
}

// loadScripts loads both Lua scripts once and caches their SHA.
func (l *RedisLimiter) loadScripts(ctx context.Context) error {
	var loadErr error
	l.shaOnce.Do(func() {
		rpmSHA, err := l.client.ScriptLoad(ctx, rpmLua).Result()
		if err != nil {
			loadErr = fmt.Errorf("load rpm script: %w", err)
			return
		}
		tpmSHA, err := l.client.ScriptLoad(ctx, tpmLua).Result()
		if err != nil {
			loadErr = fmt.Errorf("load tpm script: %w", err)
			return
		}
		l.rpmSHA = rpmSHA
		l.tpmSHA = tpmSHA
	})
	return loadErr
}

// redisKey returns the Redis key for a given metric (rpm/tpm) and API key ID.
func redisKey(metric string, keyID int) string {
	return fmt.Sprintf("rl:%s:%d", metric, keyID)
}

// rpmSHAFallback computes the expected SHA without loading (for re-use after NOSCRIPT).
func rpmSHAFallback() string {
	h := sha1.Sum([]byte(rpmLua))
	return hex.EncodeToString(h[:])
}

// evalBool runs EVALSHA and retries once with EVAL on NOSCRIPT.
func (l *RedisLimiter) evalBool(ctx context.Context, sha, script string, keys []string, args ...any) (bool, error) {
	val, err := l.client.EvalSha(ctx, sha, keys, args...).Int()
	if err != nil {
		if isNoScript(err) {
			// Script evicted from Redis cache — reload
			val, err = l.client.Eval(ctx, script, keys, args...).Int()
			if err != nil {
				return false, err
			}
			// Reload for future calls
			go func() {
				reloadCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = l.loadScripts(reloadCtx)
			}()
		} else {
			return false, err
		}
	}
	return val == 1, nil
}

func isNoScript(err error) bool {
	return err != nil && strings.Contains(err.Error(), "NOSCRIPT")
}

// isRedisAvailable checks the circuit flag.
func (l *RedisLimiter) isRedisAvailable() bool {
	if l.client == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return !l.unhealthy
}

// markUnhealthy trips the circuit; background goroutine will try to recover.
func (l *RedisLimiter) markUnhealthy(err error) {
	slog.Warn("rate-limit Redis error, switching to in-memory fallback", "error", err)
	l.mu.Lock()
	wasHealthy := !l.unhealthy
	l.unhealthy = true
	l.mu.Unlock()

	if wasHealthy {
		go l.waitForRecovery()
	}
}

func (l *RedisLimiter) waitForRecovery() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := l.client.Ping(ctx).Err()
		cancel()
		if err == nil {
			l.mu.Lock()
			l.unhealthy = false
			l.mu.Unlock()
			// Reset shaOnce so scripts are reloaded
			l.shaOnce = sync.Once{}
			slog.Info("rate-limit Redis recovered")
			return
		}
	}
}

// ----------------------------------------------------------------------------
// Public API — mirrors SlidingWindowLimiter
// ----------------------------------------------------------------------------

// CheckRPM returns true if the request is within the RPM limit.
// Internal callers (IsInternal=true) are always allowed through.
func (l *RedisLimiter) CheckRPM(keyID int, limit int) bool {
	return l.CheckRPMCtx(context.Background(), keyID, limit)
}

func (l *RedisLimiter) CheckRPMCtx(ctx context.Context, keyID int, limit int) bool {
	if limit <= 0 {
		return true
	}
	if !l.isRedisAvailable() {
		return l.fallback.CheckRPM(keyID, limit)
	}

	tctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()

	if err := l.loadScripts(tctx); err != nil {
		l.markUnhealthy(err)
		return l.fallback.CheckRPM(keyID, limit)
	}

	nowMS := strconv.FormatInt(time.Now().UnixMilli(), 10)
	allowed, err := l.evalBool(tctx, l.rpmSHA, rpmLua,
		[]string{redisKey("rpm", keyID)},
		nowMS, "60000", strconv.Itoa(limit),
	)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || isRedisNetErr(err) {
			l.markUnhealthy(err)
		}
		return l.fallback.CheckRPM(keyID, limit)
	}
	return allowed
}

// CheckTPM returns true if the request is within the TPM limit.
func (l *RedisLimiter) CheckTPM(keyID int, estimatedTokens int, limit int) bool {
	if limit <= 0 {
		return true
	}
	if !l.isRedisAvailable() {
		return l.fallback.CheckTPM(keyID, estimatedTokens, limit)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	if err := l.loadScripts(ctx); err != nil {
		l.markUnhealthy(err)
		return l.fallback.CheckTPM(keyID, estimatedTokens, limit)
	}

	nowMS := strconv.FormatInt(time.Now().UnixMilli(), 10)
	allowed, err := l.evalBool(ctx, l.tpmSHA, tpmLua,
		[]string{redisKey("tpm", keyID)},
		nowMS, "60000", strconv.Itoa(limit), strconv.Itoa(estimatedTokens),
	)
	if err != nil {
		if isRedisNetErr(err) {
			l.markUnhealthy(err)
		}
		return l.fallback.CheckTPM(keyID, estimatedTokens, limit)
	}
	return allowed
}

// RPMStatus returns (used, remaining) for a key without modifying state.
// Note: this is a read-only path; it does not record a request.
func (l *RedisLimiter) RPMStatus(keyID int, limit int) (used int, remaining int) {
	if limit <= 0 {
		return 0, -1
	}
	if !l.isRedisAvailable() {
		return l.fallback.RPMStatus(keyID, limit)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	nowMS := time.Now().UnixMilli()
	cutoff := float64(nowMS - 60000)
	count, err := l.client.ZCount(ctx, redisKey("rpm", keyID),
		strconv.FormatFloat(cutoff, 'f', 0, 64), "+inf").Result()
	if err != nil {
		return l.fallback.RPMStatus(keyID, limit)
	}
	used = int(count)
	remaining = limit - used
	if remaining < 0 {
		remaining = 0
	}
	return
}

func isRedisNetErr(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "connection refused") ||
		strings.Contains(s, "EOF") ||
		strings.Contains(s, "i/o timeout") ||
		errors.Is(err, context.DeadlineExceeded)
}
