package admin

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ★ 2026-10-28（12h 审计轮）—— 本文件是 authRevocationEpoch 的**并发与缓存
// 正确性**守卫。它锁住三个缺陷（它们都属于「用错了判据」这一类）：
//
//   D1 缓存永远不命中：新鲜度判据曾是「map 是不是 nil」⇒ 系统里从未有人
//      改过密码（settings_kv 那一行**不存在** ⇒ parseAuthRevocations 返回
//      nil）⇒ map 恒 nil ⇒ **每次探测都重新查库**。
//      实测（修复前）：连做 5 次探测 ⇒ 5 次 settings_kv 取数。
//
//   D2 全局锁被跨越一次 DB 往返持有：并发探测全部串行。
//      实测（修复前，取数模拟 50ms）：8 个并发探测墙钟 414ms（= 8×50ms）。
//
//   D3 回填覆盖一次更新的失效：取数挪到锁外后，在途的旧结果可能在
//      「改密 ⇒ 失效缓存」之后才回来 ⇒ 把**已吊销**的纪元写回 ⇒ 刚被
//      吊销的 token 在整个 TTL 内继续有效（**安全**语义漏洞）。
//
// 三条都用**可观测的量**钉住（不是「调用成功」），且都做了变异验证：
// 把修复逐条还原 ⇒ 对应断言必须红。

// stubAuthRevocationFetch replaces the settings_kv read seam and counts
// calls. It returns the given raw bytes and sleeps for the given duration
// before returning (to model DB latency).
func stubAuthRevocationFetch(t *testing.T, raw []byte, delay time.Duration) *int64 {
	t.Helper()
	var calls int64
	old := authRevocationFetch
	authRevocationFetch = func(ctx context.Context, db *pgxpool.Pool) []byte {
		atomic.AddInt64(&calls, 1)
		if delay > 0 {
			time.Sleep(delay)
		}
		return raw
	}
	t.Cleanup(func() { authRevocationFetch = old })
	return &calls
}

// resetRevocationCache clears the cache and generation counter so each test
// starts from a known-cold state.
func resetRevocationCache(t *testing.T) {
	t.Helper()
	invalidateAuthRevocationCache()
	t.Cleanup(invalidateAuthRevocationCache)
}

// D1: with NO revocation row (the common case — nobody ever changed a
// password), repeated probes must hit the cache, not the database.
func TestAuthRevocationEpoch_CacheHitsWhenNoRevocationRowExists(t *testing.T) {
	resetRevocationCache(t)
	calls := stubAuthRevocationFetch(t, nil, 0)

	const probes = 5
	for i := 0; i < probes; i++ {
		if _, ok := authRevocationEpoch(context.Background(), nil, 7); ok {
			t.Fatalf("probe %d: no revocation row ⇒ must not report an epoch", i)
		}
	}
	if got := atomic.LoadInt64(calls); got != 1 {
		t.Fatalf("settings_kv fetches = %d, want exactly 1 for %d probes ⇒ "+
			"the cache never hits when the map is nil (D1 regression: the freshness "+
			"check must not depend on the map's nil-ness)", got, probes)
	}
}

// D1 (negative control): a non-empty map must ALSO hit the cache, and a
// known epoch must still be reported correctly through the cache.
func TestAuthRevocationEpoch_CacheServesKnownEpoch(t *testing.T) {
	resetRevocationCache(t)
	epoch := time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)
	calls := stubAuthRevocationFetch(t, []byte(`{"7":"`+epoch+`"}`), 0)

	for i := 0; i < 3; i++ {
		got, ok := authRevocationEpoch(context.Background(), nil, 7)
		if !ok {
			t.Fatalf("probe %d: expected an epoch for user 7", i)
		}
		want, _ := time.Parse(time.RFC3339Nano, epoch)
		if !got.Equal(want) {
			t.Fatalf("probe %d: epoch = %v, want %v", i, got, want)
		}
	}
	if got := atomic.LoadInt64(calls); got != 1 {
		t.Fatalf("settings_kv fetches = %d, want 1 ⇒ cache did not serve the repeat probes", got)
	}
}

// D2: concurrent probes must NOT serialize behind the DB round-trip.
//
// The measurement is a **ratio**, not an absolute duration: we compare the
// wall clock of N concurrent probes against N × the simulated fetch latency.
// With the old (lock held across the fetch) implementation the ratio is ~1
// (fully serial); with the fix it must be well below 1.
func TestAuthRevocationEpoch_ConcurrentProbesDoNotSerialize(t *testing.T) {
	resetRevocationCache(t)
	const (
		fetchLatency = 50 * time.Millisecond
		workers      = 8
	)
	calls := stubAuthRevocationFetch(t, nil, fetchLatency)

	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = authRevocationEpoch(context.Background(), nil, 7)
		}()
	}
	wg.Wait()
	wall := time.Since(start)

	serial := workers * fetchLatency
	// Require the concurrent wall clock to be well under the serial bound.
	// 0.75 leaves room for scheduler noise while still failing loudly on the
	// old implementation (which lands at ~1.0).
	if wall > time.Duration(float64(serial)*0.75) {
		t.Fatalf("concurrent probes took %v for %d workers with a %v fetch "+
			"(serial bound would be %v) ⇒ they are serializing behind the DB "+
			"round-trip (D2 regression: the global mutex must not be held "+
			"across the fetch)", wall, workers, fetchLatency, serial)
	}
	t.Logf("wall=%v serial-bound=%v fetches=%d", wall, serial, atomic.LoadInt64(calls))
}

// D3: a fetch that was already in flight when the cache was invalidated must
// NOT re-install its stale result.
//
// This is the security-relevant one: re-installing a stale map would keep a
// just-revoked token alive for a whole TTL.
//
// ★ 2026-10-28：**失效动作放在另一个 goroutine 里并带超时**——
//
//	因为「把取数挪到锁外」这条修复（D2）本身若被还原（取数期间持锁），
//	本测试里的失效调用就会**卡在锁上** ⇒ 若在测试主 goroutine 里直接调，
//	变异台会**挂死**（而不是干净地红）⇒ 一个会挂死的测试比一个会红的测试
//	更糟（它烧掉整个测试预算却不给出结论）。
//	⇒ 故：失效超时 ⇒ 直接判「取数期间持锁」（D2 回归）并**主动放行**取数，
//	让测试干净地失败。
func TestAuthRevocationEpoch_InflightFetchDoesNotResurrectStaleData(t *testing.T) {
	resetRevocationCache(t)

	// The fetch blocks until we say so, and returns an OLD epoch for user 7.
	release := make(chan struct{})
	old := authRevocationFetch
	authRevocationFetch = func(ctx context.Context, db *pgxpool.Pool) []byte {
		<-release
		return []byte(`{"7":"2000-01-01T00:00:00Z"}`)
	}
	t.Cleanup(func() { authRevocationFetch = old })

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = authRevocationEpoch(context.Background(), nil, 7)
	}()

	// Wait until the fetch is actually in flight, then invalidate (this is
	// what a password change does: write epoch, then invalidate cache).
	time.Sleep(50 * time.Millisecond)

	invalidated := make(chan struct{})
	go func() {
		invalidateAuthRevocationCache()
		close(invalidated)
	}()

	select {
	case <-invalidated:
	case <-time.After(3 * time.Second):
		// The invalidation could not proceed ⇒ the fetch is holding the
		// lock across the DB round-trip (D2 regression). Release the fetch
		// so the test can finish cleanly, then fail with that diagnosis.
		close(release)
		<-done
		t.Fatal("invalidateAuthRevocationCache() blocked for 3s ⇒ the settings_kv " +
			"fetch is holding the global mutex across the DB round-trip " +
			"(D2 regression) — a slow query would block every admin auth check")
	}

	close(release)
	<-done

	// The stale result must NOT be in the cache: a fresh probe must miss
	// (no epoch cached) rather than serve the resurrected old epoch.
	authRevocationMu.RLock()
	loaded := authRevocationLoaded
	authRevocationMu.RUnlock()
	if !loaded.IsZero() {
		t.Fatalf("cache was re-loaded after an invalidation ⇒ an in-flight fetch " +
			"resurrected stale data (D3 regression: the generation counter must " +
			"discard the result)")
	}
}
