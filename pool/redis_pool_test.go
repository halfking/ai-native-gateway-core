package pool

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newRedisPoolManagerForTest(t *testing.T) (*RedisPoolManager, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: 0})
	t.Cleanup(func() { _ = client.Close() })
	return NewRedisPoolManager(client), server
}

func TestRedisPoolManagerAcquireReleaseAndConcurrencyLimit(t *testing.T) {
	manager, server := newRedisPoolManagerForTest(t)
	key := PoolKey{IdentityHash: "redis-identity", ProviderID: 7, CredentialID: 8}
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		acquired, err := manager.Acquire(ctx, key, 3)
		if err != nil || !acquired {
			t.Fatalf("Acquire %d = (%v, %v), want (true, nil)", i, acquired, err)
		}
	}
	if acquired, err := manager.Acquire(ctx, key, 3); err != nil || acquired {
		t.Fatalf("saturated Acquire = (%v, %v), want (false, nil)", acquired, err)
	}
	if got, err := manager.Stats(ctx, key); err != nil || got != 3 {
		t.Fatalf("Stats = (%d, %v), want (3, nil)", got, err)
	}

	if err := manager.Release(ctx, key); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if got, err := manager.Stats(ctx, key); err != nil || got != 2 {
		t.Fatalf("Stats after Release = (%d, %v), want (2, nil)", got, err)
	}

	// Extra release is deliberately a no-op; the Lua script must never go negative.
	for i := 0; i < 4; i++ {
		if err := manager.Release(ctx, key); err != nil {
			t.Fatalf("extra Release %d: %v", i, err)
		}
	}
	if got, err := manager.Stats(ctx, key); err != nil || got != 0 {
		t.Fatalf("Stats after extra releases = (%d, %v), want (0, nil)", got, err)
	}
	if ttl := server.TTL(manager.activeKey(key)); ttl <= 0 || ttl > redisPoolTTL {
		t.Fatalf("active key TTL = %s, want >0 and <= %s", ttl, redisPoolTTL)
	}
}

func TestRedisPoolManagerConcurrentAcquireHonorsLimit(t *testing.T) {
	manager, _ := newRedisPoolManagerForTest(t)
	key := PoolKey{IdentityHash: "redis-concurrent", ProviderID: 9, CredentialID: 10}
	const (
		workers = 32
		limit   = 5
	)

	var wg sync.WaitGroup
	var mu sync.Mutex
	acquired := 0
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := manager.Acquire(context.Background(), key, limit)
			if err != nil {
				t.Errorf("concurrent Acquire: %v", err)
				return
			}
			if ok {
				mu.Lock()
				acquired++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if acquired != limit {
		t.Fatalf("concurrent successful acquires = %d, want %d", acquired, limit)
	}
	if got, err := manager.Stats(context.Background(), key); err != nil || got != limit {
		t.Fatalf("Stats = (%d, %v), want (%d, nil)", got, err, limit)
	}
}

func TestRedisPoolManagerFailureSuccessCountersAndTTL(t *testing.T) {
	manager, server := newRedisPoolManagerForTest(t)
	key := PoolKey{IdentityHash: "redis-health", ProviderID: 11, CredentialID: 12}
	ctx := context.Background()
	failKey := manager.poolKey(key) + redisPoolFailKeySuffix

	for i := 0; i < 3; i++ {
		if err := manager.RecordFailure(ctx, key); err != nil {
			t.Fatalf("RecordFailure %d: %v", i, err)
		}
	}
	value, err := manager.client.Get(ctx, failKey).Int()
	if err != nil || value != 3 {
		t.Fatalf("failure count = (%d, %v), want (3, nil)", value, err)
	}
	for i := 0; i < 2; i++ {
		if err := manager.RecordSuccess(ctx, key); err != nil {
			t.Fatalf("RecordSuccess %d: %v", i, err)
		}
	}
	value, err = manager.client.Get(ctx, failKey).Int()
	if err != nil || value != 1 {
		t.Fatalf("failure count after successes = (%d, %v), want (1, nil)", value, err)
	}
	if ttl := server.TTL(failKey); ttl <= 0 || ttl > redisFailTTL {
		t.Fatalf("failure key TTL = %s, want >0 and <= %s", ttl, redisFailTTL)
	}
	if err := manager.RecordSuccess(ctx, key); err != nil {
		t.Fatalf("final RecordSuccess: %v", err)
	}
	value, err = manager.client.Get(ctx, failKey).Int()
	if err != nil || value != 0 {
		t.Fatalf("failure count after final success = (%d, %v), want (0, nil)", value, err)
	}
	if ttl := server.TTL(failKey); ttl <= 0 || ttl > redisFailTTL {
		t.Fatalf("failure key TTL after reaching zero = %s, want >0 and <= %s", ttl, redisFailTTL)
	}
}

func TestRedisPoolManagerRedisErrorAndContextCancel(t *testing.T) {
	manager, server := newRedisPoolManagerForTest(t)
	key := PoolKey{IdentityHash: "redis-error", ProviderID: 13, CredentialID: 14}

	server.SetError("ERR injected")
	if ok, err := manager.Acquire(context.Background(), key, 1); err == nil || ok {
		t.Fatalf("Acquire with Redis error = (%v, %v), want false and error", ok, err)
	}
	if err := manager.Release(context.Background(), key); err == nil {
		t.Fatal("Release with Redis error returned nil")
	}
	if _, err := manager.Stats(context.Background(), key); err == nil {
		t.Fatal("Stats with Redis error returned nil")
	}
	if err := manager.RecordFailure(context.Background(), key); err == nil {
		t.Fatal("RecordFailure with Redis error returned nil")
	}
	if err := manager.RecordSuccess(context.Background(), key); err == nil {
		t.Fatal("RecordSuccess with Redis error returned nil")
	}

	server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if ok, err := manager.Acquire(ctx, key, 1); err == nil || ok || !errors.Is(err, context.Canceled) {
		t.Fatalf("Acquire canceled context = (%v, %v), want false/context.Canceled", ok, err)
	}
}

func TestRedisPoolManagerDisabled(t *testing.T) {
	manager := NewRedisPoolManager(nil)
	key := PoolKey{IdentityHash: "disabled", ProviderID: 1, CredentialID: 1}
	if manager.Enabled() {
		t.Fatal("nil client should disable Redis manager")
	}
	if ok, err := manager.Acquire(context.Background(), key, 1); err == nil || ok {
		t.Fatalf("disabled Acquire = (%v, %v), want false and error", ok, err)
	}
	if err := manager.Release(context.Background(), key); err != nil {
		t.Fatalf("disabled Release: %v", err)
	}
	if got, err := manager.Stats(context.Background(), key); err != nil || got != 0 {
		t.Fatalf("disabled Stats = (%d, %v), want (0, nil)", got, err)
	}
	if err := manager.RecordFailure(context.Background(), key); err != nil {
		t.Fatalf("disabled RecordFailure: %v", err)
	}
	if err := manager.RecordSuccess(context.Background(), key); err != nil {
		t.Fatalf("disabled RecordSuccess: %v", err)
	}
}
