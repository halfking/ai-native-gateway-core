// affinity_cache_test.go — P2.2 Track A: AffinityCache 单元测试。
//
// miniredis（已在 go.mod，无新增依赖）提供真 Redis 协议语义（TTL 支持
// FastForward），DAO 用 stub 代替真库。断言 Track C 计数时用
// Snapshot() 前后差值（包内测试串行执行，无 t.Parallel，全局计数器
// 差值稳定）。全部用例 -race 安全。
package routingopt

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
)

// stubAffinitySource 是 affinitySource 的内存 stub，记录回源次数。
type stubAffinitySource struct {
	mu    sync.Mutex
	calls int
	aff   *UserAffinity
	err   error
}

func (s *stubAffinitySource) GetByUserID(_ context.Context, userID string) (*UserAffinity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	if s.aff != nil && s.aff.UserID == "" {
		s.aff.UserID = userID
	}
	return s.aff, nil
}

func (s *stubAffinitySource) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *stubAffinitySource) setAffinity(aff *UserAffinity) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.aff = aff
}

func sampleAffinity(userID string) *UserAffinity {
	return &UserAffinity{
		UserID:               userID,
		TaskTypeDistribution: map[string]int{"code": 60, "chat": 30, "reasoning": 10},
		PreferredProviders:   map[string]float64{"anthropic": 0.7, "openai": 0.3},
		TotalRequests:        100,
	}
}

func assertAffinityEqual(t *testing.T, got, want *UserAffinity) {
	t.Helper()
	if got == nil || want == nil {
		if got != want {
			t.Fatalf("affinity mismatch: got %v, want %v", got, want)
		}
		return
	}
	// 只比较被缓存的负载字段：缓存条目按设计只存
	// task_type_distribution / preferred_providers / total_requests
	// （Enhance/UpdateUserAffinity 只消费它们），其余列（如 user_id、
	// 时间戳）以 DB 为准，不随缓存还原。
	for k, v := range want.TaskTypeDistribution {
		if got.TaskTypeDistribution[k] != v {
			t.Fatalf("task_type_distribution[%q]: got %d want %d", k, got.TaskTypeDistribution[k], v)
		}
	}
	if len(got.TaskTypeDistribution) != len(want.TaskTypeDistribution) {
		t.Fatalf("task_type_distribution size: got %d want %d", len(got.TaskTypeDistribution), len(want.TaskTypeDistribution))
	}
	for k, v := range want.PreferredProviders {
		if got.PreferredProviders[k] != v {
			t.Fatalf("preferred_providers[%q]: got %f want %f", k, got.PreferredProviders[k], v)
		}
	}
	if len(got.PreferredProviders) != len(want.PreferredProviders) {
		t.Fatalf("preferred_providers size: got %d want %d", len(got.PreferredProviders), len(want.PreferredProviders))
	}
	if got.TotalRequests != want.TotalRequests {
		t.Fatalf("total_requests: got %d want %d", got.TotalRequests, want.TotalRequests)
	}
}

// newTestCache 起一个 miniredis 并构造可注入 TTL 的缓存。
func newTestCache(t *testing.T, src affinitySource, ttl, negTTL time.Duration) (*AffinityCache, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis failed to start: %v", err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return newAffinityCache(src, rdb, ttl, negTTL), mr
}

// TestAffinityCache_HitDoesNotRequeryDAO：二次 Get 命中缓存，不再触 DAO。
func TestAffinityCache_HitDoesNotRequeryDAO(t *testing.T) {
	src := &stubAffinitySource{aff: sampleAffinity("42")}
	cache, _ := newTestCache(t, src, time.Hour, 30*time.Second)
	ctx := context.Background()

	hitsBefore := Snapshot().CacheHits
	aff, err := cache.Get(ctx, "42")
	if err != nil || aff == nil {
		t.Fatalf("first Get: aff=%v err=%v", aff, err)
	}
	if aff.UserID != "42" {
		t.Fatalf("DB-sourced Get must carry user_id, got %q", aff.UserID)
	}
	if got := src.callCount(); got != 1 {
		t.Fatalf("first Get must query DAO once, got %d", got)
	}

	aff2, err := cache.Get(ctx, "42")
	if err != nil || aff2 == nil {
		t.Fatalf("second Get: aff=%v err=%v", aff2, err)
	}
	if got := src.callCount(); got != 1 {
		t.Fatalf("second Get must hit cache (DAO calls=%d), want 1", got)
	}
	assertAffinityEqual(t, aff2, aff)

	if delta := Snapshot().CacheHits - hitsBefore; delta != 1 {
		t.Fatalf("cache hits delta = %d, want 1", delta)
	}
}

// TestAffinityCache_MissFillsRedis：miss 回源后 Redis 有条目，
// 且 RecordCacheMiss 被计数。
func TestAffinityCache_MissFillsRedis(t *testing.T) {
	src := &stubAffinitySource{aff: sampleAffinity("7")}
	cache, mr := newTestCache(t, src, time.Hour, 30*time.Second)
	ctx := context.Background()

	missBefore := Snapshot().CacheMiss
	if _, err := cache.Get(ctx, "7"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if delta := Snapshot().CacheMiss - missBefore; delta != 1 {
		t.Fatalf("cache miss delta = %d, want 1", delta)
	}
	if !mr.Exists(affinityKeyPrefix + "7") {
		t.Fatal("Redis key must exist after miss-backfill")
	}
	if fields := mr.HGet(affinityKeyPrefix+"7", affinityFieldTTD); fields == "" {
		t.Fatal("task_type_distribution field must be populated")
	}
}

// TestAffinityCache_NilRedisDegradesToDB：redis=nil 直查 DB，行为等同
// 无缓存（不 panic、不报错、每次都回源、不计数）。
func TestAffinityCache_NilRedisDegradesToDB(t *testing.T) {
	src := &stubAffinitySource{aff: sampleAffinity("9")}
	cache := newAffinityCache(src, nil, time.Hour, 30*time.Second)
	ctx := context.Background()

	hitsBefore, missBefore := Snapshot().CacheHits, Snapshot().CacheMiss
	for i := 0; i < 3; i++ {
		aff, err := cache.Get(ctx, "9")
		if err != nil || aff == nil {
			t.Fatalf("Get #%d: aff=%v err=%v", i, aff, err)
		}
	}
	if got := src.callCount(); got != 3 {
		t.Fatalf("nil redis must query DB every time, got %d calls", got)
	}
	if d := Snapshot().CacheHits - hitsBefore + Snapshot().CacheMiss - missBefore; d != 0 {
		t.Fatalf("degraded path must not count hits/misses, delta=%d", d)
	}
}

// TestAffinityCache_NegativeCacheWithinTTL：DB miss → 负缓存 30s 内
// 不再回源；TTL 过后重新回源。
func TestAffinityCache_NegativeCacheWithinTTL(t *testing.T) {
	src := &stubAffinitySource{err: pgx.ErrNoRows}
	cache, mr := newTestCache(t, src, time.Hour, 30*time.Second)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		aff, err := cache.Get(ctx, "ghost")
		if err != nil || aff != nil {
			t.Fatalf("Get #%d on missing user: aff=%v err=%v", i, aff, err)
		}
	}
	if got := src.callCount(); got != 1 {
		t.Fatalf("negative cache must suppress DB re-query within TTL, got %d calls", got)
	}
	if !mr.Exists(affinityKeyPrefix + "ghost") {
		t.Fatal("negative cache key must exist")
	}

	// FastForward 越过负缓存 TTL → 下次 Get 重新回源。
	mr.FastForward(31 * time.Second)
	if _, err := cache.Get(ctx, "ghost"); err != nil {
		t.Fatalf("Get after negative TTL: %v", err)
	}
	if got := src.callCount(); got != 2 {
		t.Fatalf("after negative TTL expiry the DAO must be queried again, got %d calls", got)
	}
}

// TestAffinityCache_DBErrorNotCached：DB 故障（非 ErrNoRows）不上抛为
// nil，也不回填缓存（故障窗口不被固化）。
func TestAffinityCache_DBErrorNotCached(t *testing.T) {
	boom := errors.New("db boom")
	src := &stubAffinitySource{err: boom}
	cache, mr := newTestCache(t, src, time.Hour, 30*time.Second)
	ctx := context.Background()

	if _, err := cache.Get(ctx, "42"); !errors.Is(err, boom) {
		t.Fatalf("DB error must propagate, got %v", err)
	}
	if mr.Exists(affinityKeyPrefix + "42") {
		t.Fatal("DB failure must not populate the cache")
	}
}

// TestAffinityCache_ConcurrentGet：并发 Get 压测，-race 干净、结果一致。
func TestAffinityCache_ConcurrentGet(t *testing.T) {
	src := &stubAffinitySource{aff: sampleAffinity("100")}
	cache, _ := newTestCache(t, src, time.Hour, 30*time.Second)
	ctx := context.Background()

	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				aff, err := cache.Get(ctx, "100")
				if err != nil {
					errs <- err
					return
				}
				if aff == nil || aff.TaskTypeDistribution["code"] != 60 {
					errs <- errors.New("inconsistent affinity under concurrency")
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent Get failed: %v", err)
	}
	if got := src.callCount(); got > 16 {
		// 回填是并发的（多 goroutine 可能同时 miss），但必须有界：
		// 16 个 goroutine 各至多回源一次，之后全部命中缓存。
		t.Fatalf("DB re-queries must stay bounded, got %d", got)
	}
}

// TestAffinityCache_Invalidate：Invalidate 删除缓存条目，下次 Get 回源。
func TestAffinityCache_Invalidate(t *testing.T) {
	src := &stubAffinitySource{aff: sampleAffinity("5")}
	cache, mr := newTestCache(t, src, time.Hour, 30*time.Second)
	ctx := context.Background()

	if _, err := cache.Get(ctx, "5"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !mr.Exists(affinityKeyPrefix + "5") {
		t.Fatal("cache must be populated before Invalidate")
	}

	cache.Invalidate("5")
	if mr.Exists(affinityKeyPrefix + "5") {
		t.Fatal("Invalidate must delete the Redis key")
	}

	src.setAffinity(sampleAffinity("5")) // DB 数据"已变化"
	aff, err := cache.Get(ctx, "5")
	if err != nil || aff == nil {
		t.Fatalf("Get after Invalidate: aff=%v err=%v", aff, err)
	}
	if got := src.callCount(); got != 2 {
		t.Fatalf("Get after Invalidate must re-query the DAO, got %d calls", got)
	}
}

// TestAffinityCache_UpdateInvalidatesConsistency：模拟 UpdateUserAffinity
// 的 del-on-write 一致性协议——读（回填）→ DB upsert 新数据 → Invalidate →
// 下次读必须拿到新数据而非旧缓存。
func TestAffinityCache_UpdateInvalidatesConsistency(t *testing.T) {
	src := &stubAffinitySource{aff: sampleAffinity("11")}
	cache, _ := newTestCache(t, src, time.Hour, 30*time.Second)
	ctx := context.Background()

	if _, err := cache.Get(ctx, "11"); err != nil {
		t.Fatalf("initial Get: %v", err)
	}

	// "DB upsert"（写穿 DB）+ Invalidate（enhancer.UpdateUserAffinity 的
	// 成功路径做的事）。
	updated := sampleAffinity("11")
	updated.TaskTypeDistribution = map[string]int{"code": 90, "chat": 10}
	src.setAffinity(updated)
	cache.Invalidate("11")

	aff, err := cache.Get(ctx, "11")
	if err != nil || aff == nil {
		t.Fatalf("Get after update+invalidate: aff=%v err=%v", aff, err)
	}
	if aff.TaskTypeDistribution["code"] != 90 {
		t.Fatalf("stale cache returned after update: code=%d, want 90", aff.TaskTypeDistribution["code"])
	}
	if got := src.callCount(); got != 2 {
		t.Fatalf("post-invalidate read must re-query the DAO, got %d calls", got)
	}
}

// TestAffinityCache_NilPoolAndEmptyUser：nil pool / 空 userID 冷启动安全，
// 不 panic 且返回"无数据"。
func TestAffinityCache_NilPoolAndEmptyUser(t *testing.T) {
	cache := NewAffinityCache(nil, nil)
	aff, err := cache.Get(context.Background(), "42")
	if err != nil || aff != nil {
		t.Fatalf("nil pool must degrade to no-data, got aff=%v err=%v", aff, err)
	}

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis failed: %v", err)
	}
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()

	cache2 := newAffinityCache(&stubAffinitySource{aff: sampleAffinity("1")}, rdb, time.Hour, 30*time.Second)
	if aff, err := cache2.Get(context.Background(), ""); err != nil || aff != nil {
		t.Fatalf("empty userID must short-circuit, got aff=%v err=%v", aff, err)
	}
	// Invalidate 对 nil/空参数必须是无操作（不 panic）。
	cache.Invalidate("42")
	cache2.Invalidate("")
}

// TestAffinityCache_NegativeHitNotCounted：负缓存命中不进 hit/miss 计数
// （与文档一致的单独说明）。
func TestAffinityCache_NegativeHitNotCounted(t *testing.T) {
	src := &stubAffinitySource{err: pgx.ErrNoRows}
	cache, _ := newTestCache(t, src, time.Hour, 30*time.Second)
	ctx := context.Background()

	if _, err := cache.Get(ctx, "ghost"); err != nil { // miss + 回源 + 负缓存
		t.Fatalf("Get: %v", err)
	}
	before := Snapshot()
	if _, err := cache.Get(ctx, "ghost"); err != nil { // 负缓存命中
		t.Fatalf("Get: %v", err)
	}
	if d := (Snapshot().CacheHits - before.CacheHits) + (Snapshot().CacheMiss - before.CacheMiss); d != 0 {
		t.Fatalf("negative-cache hits must not be counted, delta=%d", d)
	}
}

// TestBuildAffinityUpdate_CacheHitKeepsUserAndTotal：UpdateUserAffinity 的
// 读-改-写基线经缓存命中还原时，回写载荷必须带正确的 user_id 且
// total_requests 跨命中累加（而非重置）。回归背景：缓存初版未存
// total_requests、decodeAffinity 无法还原 user_id，缓存命中路径会把
// upsert 写进 user_id=” 的错行并把 DB 计数反复重置为 1。
func TestBuildAffinityUpdate_CacheHitKeepsUserAndTotal(t *testing.T) {
	src := &stubAffinitySource{aff: sampleAffinity("42")} // DB baseline: total=100
	cache, mr := newTestCache(t, src, time.Hour, 30*time.Second)
	enhancer := &ClassificationEnhancer{affinityCache: cache} // affinityDAO 不参与（不 upsert）
	ctx := context.Background()

	// 第 1 次：miss 回源（基线 total=100）→ 变更后应为 101，并回填缓存。
	aff1, err := enhancer.buildAffinityUpdate(ctx, "42", "code", "anthropic")
	if err != nil {
		t.Fatalf("first update: %v", err)
	}
	if aff1.UserID != "42" {
		t.Fatalf("first update user_id = %q, want 42", aff1.UserID)
	}
	if aff1.TotalRequests != 101 {
		t.Fatalf("first update total = %d, want 101", aff1.TotalRequests)
	}

	// 第 2 次（未 Invalidate）：纯缓存命中 → 基线必须还原为 DB 状态的
	// 100（而非 decode 缺字段的 0），变更后 101；user_id 必须归位
	// （decodeAffinity 本身不携带键）。
	aff2, err := enhancer.buildAffinityUpdate(ctx, "42", "chat", "openai")
	if err != nil {
		t.Fatalf("second update (cache hit): %v", err)
	}
	if aff2.UserID != "42" {
		t.Fatalf("cache-hit update user_id = %q, want 42 (empty user_id would upsert a junk row)", aff2.UserID)
	}
	if aff2.TotalRequests != 101 {
		t.Fatalf("cache-hit update total = %d, want 101 (a 1 here means total_requests was reset by decode)", aff2.TotalRequests)
	}
	if aff2.TaskTypeDistribution["code"] != 60 || aff2.TaskTypeDistribution["chat"] != 31 {
		t.Fatalf("cache-hit distribution baseline lost: %v", aff2.TaskTypeDistribution)
	}
	if got := src.callCount(); got != 1 {
		t.Fatalf("second update must be served from cache, got %d DAO calls", got)
	}

	// upsert+Invalidate（UpdateUserAffinity 成功路径）之后，DB 假设已更新为
	// aff2 状态（total=101）→ 下次读回源拿到新基线，变更后 102。
	src.setAffinity(aff2)
	cache.Invalidate("42")
	if mr.Exists(affinityKeyPrefix + "42") {
		t.Fatal("Invalidate must delete the cache entry after upsert")
	}
	aff3, err := enhancer.buildAffinityUpdate(ctx, "42", "code", "anthropic")
	if err != nil {
		t.Fatalf("third update after invalidate: %v", err)
	}
	if aff3.TotalRequests != 102 {
		t.Fatalf("post-invalidate baseline must come from refreshed DB state, total = %d, want 102", aff3.TotalRequests)
	}
}

// TestBuildAffinityUpdate_NilDistributionMap：分布列还原为 nil map（null
// JSON 场景）时更新不得 panic——补齐 map 后正常累加。
func TestBuildAffinityUpdate_NilDistributionMap(t *testing.T) {
	aff := sampleAffinity("7")
	aff.TaskTypeDistribution = nil // json.Unmarshal("null") 的还原结果
	src := &stubAffinitySource{aff: aff}
	cache, _ := newTestCache(t, src, time.Hour, 30*time.Second)
	enhancer := &ClassificationEnhancer{affinityCache: cache}
	ctx := context.Background()

	got, err := enhancer.buildAffinityUpdate(ctx, "7", "code", "anthropic")
	if err != nil {
		t.Fatalf("update on nil distribution map: %v", err)
	}
	if got.TaskTypeDistribution["code"] != 1 {
		t.Fatalf("distribution after update = %v, want code=1", got.TaskTypeDistribution)
	}
}
