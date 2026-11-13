package v2

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// cache_v2_invalidation_bridge_test.go —— session 缓存跨实例失效桥的钉测
// （F-47 收口，2026-11-13）。照抄 provider/candidate_cache_invalidation_test.go
// 的钉法：**不是**「函数被调用了」，而是**跨进程语义真的成立**：
//
//	① A 实例 Set 新状态（本地写 + 广播）
//	② B 实例（同一 miniredis、独立 L1/L1.5）**必须自己把旧状态删掉**
//	③ B 收到事件后**不得再广播**（订阅端只调 invalidateStaleLocal，结构上无回环）
//	④ B 只清本地两层 —— **L2 必须保留** A 刚写的新治理状态
//	⑤ 载荷不可解析 ⇒ 跳过该条，不阻断通道（后续事件仍要生效）
//
// ★ 为什么必须用真 Redis（miniredis）：直接调订阅端函数只能证明
//
//	「函数能删缓存」，证明不了「事件真的从 A 到了 B」⇒ 缺覆盖的全绿。
func TestSessionCacheInvalidationPayloadRoundTrip(t *testing.T) {
	if got := encodeSessionCacheInvalidation("tenant-a", "sess-1"); got != "tenant-a\x1fsess-1" {
		t.Errorf("encode: got %q", got)
	}
	if got := encodeSessionCacheInvalidation("", ""); got != sessionCacheInvalidationAll {
		t.Errorf("encode all: got %q", got)
	}
	tenant, session, ok := decodeSessionCacheInvalidation("tenant-a\x1fsess-1")
	if !ok || tenant != "tenant-a" || session != "sess-1" {
		t.Errorf("decode: got (%q,%q,%v)", tenant, session, ok)
	}
	tenant, session, ok = decodeSessionCacheInvalidation(sessionCacheInvalidationAll)
	if !ok || tenant != "" || session != "" {
		t.Errorf("decode all: got (%q,%q,%v)", tenant, session, ok)
	}
	for _, dirty := range []string{"", "no-separator", "\x1f", "a\x1f"} {
		if _, _, ok := decodeSessionCacheInvalidation(dirty); ok {
			t.Errorf("★ 脏载荷 %q 必须被拒（跳过不阻断）", dirty)
		}
	}
}

// newBridgedSessionCachePair 造两个「各持独立 L1、共享同一 Redis（miniredis）」
// 的 SessionCacheV2（= 两个进程各起一份的最小形态），各自启动桥。
func newBridgedSessionCachePair(t *testing.T) (a, b *SessionCacheV2, mr *miniredis.Miniredis) {
	t.Helper()
	mr = miniredis.RunT(t)
	a = &SessionCacheV2{
		l1: NewCompressionMetaCache(64),
		l2: NewRedisGovernanceCache(mr.Addr(), 0, 0),
	}
	b = &SessionCacheV2{
		l1: NewCompressionMetaCache(64),
		l2: NewRedisGovernanceCache(mr.Addr(), 0, 0),
	}
	return a, b, mr
}

func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(msg)
}

func TestSessionCacheInvalidationBridgeCrossInstance(t *testing.T) {
	a, b, mr := newBridgedSessionCachePair(t)

	// ★ 编排照抄 candidate 桥钉测：桥绑定到 **B（远端实例）**——
	//   订阅端清的是它服务的那份缓存；A 没有「自己的桥」，A 的写路径
	//   （Set/Invalidate）经进程内活桥广播（本进程只有一个桥 ⇒ 不会自环）。
	StartSessionCacheInvalidationBridge(context.Background(), b, redis.NewClient(&redis.Options{Addr: mr.Addr()}))
	t.Cleanup(StopSessionCacheInvalidationBridge)
	waitFor(t, func() bool {
		return mr.PubSubNumSub(sessionCacheInvalidationChannel)[sessionCacheInvalidationChannel] == 1
	}, "subscriber did not subscribe to the session invalidation channel")

	// B 预置一条**旧状态**（另一实例今天会脏读 30min 的现场）。
	stale := &SessionStateV2{TenantID: "tenant-a", SessionID: "sess-1"}
	stale.CompressionMeta.SummaryMarker = "stale-from-yesterday"
	b.l1.Set(stale)

	// A（另一个「进程」）写新状态 ⇒ 广播 ⇒ B 必须自行清掉旧 L1 条目。
	fresh := &SessionStateV2{TenantID: "tenant-a", SessionID: "sess-1"}
	fresh.CompressionMeta.SummaryMarker = "fresh-from-a"
	if err := a.Set(context.Background(), fresh); err != nil {
		t.Fatalf("A.Set: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b.l1.Get("tenant-a", "sess-1") == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := b.l1.Get("tenant-a", "sess-1"); got != nil {
		t.Fatal("★ B 实例的旧 L1 条目未被 A 的写广播清掉 —— 跨实例失效语义不成立（今天 30min 脏读的现场）")
	}

	// ④ B 只清本地两层：A 写进 L2 的治理状态必须保留（订阅端不得删 L2）。
	l2key := redisKeyV2("tenant-a", "sess-1")
	if exists := mr.Exists(l2key); !exists {
		t.Fatalf("★ B 收到失效事件后 L2 键 %s 丢失（订阅端清了共享源）", l2key)
	}
}

func TestSessionCacheInvalidationBridgeDirtyPayloadDoesNotBreakChannel(t *testing.T) {
	a, b, mr := newBridgedSessionCachePair(t)
	stale := &SessionStateV2{TenantID: "tenant-a", SessionID: "sess-2"}
	b.l1.Set(stale)

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	StartSessionCacheInvalidationBridge(context.Background(), b, rdb)
	t.Cleanup(StopSessionCacheInvalidationBridge)
	waitFor(t, func() bool {
		return mr.PubSubNumSub(sessionCacheInvalidationChannel)[sessionCacheInvalidationChannel] == 1
	}, "subscriber did not subscribe to the session invalidation channel")

	// 先灌一条脏载荷，再发一条有效载荷 —— 后者必须仍然生效。
	if err := rdb.Publish(context.Background(), sessionCacheInvalidationChannel, "garbage-no-separator").Err(); err != nil {
		t.Fatalf("publish dirty: %v", err)
	}
	fresh := &SessionStateV2{TenantID: "tenant-a", SessionID: "sess-2"}
	if err := a.Set(context.Background(), fresh); err != nil {
		t.Fatalf("A.Set: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b.l1.Get("tenant-a", "sess-2") == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := b.l1.Get("tenant-a", "sess-2"); got != nil {
		t.Fatal("★ 脏载荷打断了通道 —— 后续有效失效事件未生效（脏数据不该打挂整条通道）")
	}
}

func TestSessionCacheInvalidationPublishIsNoopWithoutBridge(t *testing.T) {
	StopSessionCacheInvalidationBridge() // 确保本测试进程内无活桥
	c := &SessionCacheV2{l1: NewCompressionMetaCache(8)}
	state := &SessionStateV2{TenantID: "t", SessionID: "s"}
	if err := c.Set(context.Background(), state); err != nil {
		t.Fatalf("无桥 Set 必须照常成功: %v", err)
	}
	if err := c.Invalidate(context.Background(), "t", "s"); err != nil {
		t.Fatalf("无桥 Invalidate 必须照常成功: %v", err)
	}
}

func TestSessionCacheInvalidateAlsoBroadcasts(t *testing.T) {
	a, b, mr := newBridgedSessionCachePair(t)
	stale := &SessionStateV2{TenantID: "tenant-a", SessionID: "sess-3"}
	b.l1.Set(stale)

	StartSessionCacheInvalidationBridge(context.Background(), b, redis.NewClient(&redis.Options{Addr: mr.Addr()}))
	t.Cleanup(StopSessionCacheInvalidationBridge)
	waitFor(t, func() bool {
		return mr.PubSubNumSub(sessionCacheInvalidationChannel)[sessionCacheInvalidationChannel] == 1
	}, "subscriber did not subscribe to the session invalidation channel")

	if err := a.Invalidate(context.Background(), "tenant-a", "sess-3"); err != nil {
		t.Fatalf("A.Invalidate: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b.l1.Get("tenant-a", "sess-3") == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := b.l1.Get("tenant-a", "sess-3"); got != nil {
		t.Fatal("★ B 实例的 L1 条目未被 A 的 Invalidate 广播清掉")
	}
}

// TestSessionCacheInvalidationNoLoopOnReceive 钉「无回环」：
// 订阅端收到事件后只做本地失效，不得再广播出去（照抄 candidate 同款钉法）。
func TestSessionCacheInvalidationNoLoopOnReceive(t *testing.T) {
	_, b, mr := newBridgedSessionCachePair(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	StartSessionCacheInvalidationBridge(context.Background(), b, rdb)
	t.Cleanup(StopSessionCacheInvalidationBridge)
	waitFor(t, func() bool {
		return mr.PubSubNumSub(sessionCacheInvalidationChannel)[sessionCacheInvalidationChannel] == 1
	}, "subscriber did not subscribe to the session invalidation channel")

	// 外部注入一条事件（模拟 A 实例广播）。
	if err := rdb.Publish(context.Background(), sessionCacheInvalidationChannel, "tenant-a\x1fsess-9").Err(); err != nil {
		t.Fatalf("publish failed: %v", err)
	}
	waitFor(t, func() bool {
		return b.l1.Get("tenant-a", "sess-9") == nil
	}, "remote instance did not apply the received invalidation")

	// ★ 无回环：队列里不得堆积本进程发出的广播。
	br := sessionInvalidationActive.Load()
	if br == nil {
		t.Fatal("bridge is not running")
	}
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if len(br.queue) != 0 {
			t.Fatalf("subscriber re-broadcast the received event: queue length = %d", len(br.queue))
		}
		time.Sleep(10 * time.Millisecond)
	}
}
