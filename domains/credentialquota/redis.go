package credentialquota

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	quotaKeyPrefix = "llmgw:cred_client_quota:"
	quotaKeySuffix = ":concurrent"
)

func concurrentKey(credentialID int64, clientType string) string {
	return fmt.Sprintf("%s%d:%s%s", quotaKeyPrefix, credentialID,
		normalizeClientType(clientType), quotaKeySuffix)
}

// acquireConcurrentScript atomically cleans up expired leases, applies the
// limit, and writes the new lease. The score is the absolute lease expiry
// derived from Redis server time so that wall-clock skew does not break
// renewals across processes.
var acquireConcurrentScript = redis.NewScript(`
local key = KEYS[1]
local leaseID = ARGV[1]
local limit = tonumber(ARGV[2])
local now = tonumber(redis.call('TIME')[1])
local ttl = tonumber(ARGV[3])
local expires = now + ttl

redis.call('ZREMRANGEBYSCORE', key, '-inf', now)
local count = redis.call('ZCARD', key)
if limit > 0 and count >= limit then
  return {0, now}
end
local added = redis.call('ZADD', key, 'NX', expires, leaseID)
if added == false then
  return {0, now}
end
return {1, expires}
`)

// renewConcurrentScript extends the existing lease score to a new absolute
// expiry. Renewing a missing lease returns the missing status without
// re-creating one.
var renewConcurrentScript = redis.NewScript(`
local key = KEYS[1]
local leaseID = ARGV[1]
local now = tonumber(redis.call('TIME')[1])
local ttl = tonumber(ARGV[2])
local expires = now + ttl

local score = redis.call('ZSCORE', key, leaseID)
if not score then
  return {0, now}
end
redis.call('ZADD', key, expires, leaseID)
return {1, expires}
`)

// releaseConcurrentScript removes the lease by id, returning the prior
// membership. Calling release on a missing lease is a no-op.
var releaseConcurrentScript = redis.NewScript(`
local key = KEYS[1]
local leaseID = ARGV[1]
local removed = redis.call('ZREM', key, leaseID)
if removed == 0 then
  return 0
end
local remaining = redis.call('ZCARD', key)
if remaining == 0 then
  redis.call('DEL', key)
  return 0
end
return remaining
`)

// countConcurrentScript returns the active lease count for a key after
// pruning expired members.
var countConcurrentScript = redis.NewScript(`
local key = KEYS[1]
local now = tonumber(redis.call('TIME')[1])
redis.call('ZREMRANGEBYSCORE', key, '-inf', now)
return redis.call('ZCARD', key)
`)

// clearConcurrentScript removes all entries for one key, used by Reset.
var clearConcurrentScript = redis.NewScript(`
local key = KEYS[1]
return redis.call('DEL', key)
`)

func errIsScript(ctx context.Context, err error) error {
	if err == nil || err == redis.Nil {
		return nil
	}
	if strings.Contains(err.Error(), "NOSCRIPT") {
		return nil
	}
	return err
}

func quotaRedisKey(credentialID int64, clientType string) string {
	return concurrentKey(credentialID, clientType)
}

func mustAcquireScript(r redis.UniversalClient) *redis.Script {
	return acquireConcurrentScript
}

// acquireLease is the shared Redis primitive used by Service. The script
// result is [code, expiry].
func acquireLease(ctx context.Context, r redis.UniversalClient, key, leaseID string, limit int, ttl time.Duration) (acquired bool, expiry time.Time, err error) {
	res, err := acquireConcurrentScript.Run(ctx, r, []string{key},
		leaseID, limit, int(ttl.Seconds())).Result()
	if err != nil {
		return false, time.Time{}, fmt.Errorf("%w: %v", ErrRedis, err)
	}
	parts, ok := res.([]interface{})
	if !ok || len(parts) < 2 {
		return false, time.Time{}, fmt.Errorf("%w: malformed acquire result", ErrRedis)
	}
	code, _ := parts[0].(int64)
	exp, _ := parts[1].(int64)
	if code != 1 {
		return false, time.Time{}, nil
	}
	return true, time.Unix(exp, 0), nil
}

func renewLease(ctx context.Context, r redis.UniversalClient, key, leaseID string, ttl time.Duration) (Lease, error) {
	res, err := renewConcurrentScript.Run(ctx, r, []string{key},
		leaseID, int(ttl.Seconds())).Result()
	if err != nil {
		return Lease{}, fmt.Errorf("%w: %v", ErrRedis, err)
	}
	parts, ok := res.([]interface{})
	if !ok || len(parts) < 2 {
		return Lease{}, fmt.Errorf("%w: malformed renew result", ErrRedis)
	}
	code, _ := parts[0].(int64)
	if code != 1 {
		return Lease{}, ErrLeaseNotFound
	}
	exp, _ := parts[1].(int64)
	return Lease{ID: leaseID, ExpiresAt: time.Unix(exp, 0)}, nil
}

func releaseLease(ctx context.Context, r redis.UniversalClient, key, leaseID string) error {
	_, err := releaseConcurrentScript.Run(ctx, r, []string{key}, leaseID).Result()
	if err == redis.Nil {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRedis, err)
	}
	return nil
}

func countActive(ctx context.Context, r redis.UniversalClient, key string) (int64, error) {
	res, err := countConcurrentScript.Run(ctx, r, []string{key}).Result()
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrRedis, err)
	}
	v, _ := res.(int64)
	return v, nil
}

func resetLeaseKey(ctx context.Context, r redis.UniversalClient, key string) error {
	_, err := clearConcurrentScript.Run(ctx, r, []string{key}).Result()
	if err == redis.Nil {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRedis, err)
	}
	return nil
}

var _ = mustAcquireScript // silence unused in non-test builds
