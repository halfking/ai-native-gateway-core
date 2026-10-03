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
