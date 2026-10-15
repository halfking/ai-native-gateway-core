package compression

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"
)

// SessionCacheScriptBackend is required for shared writes. A configured Redis
// backend without it fails explicitly; it never silently becomes a local cache.
type SessionCacheScriptBackend interface {
	Eval(context.Context, string, []string, ...any) (any, error)
}

const (
	sharedLeaseTTL            = 30 * time.Second
	sharedLeaseCleanupTimeout = 500 * time.Millisecond
	sharedBodyLimit           = 128 << 20 // streaming.maxBodySize; configurable for larger deployments
	sharedStateLimit          = 512 << 20 // Redis' default maximum bulk-string size, not a new small state cap
	sharedAcquireScript       = `-- session-cache-acquire
if type(redis.acl_check_cmd) ~= 'function' then return redis.error_reply('session cache requires Redis 7.0 acl_check_cmd') end
if not redis.acl_check_cmd('SET',KEYS[1],ARGV[1],'NX','PX',ARGV[2]) or not redis.acl_check_cmd('GET',KEYS[1]) or not redis.acl_check_cmd('DEL',KEYS[1]) then return redis.error_reply('NOPERM session cache lease commands denied') end
return redis.call('SET',KEYS[1],ARGV[1],'NX','PX',ARGV[2]) and 1 or 0`
	sharedReleaseScript = `-- session-cache-release
if redis.call('GET',KEYS[1]) == ARGV[1] then return redis.call('DEL',KEYS[1]) end
return 0`
	sharedCommitScript = `-- session-cache-commit
if type(redis.acl_check_cmd) ~= 'function' then return redis.error_reply('session cache requires Redis 7.0 acl_check_cmd') end
if redis.call('GET',KEYS[2]) ~= ARGV[1] then return 0 end
local kind=redis.call('TYPE',KEYS[1]).ok
if kind ~= 'none' and kind ~= 'hash' then return redis.error_reply('WRONGTYPE session cache requires hash') end
local ttl=tonumber(ARGV[2])
if not ttl or ttl <= 0 then return redis.error_reply('invalid session cache TTL') end
-- Lua runtime errors do not roll back prior writes. Check every mutation,
-- with its actual arguments, before the first destructive command.
if not redis.acl_check_cmd('DEL',KEYS[1]) or not redis.acl_check_cmd('HSET',KEYS[1],unpack(ARGV,3)) or not redis.acl_check_cmd('PEXPIRE',KEYS[1],ttl) then return redis.error_reply('NOPERM session cache commit commands denied') end
redis.call('DEL',KEYS[1])
redis.call('HSET',KEYS[1],unpack(ARGV,3))
redis.call('PEXPIRE',KEYS[1],ttl)
return 1`
)

var ErrSessionCacheLeaseLost = errors.New("session cache lease lost before commit")

func legacyRedisKey(tenantID, sessionID string) string {
	return "session:sc:" + tenantID + ":" + sessionID + ":v1"
}
func sharedLimit(env string, fallback int) (int, error) {
	raw := os.Getenv(env)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 || value > 512<<20 {
		return 0, fmt.Errorf("%s must be between 1 and 536870912 bytes", env)
	}
	return int(value), nil
}
func sharedToken() (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", fmt.Errorf("session cache token: %w", err)
	}
	return hex.EncodeToString(token[:]), nil
}
func (c *SessionCache) scriptBackend() (SessionCacheScriptBackend, error) {
	backend, ok := c.redis.(SessionCacheScriptBackend)
	if !ok {
		return nil, errors.New("session cache Redis backend lacks atomic script support")
	}
	return backend, nil
}

// withSharedLease invokes work once. It never retries a transform after lease
// loss. Cleanup uses its own bounded context even after caller cancellation;
// compare-token prevents deleting a successor's lease. Failed cleanup leaves
// only the expiring lease and never changes the primary result.
func (c *SessionCache) withSharedLease(ctx context.Context, tenantID, sessionID string, work func(string) error) (err error) {
	backend, err := c.scriptBackend()
	if err != nil {
		return err
	}
	token, err := sharedToken()
	if err != nil {
		return err
	}
	lockKey := redisKey(tenantID, sessionID) + ":lease"
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		var result any
		result, err = backend.Eval(ctx, sharedAcquireScript, []string{lockKey}, token, sharedLeaseTTL.Milliseconds())
		if err != nil {
			return fmt.Errorf("acquire session cache lease: %w", err)
		}
		if result == int64(1) {
			break
		}
		if result != int64(0) {
			return fmt.Errorf("invalid session cache acquire result: %T", result)
		}
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), sharedLeaseCleanupTimeout)
		defer cancel()
		_, releaseErr := backend.Eval(cleanup, sharedReleaseScript, []string{lockKey}, token)
		if releaseErr != nil {
			slog.WarnContext(cleanup, "session_cache: lease cleanup failed", "session", sessionID, "error", releaseErr, "primary_failed", err != nil)
		}
	}()
	if err = ctx.Err(); err != nil {
		return err
	}
	return work(token)
}
