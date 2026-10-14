//go:build integration

package v2

import (
	"context"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStorageAuditGovernanceRealRedis(t *testing.T) {
	addr := os.Getenv("SESSION_AUDIT_REDIS_ADDR")
	if addr == "" {
		t.Skip("set SESSION_AUDIT_REDIS_ADDR to disposable loopback Redis")
	}
	host, _, err := net.SplitHostPort(addr)
	require.NoError(t, err)
	ip := net.ParseIP(host)
	require.True(t, ip != nil && ip.IsLoopback())
	a, b := NewRedisGovernanceCache(addr, time.Minute, 0), NewRedisGovernanceCache(addr, time.Minute, 0)
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	require.True(t, a.enabled)
	require.True(t, b.enabled)
	ctx := context.Background()
	tenant, session := "audit-v2", fmt.Sprintf("audit-%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = a.Delete(ctx, tenant, session) })
	require.NoError(t, a.Set(ctx, tenant, session, &GovernanceMeta{LastInjectionVerdict: "block", LastOutputVerdict: "warn", SensitiveDetected: true, AuditedAt: time.Now()}))
	require.NoError(t, a.Set(ctx, tenant, session, &GovernanceMeta{}))
	got, err := b.Get(ctx, tenant, session)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.False(t, got.SensitiveDetected)
	require.Empty(t, got.LastInjectionVerdict)
	require.Empty(t, got.LastOutputVerdict)
	require.True(t, got.AuditedAt.IsZero())
	// Wrong type is an error, never fabricated successful governance metadata.
	require.NoError(t, a.client.Del(ctx, redisKeyV2(tenant, session)).Err())
	require.NoError(t, a.client.Set(ctx, redisKeyV2(tenant, session), "wrong-type", time.Minute).Err())
	got, err = b.Get(ctx, tenant, session)
	require.Error(t, err)
	require.Nil(t, got)
	t.Log("two Redis clients: zero-value replacement and WRONGTYPE checked")
}

// TestStorageAuditGovernanceSetGetDeleteMiss covers the RedisGovernanceCache
// CRUD surface against a real (disposable) Redis: miss on a never-written key,
// non-empty meta roundtrip (audited_at is stored as unix seconds — compare
// instants, not time.Time structs), delete, and miss again.
//
// Migrated 2026-10-14 from two unconditionally-skipped unit tests
// (TestRedisGovernanceCache_SetGet / _CacheMiss) that never executed anywhere:
// the skip sat above a live localhost:6379 client, so enabling them would have
// hit whatever Redis happened to run on the developer machine instead.
func TestStorageAuditGovernanceSetGetDeleteMiss(t *testing.T) {
	addr := os.Getenv("SESSION_AUDIT_REDIS_ADDR")
	if addr == "" {
		t.Skip("set SESSION_AUDIT_REDIS_ADDR to disposable loopback Redis")
	}
	host, _, err := net.SplitHostPort(addr)
	require.NoError(t, err)
	ip := net.ParseIP(host)
	require.True(t, ip != nil && ip.IsLoopback())

	cache := NewRedisGovernanceCache(addr, time.Minute, 0)
	t.Cleanup(func() { _ = cache.Close() })
	require.True(t, cache.enabled)

	ctx := context.Background()
	tenant, session := "audit-v2", fmt.Sprintf("audit-%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = cache.Delete(ctx, tenant, session) })

	miss, err := cache.Get(ctx, tenant, session)
	require.NoError(t, err)
	require.Nil(t, miss, "never-written key must be a silent miss")

	meta := &GovernanceMeta{
		LastInjectionVerdict: "pass",
		LastOutputVerdict:    "warn",
		AuditedAt:            time.Now().Truncate(time.Second),
		SensitiveDetected:    true,
	}
	require.NoError(t, cache.Set(ctx, tenant, session, meta))

	got, err := cache.Get(ctx, tenant, session)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, meta.LastInjectionVerdict, got.LastInjectionVerdict)
	require.Equal(t, meta.LastOutputVerdict, got.LastOutputVerdict)
	require.True(t, got.SensitiveDetected)
	require.Equal(t, meta.AuditedAt.Unix(), got.AuditedAt.Unix())

	require.NoError(t, cache.Delete(ctx, tenant, session))
	after, err := cache.Get(ctx, tenant, session)
	require.NoError(t, err)
	require.Nil(t, after, "deleted key must read back as a miss")
}
