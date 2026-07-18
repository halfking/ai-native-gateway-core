package credential

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestRedisRPMLimiter(t *testing.T) (*RedisRPMLimiter, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: 0})
	t.Cleanup(func() { _ = client.Close() })
	return NewRedisRPMLimiter(client), server
}

func TestRedisRPMLimiterBasicWindow(t *testing.T) {
	limiter, _ := newTestRedisRPMLimiter(t)
	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		allowed, count, err := limiter.CheckAndReserve(ctx, 1, 2, 3)
		if err != nil || !allowed || count != i {
			t.Fatalf("reservation %d: allowed=%v count=%d err=%v", i, allowed, count, err)
		}
	}
	allowed, count, err := limiter.CheckAndReserve(ctx, 1, 2, 3)
	if err != nil || allowed || count != 3 {
		t.Fatalf("expected denial at limit: allowed=%v count=%d err=%v", allowed, count, err)
	}
}

func TestNewRPMLimiterFromEnvDefaultsToMemory(t *testing.T) {
	t.Setenv("RPM_REDIS_URL", "")
	if _, ok := NewRPMLimiterFromEnv().(*MemoryRPMLimiter); !ok {
		t.Fatal("empty RPM_REDIS_URL should select memory limiter")
	}
}

func TestNewRPMLimiterFromEnvInvalidURLDefaultsToMemory(t *testing.T) {
	t.Setenv("RPM_REDIS_URL", "not-a-redis-url")
	if _, ok := NewRPMLimiterFromEnv().(*MemoryRPMLimiter); !ok {
		t.Fatal("invalid RPM_REDIS_URL should select memory limiter")
	}
}

func TestRedisRPMLimiterConcurrent(t *testing.T) {
	limiter, _ := newTestRedisRPMLimiter(t)
	const limit = 20
	const requests = 100
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, _, err := limiter.CheckAndReserve(context.Background(), 1, 1, limit)
			if err != nil {
				t.Errorf("concurrent reservation failed: %v", err)
				return
			}
			if ok {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if allowed != limit {
		t.Fatalf("allowed=%d, want exactly %d", allowed, limit)
	}
}

func TestRedisRPMLimiterFallback(t *testing.T) {
	limiter, server := newTestRedisRPMLimiter(t)
	server.Close()
	for i := 0; i < 2; i++ {
		allowed, _, err := limiter.CheckAndReserve(context.Background(), 3, 4, 2)
		if err != nil || !allowed {
			t.Fatalf("fallback reservation %d: allowed=%v err=%v", i, allowed, err)
		}
	}
	allowed, _, err := limiter.CheckAndReserve(context.Background(), 3, 4, 2)
	if err != nil || allowed {
		t.Fatalf("fallback should enforce limit: allowed=%v err=%v", allowed, err)
	}
}

func TestRedisRPMLimiterMultiInstance(t *testing.T) {
	server := miniredis.RunT(t)
	defer server.Close()
	clients := make([]*RedisRPMLimiter, 3)
	for i := range clients {
		client := redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: 0})
		defer client.Close()
		clients[i] = NewRedisRPMLimiter(client)
	}

	allowed := 0
	for i := 0; i < 30; i++ {
		ok, _, err := clients[i%len(clients)].CheckAndReserve(context.Background(), 8, 9, 10)
		if err != nil {
			t.Fatalf("instance %d failed: %v", i%len(clients), err)
		}
		if ok {
			allowed++
		}
	}
	if allowed != 10 {
		t.Fatalf("global allowed=%d, want 10", allowed)
	}
}

func TestRedisRPMLimiterUnlimitedAndFallbackContext(t *testing.T) {
	limiter, _ := newTestRedisRPMLimiter(t)
	for i := 0; i < 10; i++ {
		allowed, count, err := limiter.CheckAndReserve(context.Background(), 1, 1, 0)
		if err != nil || !allowed || count != 0 {
			t.Fatalf("unlimited reservation: allowed=%v count=%d err=%v", allowed, count, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	allowed, _, err := limiter.CheckAndReserve(ctx, 1, 1, 1)
	if !errors.Is(err, context.Canceled) || allowed {
		t.Fatalf("fallback should not reserve canceled request: allowed=%v err=%v", allowed, err)
	}
}

func TestRedisRPMIntegration(t *testing.T) {
	limiter, server := newTestRedisRPMLimiter(t)
	allowed, count, err := limiter.CheckAndReserve(context.Background(), 11, 12, 1)
	if err != nil || !allowed || count != 1 {
		t.Fatalf("integration reservation: allowed=%v count=%d err=%v", allowed, count, err)
	}
	key := limiter.redisKey(11, 12)
	members, err := server.ZMembers(key)
	if err != nil || len(members) != 1 {
		t.Fatalf("redis zset cardinality=%d, want 1 (err=%v)", len(members), err)
	}
}

func TestRedisRPMMultiGateway(t *testing.T) {
	server := miniredis.RunT(t)
	defer server.Close()
	const replicas = 3
	const limit = 15
	limiters := make([]*RedisRPMLimiter, replicas)
	for i := range limiters {
		client := redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: 0})
		defer client.Close()
		limiters[i] = NewRedisRPMLimiter(client)
	}

	allowed := 0
	for i := 0; i < replicas*limit; i++ {
		ok, _, err := limiters[i%replicas].CheckAndReserve(context.Background(), 21, 22, limit)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			allowed++
		}
	}
	if allowed != limit {
		t.Fatalf("3 gateways allowed=%d, want global limit=%d", allowed, limit)
	}
}

func BenchmarkRPMMemory(b *testing.B) {
	limiter := NewMemoryRPMLimiter()
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = limiter.CheckAndReserve(ctx, 1, i%100000, b.N+1)
	}
}

func BenchmarkRPMRedis(b *testing.B) {
	server := miniredis.RunT(b)
	defer server.Close()
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: 0})
	defer client.Close()
	limiter := NewRedisRPMLimiter(client)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = limiter.CheckAndReserve(ctx, 1, i%100000, b.N+1)
	}
}
