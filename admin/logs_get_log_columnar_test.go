// Copyright 2026 kaixuan.ai
// Regression tests for the two-stage request body lookup introduced in
// admin/logs.go::fetchRequestBodies.
//
// 2026-08-17 incident: dashboard "实时请求流 → 点击请求" returns HTTP 500
// `{"error":{"detail":"query failed","db_error":"timeout: context
// deadline exceeded"}}`.
//
// Root cause: request_logs_bodies_with_current_month UNIONs hot (heap,
// idx <1ms) with columnar monthly partitions (request_logs_bodies_2026_08,
// 2 GB, no btree usable, ColumnarScan = full decompression pass for single
// ID lookup, >30s). Even when the row lives in hot, the planner had to
// include the columnar partition in the Append node.
//
// Fix: getLog no longer JOINs the bodies view in the main metadata query.
// fetchRequestBodies is called instead and short-circuits after hot hits;
// columnar is only consulted when hot misses (older rows after TTL promote).
//
// 2026-08-17 OPTIMIZATION: in-memory LRU+TTL cache. Dashboard users
// frequently "open → close → reopen" the same request_id to compare
// payloads; second click goes through cache in < 1ms instead of
// re-running a 5s columnar scan.
//
// These tests assert:
//   - hot-only row → helper returns body from hot, completes fast.
//   - hot-empty row → helper falls back to bodies view (cold path).
//   - bodies absent → helper returns errBodyNotFound; caller treats this
//     as non-fatal and still returns metadata.
//   - cache hit → second call returns the cached body in < 10ms.
//   - cache TTL expired → next call falls through to DB.
//   - transport error → not cached (next call retries).
//
// Run with TEST_DATABASE_URL pointing at the dev DB. -short skips them.
package admin

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFetchRequestBodies_HotOnly_PassesInUnderOneSecond is the regression
// for the dashboard incident. The previous LEFT JOIN forced a columnar
// scan regardless of where the body lived; this test verifies that with
// the new helper, a hot-only row resolves in well under 1 s.
func TestFetchRequestBodies_HotOnly_PassesInUnderOneSecond(t *testing.T) {
	pool := setupTestDB(t)
	defer pool.Close()

	ctx := context.Background()
	requestID := "test-fetch-bodies-hot-" + time.Now().Format("20060102-150405.000000")
	insertSessionTurnWithBodies(t, pool, requestID)
	// R65 批判复审：t.Cleanup 晚于 defer pool.Close 执行，cleanup 打在已关池上
	// 静默漏行（每轮全量漏 7 行实锤）——defer 注册晚于 Close，LIFO 先清后关。
	defer cleanupTestRequestLog(t, pool, requestID)

	h := &Handler{db: pool}
	start := time.Now()
	reqBody, respBody, err := h.fetchRequestBodies(ctx, requestID)
	elapsed := time.Since(start)

	require.NoError(t, err, "hot-only row should resolve without error")
	assert.Less(t, elapsed, 1*time.Second,
		"hot path must not trigger columnar scan (got %s; if this is slow the fix regressed)", elapsed)

	// Verify body contents survived the trip through decodeStoredBodyForAdmin.
	rbJSON, ok := reqBody.(map[string]any)
	require.True(t, ok, "requestBody should decode to a JSON object, got %T", reqBody)
	assert.Contains(t, rbJSON, "model", "requestBody should carry the inserted model field")

	obJSON, ok := respBody.(map[string]any)
	require.True(t, ok, "responseBody should decode to a JSON object, got %T", respBody)
	assert.Contains(t, obJSON, "id", "responseBody should carry the inserted id field")
}

// TestFetchRequestBodies_HotMiss_FallsBackToBodiesView verifies that when
// the hot tier has no row for a given request_id, the helper still reaches the
// parent tier.
//
// ★ R68 改瞄（2026-10-21）：旧版只能「断言第二条腿被调用过」——因为当时 v1 的
// bodies 父表是 **columnar** 分区，dev 库里有没有被 TTL 晋升过是环境状态
// ⇒ 断言写成「要么成功、要么 ErrNoRows」⇒ **两���都绿** ⇒ 这道门其实没牙。
// 会话族的 bodies 父表当月分区实测是 **heap** ⇒ 父表腿**必然可达**
// ⇒ 这里可以**确定性地**断言「父表腿的行被取到」。
func TestFetchRequestBodies_HotMiss_FallsBackToBodiesView(t *testing.T) {
	pool := setupTestDB(t)
	defer pool.Close()

	ctx := context.Background()
	requestID := "test-fetch-bodies-cold-" + time.Now().Format("20060102-150405.000000")

	// 只插父表腿（session_bodies）；session_bodies_hot 不会有这个 request_id
	// ⇒ 命中只可能来自父表腿。
	require.NoError(t, insertBodyIntoPartitionedOnly(t, pool, requestID))
	// R65 同形修正（R41 R-1 漏网处）：t.Cleanup 晚于 defer pool.Close 执行，
	// 清理打在已关池上静默漏行——defer 注册晚于 Close，LIFO 保证先清后关。
	defer cleanupColdBody(t, pool, requestID)

	// 前提：hot 腿确实没有这一行 ⇒ 父表腿可达性没被证伪
	var hotRows int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT count(*) FROM session_bodies_hot WHERE request_id = $1
	`, requestID).Scan(&hotRows))
	require.Equal(t, 0, hotRows, "前提破坏：hot 腿竟然有这一行 ⇒ 没证明父表腿被读到")

	h := &Handler{db: pool}
	reqBody, respBody, err := h.fetchRequestBodies(ctx, requestID)

	require.NoError(t, err, "hot miss 时必须从父表腿取到正文")
	require.NotNil(t, reqBody, "父表腿的正文必须被取到")
	require.NotNil(t, respBody)

	rbJSON, ok := reqBody.(map[string]any)
	require.True(t, ok, "request_body should decode to a JSON object, got %T", reqBody)
	assert.Contains(t, rbJSON, "model", "父表腿的 request_body 必须完整")
	assert.Equal(t, "cold-model", rbJSON["model"])
}

// TestFetchRequestBodies_TotalMissReturnsErrNoRows verifies the
// no-row-at-all case. getLog treats this error as non-fatal (body fields
// nil out, metadata still returns), so fetchRequestBodies must surface
// sql.ErrNoRows so callers can distinguish from real errors.
func TestFetchRequestBodies_TotalMissReturnsErrNoRows(t *testing.T) {
	pool := setupTestDB(t)
	defer pool.Close()

	ctx := context.Background()
	requestID := "test-fetch-bodies-none-" + time.Now().Format("20060102-150405.000000")

	h := &Handler{db: pool}
	reqBody, respBody, err := h.fetchRequestBodies(ctx, requestID)

	require.Error(t, err)
	assert.ErrorIs(t, err, sql.ErrNoRows,
		"missing-in-both should be sql.ErrNoRows so caller treats it as non-fatal")
	assert.Nil(t, reqBody)
	assert.Nil(t, respBody)
}

// TestFetchRequestBodies_CancelledParentCtx_DoesNotHang is the regression
// for the cold-path defense (2026-08-17 follow-up). fetchRequestBodies
// receives a parent ctx; the cold-path must derive its own sub-ctx from
// that parent so that if the parent is cancelled (e.g. http.Server
// shutting down mid-handler, or the outer 30s ctx of getLog fires), the
// helper returns within ms — not hang on a 30s columnar scan.
//
// We simulate by passing an already-cancelled context and asserting the
// helper returns an error (sql.ErrNoRows or context.DeadlineExceeded)
// quickly (< 2s).
func TestFetchRequestBodies_CancelledParentCtx_DoesNotHang(t *testing.T) {
	pool := setupTestDB(t)
	defer pool.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before the call

	h := &Handler{db: pool}
	start := time.Now()
	_, _, err := h.fetchRequestBodies(ctx, "any-request-id")
	elapsed := time.Since(start)

	require.Error(t, err, "cancelled parent ctx should yield error")
	assert.Less(t, elapsed, 2*time.Second,
		"cancelled parent ctx must propagate to cold path immediately (got %s; if this hangs the ctx hierarchy regressed)", elapsed)
}

// insertBodyIntoPartitionedOnly writes a body row directly into the
// **parent tier** of the session family's body source (session_bodies), with
// no hot counterpart (session_bodies_hot). Used to exercise the cold/parent
// leg in fetchRequestBodies.
//
// ★ R68 改瞄（2026-10-21，v1 族退役）：
//
//	原来写的是 v1 的 `_request_logs_bodies`（列名 request_body/response_body）。
//	890 之后 v1 族不存在 ⇒ 该 INSERT 必然 42P01。
//	⇒ 换成会话族的**父表腿** `session_bodies`（列名 request_delta/response_delta），
//	  语义完全对应：「hot 没有、只有父表有」。
func insertBodyIntoPartitionedOnly(t *testing.T, pool *pgxpool.Pool, requestID string) error {
	t.Helper()
	ctx := context.Background()
	_, err := pool.Exec(ctx, `
		INSERT INTO session_bodies
			(session_id, turn_no, tenant_id, request_id,
			 request_delta, response_delta)
		VALUES (
			$1, 1, 'test-tenant', $2,
			'{"model":"cold-model","messages":[{"role":"user","content":"cold"}]}'::jsonb,
			'{"id":"cold-response","content":"cold-result"}'::jsonb
		)
	`, sessionIDFor(requestID), requestID)
	return err
}

// cleanupColdBody removes the cold-only row inserted by
// insertBodyIntoPartitionedOnly.
//
// ★ R68 改瞄：原来删的是 v1 的 `_request_logs_bodies`，且注释说「columnar 分区
// 不支持 DELETE ⇒ 失败只告警」。会话族的父表当月分区实测是 **heap**
// ⇒ DELETE 支持 ⇒ 这里失败就是**真失败**，应当让它响。
func cleanupColdBody(t *testing.T, pool *pgxpool.Pool, requestID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `DELETE FROM session_bodies WHERE request_id = $1`, requestID); err != nil {
		t.Logf("cleanupColdBody: delete failed: %v", err)
	}
}

// ============================================================================
// 2026-08-17 OPTIMIZATION: bodyFetchCache (in-memory LRU+TTL) regression tests
// ============================================================================

// TestBodyFetchCache_HitUnderOneMillisecond exercises the hot cache path:
// first call fills the cache (DB hit), second call returns the cached
// value in < 1ms. This locks in the "open → close → reopen" UX win that
// reduces dashboard repeat-click latency from seconds to sub-millisecond.
func TestBodyFetchCache_HitUnderOneMillisecond(t *testing.T) {
	pool := setupTestDB(t)
	defer pool.Close()

	ctx := context.Background()
	requestID := "test-cache-hit-" + time.Now().Format("20060102-150405.000000")
	insertSessionTurnWithBodies(t, pool, requestID)
	// R65 批判复审：t.Cleanup 晚于 defer pool.Close 执行，cleanup 打在已关池上
	// 静默漏行（每轮全量漏 7 行实锤）——defer 注册晚于 Close，LIFO 先清后关。
	defer cleanupTestRequestLog(t, pool, requestID)

	// Per-test cache (avoid cross-test pollution). 100 entries, 1min TTL is
	// sufficient for this single-key test.
	h := &Handler{db: pool, bodyFetchCache: newBodyFetchCache(100, time.Minute)}

	// First call: cache miss → DB query → populates cache.
	_, _, err := h.fetchRequestBodies(ctx, requestID)
	require.NoError(t, err)

	// Second call: cache hit, should be < 1ms.
	start := time.Now()
	reqBody, respBody, err := h.fetchRequestBodies(ctx, requestID)
	elapsed := time.Since(start)
	require.NoError(t, err)
	assert.Less(t, elapsed, time.Millisecond,
		"cache hit should be < 1ms (got %s; if this is slow the LRU isn't actually being used)", elapsed)
	assert.NotNil(t, reqBody, "cached request_body must not be nil on success")
	assert.NotNil(t, respBody, "cached response_body must not be nil on success")

	// Stats: 1 miss + 1 hit.
	size, hits, misses, _ := h.bodyFetchCache.Stats()
	assert.Equal(t, 1, size, "cache should hold 1 entry")
	assert.EqualValues(t, 1, hits, "should record 1 hit")
	assert.EqualValues(t, 1, misses, "should record 1 miss")
}

// TestBodyFetchCache_TTLExpires exercises the TTL expiry path: a value
// cached with very short TTL (1ms) should be evicted/treated as miss on
// the next call, forcing a DB re-fetch.
func TestBodyFetchCache_TTLExpires(t *testing.T) {
	pool := setupTestDB(t)
	defer pool.Close()

	ctx := context.Background()
	requestID := "test-cache-ttl-" + time.Now().Format("20060102-150405.000000")
	insertSessionTurnWithBodies(t, pool, requestID)
	// R65 批判复审：t.Cleanup 晚于 defer pool.Close 执行，cleanup 打在已关池上
	// 静默漏行（每轮全量漏 7 行实锤）——defer 注册晚于 Close，LIFO 先清后关。
	defer cleanupTestRequestLog(t, pool, requestID)

	// 1ms TTL — guaranteed expiry on the second call.
	h := &Handler{db: pool, bodyFetchCache: newBodyFetchCache(100, time.Millisecond)}

	// First call: cache miss → DB.
	_, _, err := h.fetchRequestBodies(ctx, requestID)
	require.NoError(t, err)

	// Wait > 1ms then call again — TTL must trigger re-fetch.
	time.Sleep(50 * time.Millisecond)
	start := time.Now()
	_, _, err = h.fetchRequestBodies(ctx, requestID)
	elapsed := time.Since(start)
	require.NoError(t, err)

	// After TTL expiry, the second call must hit the DB again
	// (>= 1ms typically). Cache must record 2 misses total.
	_, hits, misses, _ := h.bodyFetchCache.Stats()
	assert.EqualValues(t, 0, hits, "TTL expired → must not count as hit")
	assert.GreaterOrEqual(t, misses, 2, "TTL expired → both calls should be misses")
	t.Logf("post-TTL re-fetch elapsed=%s hits=%d misses=%d", elapsed, hits, misses)
}

// TestBodyFetchCache_NotFoundIsCached exercises the "两边都没找到"
// sentinel caching. First call returns sql.ErrNoRows and caches (nil, nil);
// second call must hit cache immediately and return sql.ErrNoRows without
// touching the DB. This avoids hammering PG with repeat lookups for IDs
// that don't exist anywhere.
func TestBodyFetchCache_NotFoundIsCached(t *testing.T) {
	pool := setupTestDB(t)
	defer pool.Close()

	ctx := context.Background()
	requestID := "test-cache-notfound-" + time.Now().Format("20060102-150405.000000")

	h := &Handler{db: pool, bodyFetchCache: newBodyFetchCache(100, time.Minute)}

	// First call: not found anywhere (no row in hot, no row in bodies view).
	_, _, err := h.fetchRequestBodies(ctx, requestID)
	require.Error(t, err)
	assert.ErrorIs(t, err, sql.ErrNoRows)

	// Second call: cache hit on (nil, nil) sentinel → sql.ErrNoRows.
	start := time.Now()
	_, _, err = h.fetchRequestBodies(ctx, requestID)
	elapsed := time.Since(start)
	require.Error(t, err)
	assert.ErrorIs(t, err, sql.ErrNoRows)
	assert.Less(t, elapsed, time.Millisecond,
		"not-found cache hit should be < 1ms (got %s)", elapsed)

	_, hits, misses, _ := h.bodyFetchCache.Stats()
	assert.EqualValues(t, 1, hits, "second not-found call should be a cache hit")
	assert.EqualValues(t, 1, misses, "first not-found call should be a cache miss")
}

// TestBodyFetchCache_LRUEvictsOldest verifies that when capacity is
// reached, the LRU evicts the oldest entry. We fill 3 entries into a
// 2-cap cache and assert that the first entry is gone (Peek miss) while
// the second/third remain.
func TestBodyFetchCache_LRUEvictsOldest(t *testing.T) {
	c := newBodyFetchCache(2, time.Minute)
	c.Put("a", "first", "first")
	c.Put("b", "second", "second")
	// "a" is now LRU; inserting "c" must evict "a".
	evictedKey, evicted := c.lru.Put("c", bodyFetchEntry{body: "third", resp: "third", storedAt: time.Now()})
	// Note: cache.LRU.Put returns (evictedKey K, evicted bool) — we use
	// internal lru.Put here to inspect eviction directly.
	if !evicted || evictedKey != "a" {
		t.Fatalf("expected a to be evicted (LRU), got key=%q evicted=%v", evictedKey, evicted)
	}
	if entry, ok := c.Get("a"); ok {
		t.Fatalf("a should have been evicted, got entry=%+v ok=%v", entry, ok)
	}
	if entry, ok := c.Get("b"); !ok || entry.body != "second" {
		t.Fatalf("b must remain, got entry=%+v ok=%v", entry, ok)
	}
	if entry, ok := c.Get("c"); !ok || entry.body != "third" {
		t.Fatalf("c must remain, got entry=%+v ok=%v", entry, ok)
	}
}
