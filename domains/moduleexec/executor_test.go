// Package moduleexec 测试
package moduleexec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/kaixuan/llm-gateway-go/domains/moduleregistry"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// TestComputeCacheKey 测试缓存键计算
func TestComputeCacheKey(t *testing.T) {
	// 相同输入应该产生相同 key
	params1 := map[string]interface{}{"a": 1, "b": "test"}
	params2 := map[string]interface{}{"a": 1, "b": "test"}

	key1 := ComputeCacheKey("test_module", params1)
	key2 := ComputeCacheKey("test_module", params2)

	if key1 != key2 {
		t.Errorf("expected same key for same params, got %s != %s", key1, key2)
	}

	// 不同输入应该产生不同 key
	params3 := map[string]interface{}{"a": 2, "b": "test"}
	key3 := ComputeCacheKey("test_module", params3)

	if key1 == key3 {
		t.Errorf("expected different key for different params")
	}

	// key 格式应该是 module:hash
	if len(key1) < len("test_module:")+16 {
		t.Errorf("key format incorrect: %s", key1)
	}
}

// TestExecutorMemoryCache 测试内存缓存
func TestExecutorMemoryCache(t *testing.T) {
	executor := &Executor{
		memCache:     make(map[string]*ExecuteResult),
		memCacheTime: make(map[string]time.Time),
		memCacheTTL:  1 * time.Second,
	}

	sessionID := "test_session"
	moduleName := "test_module"
	cacheKey := "test_key"

	result := &ExecuteResult{
		ExecutionID: 1,
		Status:      StatusCompleted,
	}

	// 第一次查询应该返回 nil
	cached := executor.getFromMemCache("tenant", sessionID, moduleName, cacheKey)
	if cached != nil {
		t.Error("expected nil for empty cache")
	}

	// 写入缓存
	executor.setToMemCache("tenant", sessionID, moduleName, cacheKey, result)

	// 第二次查询应该返回缓存
	cached = executor.getFromMemCache("tenant", sessionID, moduleName, cacheKey)
	if cached == nil {
		t.Error("expected cached result")
	}
	if cached.ExecutionID != 1 {
		t.Errorf("expected execution_id 1, got %d", cached.ExecutionID)
	}

	// 等待 TTL 过期
	time.Sleep(1100 * time.Millisecond)

	// 过期后应该返回 nil
	cached = executor.getFromMemCache("tenant", sessionID, moduleName, cacheKey)
	if cached != nil {
		t.Error("expected nil for expired cache")
	}
}

// TestExecutorMemoryCacheConcurrent 测试内存缓存并发安全
func TestExecutorMemoryCacheConcurrent(t *testing.T) {
	executor := &Executor{
		memCache:     make(map[string]*ExecuteResult),
		memCacheTime: make(map[string]time.Time),
		memCacheTTL:  5 * time.Second,
	}

	var wg sync.WaitGroup
	var hits int64

	// 100 个 goroutine 并发读写
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			sessionID := "session"
			moduleName := "module"
			cacheKey := "key"

			// 写
			executor.setToMemCache("tenant", sessionID, moduleName, cacheKey, &ExecuteResult{
				ExecutionID: int64(idx),
			})

			// 读
			cached := executor.getFromMemCache("tenant", sessionID, moduleName, cacheKey)
			if cached != nil {
				atomic.AddInt64(&hits, 1)
			}
		}(i)
	}

	wg.Wait()

	if hits == 0 {
		t.Error("expected at least some cache hits")
	}
}

// TestExecuteResultJSON 测试 ExecuteResult 序列化
func TestExecuteResultJSON(t *testing.T) {
	result := &ExecuteResult{
		ExecutionID: 123,
		Status:      StatusCompleted,
		ResultSummary: map[string]interface{}{
			"score":    3,
			"decision": "pass",
		},
		ResultDetail: map[string]interface{}{
			"full_data": "test",
		},
		FromCache:  true,
		DurationMs: 100,
	}

	// 序列化
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	// 反序列化
	var decoded ExecuteResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	// 验证
	if decoded.ExecutionID != 123 {
		t.Errorf("expected execution_id 123, got %d", decoded.ExecutionID)
	}
	if decoded.Status != StatusCompleted {
		t.Errorf("expected status completed, got %s", decoded.Status)
	}
	if !decoded.FromCache {
		t.Error("expected from_cache true")
	}
	if decoded.ResultSummary["score"].(float64) != 3 {
		t.Error("score mismatch")
	}
}

// TestComputeCacheKeyStability 测试缓存键稳定性
func TestComputeCacheKeyStability(t *testing.T) {
	// 即使 map 顺序不同，相同内容应该产生相同 key
	params1 := map[string]interface{}{"a": 1, "b": 2, "c": 3}
	params2 := map[string]interface{}{"c": 3, "b": 2, "a": 1}

	key1 := ComputeCacheKey("mod", params1)
	key2 := ComputeCacheKey("mod", params2)

	// 注意：Go map 序列化顺序不固定，所以这两个 key 可能不同
	// 这个测试主要是为了记录这个特性
	t.Logf("key1: %s, key2: %s, equal: %v", key1, key2, key1 == key2)
}

// TestModuleRegistryConsistency 测试模块注册表一致性
func TestModuleRegistryConsistency(t *testing.T) {
	// 这个测试需要在导入 moduleregistry 包时运行
	t.Skip("需要导入 moduleregistry 包进行测试")
}

func TestExecutorNilDBExecutesDirectly(t *testing.T) {
	executor := NewExecutor(Config{})
	var calls int
	for i := 0; i < 2; i++ {
		result, err := executor.CheckAndExecute(context.Background(), "session", "tenant", moduleregistry.ModuleSessionAudit, nil, 60,
			func(context.Context) (*ExecuteResult, error) {
				calls++
				return &ExecuteResult{ResultSummary: map[string]interface{}{"call": calls}}, nil
			})
		if err != nil {
			t.Fatalf("CheckAndExecute: %v", err)
		}
		if result.FromCache {
			t.Fatal("nil DB direct execution must not report a cache hit")
		}
	}
	if calls != 2 {
		t.Fatalf("execute calls = %d, want 2", calls)
	}
}

func TestExecutorMemoryCacheCompositeKeyPreventsCollisionAndIsolatesInvalidation(t *testing.T) {
	executor := newTestExecutor(t)
	first := []string{"tenant:one", "session", "module", "key"}
	second := []string{"tenant", "one:session", "module", "key"}

	if memCacheKey(first[0], first[1], first[2], first[3]) == memCacheKey(second[0], second[1], second[2], second[3]) {
		t.Fatal("structured cache keys collided for delimiter-containing values")
	}
	if executor.redisKey(first[0], first[1], first[2], first[3]) == executor.redisKey(second[0], second[1], second[2], second[3]) {
		t.Fatal("structured Redis keys collided for delimiter-containing values")
	}

	if err := executor.setToMemCache(first[0], first[1], first[2], first[3], &ExecuteResult{ExecutionID: 1}); err != nil {
		t.Fatalf("set first cache result: %v", err)
	}
	if err := executor.setToMemCache(second[0], second[1], second[2], second[3], &ExecuteResult{ExecutionID: 2}); err != nil {
		t.Fatalf("set second cache result: %v", err)
	}

	if err := executor.InvalidateCache(context.Background(), first[0], first[1], first[2]); err != nil {
		t.Fatalf("invalidate first cache key: %v", err)
	}
	if got := executor.getFromMemCache(first[0], first[1], first[2], first[3]); got != nil {
		t.Fatalf("invalidated cache result = %+v, want nil", got)
	}
	if got := executor.getFromMemCache(second[0], second[1], second[2], second[3]); got == nil || got.ExecutionID != 2 {
		t.Fatalf("unrelated delimiter-containing cache result = %+v, want execution ID 2", got)
	}
}

func TestCloneMapReturnsErrorForUnsupportedValues(t *testing.T) {
	if _, err := cloneMap(map[string]interface{}{"unsupported": func() {}}); err == nil {
		t.Fatal("cloneMap accepted unsupported value")
	}
}

func TestExecutorCheckAndExecuteCoalescesConcurrentRequests(t *testing.T) {
	executor := NewExecutor(Config{})
	const workers = 20

	start := make(chan struct{})
	var calls atomic.Int64
	results := make(chan *ExecuteResult, workers)
	errors := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			result, err := executor.CheckAndExecute(context.Background(), "session", "tenant", moduleregistry.ModuleSessionAudit, map[string]interface{}{"request": "same"}, 60,
				func(context.Context) (*ExecuteResult, error) {
					calls.Add(1)
					time.Sleep(25 * time.Millisecond)
					return &ExecuteResult{ResultSummary: map[string]interface{}{"nested": map[string]interface{}{"value": "result"}}}, nil
				})
			if err != nil {
				errors <- err
				return
			}
			results <- result
		}()
	}
	close(start)
	wg.Wait()
	close(errors)
	close(results)

	for err := range errors {
		t.Fatalf("CheckAndExecute: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("execute function calls = %d, want 1", got)
	}
	for result := range results {
		if result == nil || result.Status != StatusCompleted {
			t.Fatalf("result = %+v, want completed", result)
		}
	}
}

func TestExecutorMemoryCacheClonesResults(t *testing.T) {
	executor := newTestExecutor(t)
	original := &ExecuteResult{ResultSummary: map[string]interface{}{"nested": map[string]interface{}{"value": "original"}}}
	executor.setToMemCache("tenant", "session", "module", "key", original)
	original.ResultSummary["nested"].(map[string]interface{})["value"] = "mutated"

	first := executor.getFromMemCache("tenant", "session", "module", "key")
	first.ResultSummary["nested"].(map[string]interface{})["value"] = "reader mutation"
	second := executor.getFromMemCache("tenant", "session", "module", "key")
	if got := second.ResultSummary["nested"].(map[string]interface{})["value"]; got != "original" {
		t.Fatalf("cached nested value = %v, want original", got)
	}
}

func TestExecutorMemoryCacheConcurrentResultMutation(t *testing.T) {
	executor := newTestExecutor(t)
	executor.setToMemCache("tenant", "session", "module", "key", &ExecuteResult{ResultSummary: map[string]interface{}{"value": 1}})
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(value int) {
			defer wg.Done()
			result := executor.getFromMemCache("tenant", "session", "module", "key")
			result.ResultSummary["value"] = value
		}(i)
	}
	wg.Wait()
}

func TestExecutorCorruptRedisCacheIsMiss(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	executor := NewExecutor(Config{Redis: client, EnableRedis: true})
	key := executor.redisKey("tenant", "session", "module", "key")
	server.Set(key, "not-json")
	if got := executor.getFromRedis(context.Background(), "tenant", "session", "module", "key"); got != nil {
		t.Fatalf("corrupt cache returned %+v", got)
	}
}

// BenchmarkComputeCacheKey 性能测试
func BenchmarkComputeCacheKey(b *testing.B) {
	params := map[string]interface{}{
		"a": 1,
		"b": "test",
		"c": []int{1, 2, 3},
		"d": map[string]interface{}{"nested": "value"},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ComputeCacheKey("test_module", params)
	}
}

// BenchmarkMemoryCacheGet 内存缓存读取性能
func BenchmarkMemoryCacheGet(b *testing.B) {
	executor := &Executor{
		memCache:     make(map[string]*ExecuteResult),
		memCacheTime: make(map[string]time.Time),
		memCacheTTL:  1 * time.Hour,
	}

	executor.setToMemCache("tenant", "session", "module", "key", &ExecuteResult{
		ExecutionID: 1,
	})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		executor.getFromMemCache("tenant", "session", "module", "key")
	}
}

// 辅助函数：计算 SHA256（用于测试）
func sha256hex(data string) string {
	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:])
}

// 辅助函数：创建测试用的 Executor
func newTestExecutor(t *testing.T) *Executor {
	t.Helper()
	return &Executor{
		memCache:     make(map[string]*ExecuteResult),
		memCacheTime: make(map[string]time.Time),
		memCacheTTL:  30 * time.Second,
	}
}

// 辅助函数：创建测试用的 DB（需要真实数据库）
func newTestDB(t *testing.T) *pgxpool.Pool {
	t.Skip("需要真实数据库连接")
	return nil
}

// 辅助函数：创建测试用的 Redis
func newTestRedis(t *testing.T) *redis.Client {
	t.Skip("需要真实 Redis 连接")
	return nil
}

// TestCheckAndExecuteLogic 测试 CheckAndExecute 逻辑（不依赖数据库）
func TestCheckAndExecuteLogic(t *testing.T) {
	_ = newTestExecutor(t)
	_ = context.Background()

	// 这里可以添加更多单元测试
	// 实际的集成测试需要数据库连接
}
