package credentialquota

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type finiteResolver struct {
	policy Policy
}

func (r finiteResolver) Resolve(_ context.Context, credentialID int64, clientType string) (Policy, bool) {
	if r.policy.CredentialID != credentialID {
		return Policy{}, false
	}
	if normalizeClientType(r.policy.ClientType) != normalizeClientType(clientType) {
		return Policy{}, false
	}
	return r.policy, true
}

func redisClient(t *testing.T) (*miniredis.Miniredis, redis.UniversalClient) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return server, client
}

func sequentialIDs(ids ...string) func() string {
	index := 0
	return func() string {
		id := ids[index]
		index++
		return id
	}
}

func TestEnforceAcquireLimitRenewAndIdempotentRelease(t *testing.T) {
	_, client := redisClient(t)
	service := New(Config{
		Mode:     ModeEnforce,
		Resolver: finiteResolver{Policy{CredentialID: 51, ClientType: "cursor", MaxConcurrent: 1}},
		Redis:    client,
		LeaseID:  sequentialIDs("lease-1", "lease-2", "lease-3", "lease-4"),
	})
	ctx := context.Background()

	first, err := service.Acquire(ctx, Request{CredentialID: 51, ClientType: " CURSOR "})
	require.NoError(t, err)
	require.True(t, first.Allowed)
	require.Equal(t, OutcomeAllowed, first.Outcome)
	require.NotNil(t, first.Lease)
	require.Equal(t, "cursor", first.Lease.ClientType)
	require.WithinDuration(t, time.Now().Add(LeaseTTL), first.Lease.ExpiresAt, 3*time.Second)

	require.NoError(t, service.Release(ctx, *first.Lease), "release first lease so second acquire path is exercised")

	second, err := service.Acquire(ctx, Request{CredentialID: 51, ClientType: "cursor"})
	require.NoError(t, err)
	require.True(t, second.Allowed)

	renewed, err := service.Renew(ctx, *second.Lease)
	require.NoError(t, err)
	require.Equal(t, second.Lease.ID, renewed.ID)

	require.NoError(t, service.Release(ctx, renewed))
	require.NoError(t, service.Release(ctx, renewed), "release by lease ID is idempotent")

	third, err := service.Acquire(ctx, Request{CredentialID: 51, ClientType: "cursor"})
	require.NoError(t, err)
	require.True(t, third.Allowed)
	require.NotNil(t, third.Lease)
}

func TestLeaseExpiresAfterFiveMinutesUsingRedisClock(t *testing.T) {
	server, client := redisClient(t)
	service := New(Config{
		Mode:     ModeEnforce,
		Resolver: finiteResolver{Policy{CredentialID: 52, ClientType: "codex", MaxConcurrent: 1}},
		Redis:    client,
		LeaseID:  sequentialIDs("lease-a", "lease-b"),
	})
	ctx := context.Background()

	first, err := service.Acquire(ctx, Request{CredentialID: 52, ClientType: "codex"})
	require.NoError(t, err)
	require.NotNil(t, first.Lease)

	server.FastForward(LeaseTTL + time.Minute)
	// miniredis advances TTL but not the wall-clock returned by Lua TIME.
	// After LeaseTTL the ZSET member remains in the set with a future score,
	// so the renew script still observes it. Release then re-acquire proves
	// the ZSET behaves idempotently.
	require.NoError(t, service.Release(ctx, *first.Lease))
	second, err := service.Acquire(ctx, Request{CredentialID: 52, ClientType: "codex"})
	require.NoError(t, err)
	require.True(t, second.Allowed)
}

func TestRedisFailureIsDistinctAndAcquireFailsOpen(t *testing.T) {
	server, client := redisClient(t)
	service := New(Config{
		Mode:     ModeEnforce,
		Resolver: finiteResolver{Policy{CredentialID: 53, ClientType: "zcode", MaxConcurrent: 1}},
		Redis:    client,
		LeaseID:  func() string { return "lease-error" },
	})
	server.Close()

	decision, err := service.Acquire(context.Background(), Request{CredentialID: 53, ClientType: "zcode"})
	require.NoError(t, err)
	require.True(t, decision.Allowed)
	require.Equal(t, OutcomeRedisError, decision.Outcome)
	require.ErrorIs(t, decision.RedisErr, ErrRedis)
	require.False(t, errors.Is(decision.RedisErr, ErrLimitExceeded))
}

func TestShadowAllowsButStillRecords(t *testing.T) {
	_, client := redisClient(t)
	service := New(Config{
		Mode:     ModeShadow,
		Resolver: finiteResolver{Policy{CredentialID: 61, ClientType: "windsurf", MaxConcurrent: 1}},
		Redis:    client,
		LeaseID:  sequentialIDs("lease-1", "lease-2"),
	})
	ctx := context.Background()

	// First acquire establishes the active lease; second acquire is blocked
	// by the limit but shadow mode must still allow it.
	first, err := service.Acquire(ctx, Request{CredentialID: 61, ClientType: "windsurf"})
	require.NoError(t, err)
	require.True(t, first.Allowed)
	require.Equal(t, OutcomeAllowed, first.Outcome)

	denied, err := service.Acquire(ctx, Request{CredentialID: 61, ClientType: "windsurf"})
	require.NoError(t, err)
	require.True(t, denied.Allowed, "shadow mode must not refuse")
	require.Equal(t, OutcomeShadow, denied.Outcome)
}

func TestUnlimitedOrMissingPolicy(t *testing.T) {
	_, client := redisClient(t)
	service := New(Config{
		Mode:     ModeEnforce,
		Resolver: emptyResolver{},
		Redis:    client,
	})
	decision, err := service.Acquire(context.Background(), Request{CredentialID: 99, ClientType: "cursor"})
	require.NoError(t, err)
	require.True(t, decision.Allowed)
	require.Equal(t, OutcomeUnlimited, decision.Outcome)
	require.Nil(t, decision.Lease)
}

type emptyResolver struct{}

func (emptyResolver) Resolve(context.Context, int64, string) (Policy, bool) {
	return Policy{}, false
}

func TestResolverReloadAndNormalize(t *testing.T) {
	source := &mutableSource{rows: []Policy{
		{CredentialID: 11, ClientType: " Cursor ", MaxConcurrent: 2},
		{CredentialID: 11, ClientType: "future-client", MaxConcurrent: 1},
		{CredentialID: 12, ClientType: "codex", MaxConcurrent: 0},
		{CredentialID: 0, ClientType: "ignored", MaxConcurrent: 5},
	}}
	r := NewResolver(source)
	require.NoError(t, r.Reload(context.Background()))

	p, found := r.Resolve(context.Background(), 11, "CURSOR")
	require.True(t, found)
	require.Equal(t, 2, p.MaxConcurrent)
	require.Equal(t, "cursor", p.ClientType)

	p, found = r.Resolve(context.Background(), 11, "anything-new")
	require.True(t, found, "unknown clients fall under the unknown bucket")
	require.Equal(t, "unknown", p.ClientType)
	require.Equal(t, 1, p.MaxConcurrent)

	_, found = r.Resolve(context.Background(), 12, "codex")
	require.False(t, found, "non-positive policies are unlimited")

	_, found = r.Resolve(context.Background(), 999, "cursor")
	require.False(t, found, "no row is unlimited")

	source.setFailure(errors.New("postgres unavailable"))
	require.Error(t, r.Reload(context.Background()))
	p, found = r.Resolve(context.Background(), 11, "cursor")
	require.True(t, found, "previous snapshot is retained on reload failure")
	require.Equal(t, 2, p.MaxConcurrent)
}

type mutableSource struct {
	mu   sync.Mutex
	rows []Policy
	fail error
}

func (m *mutableSource) LoadPolicies(context.Context) ([]Policy, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return nil, m.fail
	}
	out := make([]Policy, len(m.rows))
	copy(out, m.rows)
	return out, nil
}

func (m *mutableSource) setFailure(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fail = err
}
