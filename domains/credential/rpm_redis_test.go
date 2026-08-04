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
	t.Setenv("LLM_GATEWAY_REDIS_ADDR", "")
	if _, ok := NewRPMLimiterFromEnv().(*MemoryRPMLimiter); !ok {
		t.Fatal("empty RPM_REDIS_URL should select memory limiter")
	}
}

func TestNewRPMLimiterFromEnvInvalidURLDefaultsToMemory(t *testing.T) {
	t.Setenv("RPM_REDIS_URL", "not-a-redis-url")
	t.Setenv("LLM_GATEWAY_REDIS_ADDR", "")
	if _, ok := NewRPMLimiterFromEnv().(*MemoryRPMLimiter); !ok {
		t.Fatal("invalid RPM_REDIS_URL should select memory limiter")
	}
}

func TestNewRPMLimiterFromGatewayRedisAddress(t *testing.T) {
	t.Setenv("RPM_REDIS_URL", "")
	t.Setenv("LLM_GATEWAY_REDIS_ADDR", "127.0.0.1:6379")
	limiter, ok := NewRPMLimiterFromEnv().(*RedisRPMLimiter)
	if ok {
		t.Cleanup(func() { _ = limiter.client.Close() })
	}
	if !ok || limiter.client == nil {
		t.Fatal("LLM_GATEWAY_REDIS_ADDR should select Redis limiter")
	}
}

func TestNewRPMLimiterFromEnvPicksGatewayDB(t *testing.T) {
	// 2026-08-04: 验证 rpm limiter 与主网关使用同一个 db。
	t.Setenv("RPM_REDIS_URL", "")
	t.Setenv("LLM_GATEWAY_REDIS_ADDR", "127.0.0.1:1") // 故意不可达；只测 Options，不连
	t.Setenv("LLM_GATEWAY_REDIS_DB", "5")
	limiter, ok := NewRPMLimiterFromEnv().(*RedisRPMLimiter)
	if !ok || limiter == nil {
		t.Fatal("expected RedisRPMLimiter")
	}
	if got := limiter.client.Options().DB; got != 5 {
		t.Fatalf("DB = %d, want 5 (from LLM_GATEWAY_REDIS_DB)", got)
	}
	_ = limiter.client.Close()

	// env 没设时，rpm 应回退到默认 db=2（与 cfg.RedisDB 对齐）
	t.Setenv("LLM_GATEWAY_REDIS_DB", "")
	limiter2, ok := NewRPMLimiterFromEnv().(*RedisRPMLimiter)
	if !ok {
		t.Fatal("expected RedisRPMLimiter with default db")
	}
	if got := limiter2.client.Options().DB; got != 2 {
		t.Fatalf("default DB = %d, want 2", got)
	}
	_ = limiter2.client.Close()

	// RPM_REDIS_URL 显式指定 db=7 时，URL 优先
	t.Setenv("RPM_REDIS_URL", "redis://127.0.0.1:1/7")
	limiter3, ok := NewRPMLimiterFromEnv().(*RedisRPMLimiter)
	if !ok {
		t.Fatal("expected RedisRPMLimiter from explicit URL")
	}
	if got := limiter3.client.Options().DB; got != 7 {
		t.Fatalf("explicit URL DB = %d, want 7", got)
	}
	_ = limiter3.client.Close()
}

func TestRedisURLFromAddress(t *testing.T) {
	// db <= 0: 不附加 /db（向后兼容旧行为）
	if got := redisURLFromAddress("redis.example:6379", 0); got != "redis://redis.example:6379" {
		t.Fatalf("got %q", got)
	}
	if got := redisURLFromAddress("redis://redis.example:6379/2", 5); got != "redis://redis.example:6379/2" {
		t.Fatalf("explicit /db in URL wins: got %q", got)
	}
	// 新行为：默认 db=2 时自动附加
	if got := redisURLFromAddress("redis.example:6379", 2); got != "redis://redis.example:6379/2" {
		t.Fatalf("got %q", got)
	}
	// 已有末尾斜杠的 URL
	if got := redisURLFromAddress("redis://redis.example:6379/", 2); got != "redis://redis.example:6379/2" {
		t.Fatalf("got %q", got)
	}
	// 含 user:pass@ 的 URL
	if got := redisURLFromAddress("redis://u:p@redis.example:6379", 3); got != "redis://u:p@redis.example:6379/3" {
		t.Fatalf("got %q", got)
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
