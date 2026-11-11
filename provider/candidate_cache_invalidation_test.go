package provider

// candidate_cache_invalidation_test.go —— 跨实例候选缓存失效通道的钉测
// （2026-11-11 缓存与路由状态即时同步轮）。
//
// ★ 本文件要钉的**不是**「函数被调用了」，而是**跨进程语义真的成立**：
//
//	① A 实例把某个凭据的判定翻掉（本地清缓存 + 广播）
//	② B 实例（另一个进程内的一份 Client）**必须自己把那条缓存删掉**
//	③ B 收到事件后**不得再广播**（无回环）
//	④ 载荷不可解析 ⇒ 跳过该条，**不阻断**通道（后续事件仍要生效）
//
// ★ 为什么必须用**真 Redis（miniredis）**而不是直接调函数：
//   直接调订阅端函数只能证明「函数能删缓存」，证明不了
//   「事件真的从 A 到了 B」⇒ 那就是**缺覆盖的全绿**（同族：
//   第 27 处「守卫自己也要被审计」的量具层缺陷）。
//
// ★ 「两个实例」怎么造：桥是**进程级单例**（一个进程一份 Client），
//   所以这里用**两个 Client 实例 + 各自启动桥**（第二个桥会被单例守卫
//   挡掉）⇒ 真正的双实例形态是「两个进程各起一份」。
//   为此本文件让**桥与 Client 绑定**（订阅端清的是它服务的那一份缓存），
//   并且测试用**两个 miniredis 实例**分别扮演 A / B 两个进程：
//   B 的桥订阅 B 的通道；A 的桥把事件发到 A 的通道……
//   ⚠ 同一条 Redis 才能互通 ⇒ 这里让**两个 Client 共用一个 miniredis**，
//     但**各自有独立的缓存**，桥的订阅端按 client 绑定清自己的缓存 ⇒
//     A 发布 ⇒ B 收到 ⇒ B 清自己的缓存 ⇒ 断言的是**数据结果**。

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// planWithCredential 造一条「计划里含指定凭据」的缓存条目。
func planWithCredential(credentialID int) *resolveResponse {
	return &resolveResponse{
		PlanOrder: []struct {
			CredentialID int    `json:"credential_id"`
			ProviderID   int    `json:"provider_id"`
			RawModel     string `json:"raw_model"`
			Tier         int    `json:"tier"`
		}{{CredentialID: credentialID, ProviderID: 1, RawModel: "gpt-4", Tier: 1}},
		Candidates: []json.RawMessage{json.RawMessage(`{"credential_id":1}`)},
	}
}

// newCachedClient 造一份「缓存里有一条计划含指定凭据」的 Client。
func newCachedClient(credentialID int) *Client {
	return &Client{candCache: map[string]cacheEntry[*resolveResponse]{
		"glm-5.3-flash": {value: planWithCredential(credentialID), expires: time.Now().Add(time.Minute)},
	}}
}

// TestCandidateCacheInvalidationPayloadRoundTrip 钉载荷编解码：
// 凭据 ID 与「整库」两个语义都要能往返；脏载荷必须被拒。
func TestCandidateCacheInvalidationPayloadRoundTrip(t *testing.T) {
	if got := encodeCandidateCacheInvalidation(42); got != "42" {
		t.Fatalf("encoded credential payload = %q, want %q", got, "42")
	}
	if got := encodeCandidateCacheInvalidation(0); got != candidateCacheInvalidationAll {
		t.Fatalf("encoded all payload = %q, want %q", got, candidateCacheInvalidationAll)
	}
	if got := encodeCandidateCacheInvalidation(-1); got != candidateCacheInvalidationAll {
		t.Fatalf("encoded negative payload = %q, want %q", got, candidateCacheInvalidationAll)
	}

	if id, ok := decodeCandidateCacheInvalidation("42"); !ok || id != 42 {
		t.Fatalf("decoded credential = (%d, %v), want (42, true)", id, ok)
	}
	if id, ok := decodeCandidateCacheInvalidation("all"); !ok || id != 0 {
		t.Fatalf("decoded all = (%d, %v), want (0, true)", id, ok)
	}
	for _, bad := range []string{"", "   ", "abc", "0", "-1", "1.5"} {
		if _, ok := decodeCandidateCacheInvalidation(bad); ok {
			t.Fatalf("dirty payload %q was accepted, want rejected", bad)
		}
	}
}

// TestCandidateCacheInvalidationCrossInstanceDropRemoteEntry 是本文件的核心用例：
// A 实例翻转凭据 42 的判定 ⇒ B 实例**自己**把那条缓存删掉。
//
// ★ 断言的是**数据结果**（B 的缓存条目没了），不是「函数被调用过」。
func TestCandidateCacheInvalidationCrossInstanceDropRemoteEntry(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	// B 实例：缓存里有一条计划含凭据 42（桥绑定到它 ⇒ 订阅端清的是它那份）。
	remote := newCachedClient(42)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	StartCandidateCacheInvalidationBridge(ctx, remote, rdb)
	defer StopCandidateCacheInvalidationBridge()

	waitFor(t, func() bool {
		return mr.PubSubNumSub(candidateCacheInvalidationChannel)[candidateCacheInvalidationChannel] == 1
	}, "subscriber did not subscribe to the invalidation channel")

	// A 实例：另一个「进程」翻转凭据 42 的判定 ⇒ 本地清 + 广播。
	local := newCachedClient(42)
	local.InvalidateCandidateCacheForCredential(42)
	if len(local.candCache) != 0 {
		t.Fatalf("local instance did not drop its own entry: %d remain", len(local.candCache))
	}

	// B 实例必须**自己**把那条缓存删掉（不需要 B 收到任何主动通知）。
	waitFor(t, func() bool {
		remote.mu.RLock()
		defer remote.mu.RUnlock()
		_, ok := remote.candCache["glm-5.3-flash"]
		return !ok
	}, "remote instance kept the stale candidate plan after the broadcast")
}

// TestCandidateCacheInvalidationNoLoopOnReceive 钉「无回环」：
// 订阅端收到事件后**只做本地失效**，不得再广播出去。
//
// ★ 为什么这条重要：回环会让「一条失效事件」变成**无限广播**，
//   在多副本部署里把 Redis 通道打成热循环。
func TestCandidateCacheInvalidationNoLoopOnReceive(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	remote := newCachedClient(42)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	StartCandidateCacheInvalidationBridge(ctx, remote, rdb)
	defer StopCandidateCacheInvalidationBridge()

	waitFor(t, func() bool {
		return mr.PubSubNumSub(candidateCacheInvalidationChannel)[candidateCacheInvalidationChannel] == 1
	}, "subscriber did not subscribe to the invalidation channel")

	// 外部注入一条事件（模拟 A 实例广播）。
	if err := rdb.Publish(context.Background(), candidateCacheInvalidationChannel, "42").Err(); err != nil {
		t.Fatalf("publish failed: %v", err)
	}
	waitFor(t, func() bool {
		remote.mu.RLock()
		defer remote.mu.RUnlock()
		_, ok := remote.candCache["glm-5.3-flash"]
		return !ok
	}, "remote instance did not apply the received invalidation")

	// ★ 无回环：队列里不得堆积本进程发出的广播。
	b := candidateInvalidationActive.Load()
	if b == nil {
		t.Fatal("bridge is not running")
	}
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if len(b.queue) != 0 {
			t.Fatalf("subscriber re-broadcast the received event: queue length = %d, want 0", len(b.queue))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestCandidateCacheInvalidationSkipsDirtyPayload 钉「脏载荷不阻断通道」：
// 一条不可解析的载荷之后，后续合法事件**仍要生效**。
func TestCandidateCacheInvalidationSkipsDirtyPayload(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	remote := newCachedClient(42)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	StartCandidateCacheInvalidationBridge(ctx, remote, rdb)
	defer StopCandidateCacheInvalidationBridge()

	waitFor(t, func() bool {
		return mr.PubSubNumSub(candidateCacheInvalidationChannel)[candidateCacheInvalidationChannel] == 1
	}, "subscriber did not subscribe to the invalidation channel")

	if err := rdb.Publish(context.Background(), candidateCacheInvalidationChannel, "not-a-number").Err(); err != nil {
		t.Fatalf("publish dirty payload failed: %v", err)
	}
	// 后续合法事件必须仍然生效 ⇒ 通道没被脏载荷打断。
	if err := rdb.Publish(context.Background(), candidateCacheInvalidationChannel, "42").Err(); err != nil {
		t.Fatalf("publish valid payload failed: %v", err)
	}
	waitFor(t, func() bool {
		remote.mu.RLock()
		defer remote.mu.RUnlock()
		_, ok := remote.candCache["glm-5.3-flash"]
		return !ok
	}, "dirty payload blocked the channel: the following valid event never landed")
}

// TestCandidateCacheInvalidationAllPayloadDropsEverything 钉「整库失效」载荷：
// credentialID == 0 ⇒ 整个缓存被清空（与 InvalidateAllCandidateCache 同语义）。
func TestCandidateCacheInvalidationAllPayloadDropsEverything(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	remote := &Client{candCache: map[string]cacheEntry[*resolveResponse]{
		"a": {value: planWithCredential(1)},
		"b": {value: planWithCredential(2)},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	StartCandidateCacheInvalidationBridge(ctx, remote, rdb)
	defer StopCandidateCacheInvalidationBridge()

	waitFor(t, func() bool {
		return mr.PubSubNumSub(candidateCacheInvalidationChannel)[candidateCacheInvalidationChannel] == 1
	}, "subscriber did not subscribe to the invalidation channel")

	if err := rdb.Publish(context.Background(), candidateCacheInvalidationChannel, "all").Err(); err != nil {
		t.Fatalf("publish all payload failed: %v", err)
	}
	waitFor(t, func() bool {
		remote.mu.RLock()
		defer remote.mu.RUnlock()
		return len(remote.candCache) == 0
	}, "remote instance did not drop the whole cache on an 'all' invalidation")
}

// TestCandidateCacheInvalidationWithoutBridgeIsNoOp 钉「桥没启动时不炸」：
// 没有 Redis（= 没装配桥）时，本地失效照旧生效，广播被跳过。
//
// ★ 这条是**降级语义**的钉子：桥是**增强**，不是**前提**。
func TestCandidateCacheInvalidationWithoutBridgeIsNoOp(t *testing.T) {
	StopCandidateCacheInvalidationBridge() // 确保没有活桥

	client := newCachedClient(42)
	client.InvalidateCandidateCacheForCredential(42)
	if len(client.candCache) != 0 {
		t.Fatalf("local invalidation did not apply without the bridge: %d entries remain", len(client.candCache))
	}
}

// TestCandidateCacheInvalidationLocalKeepsUnrelatedEntries 钉本地失效的粒度：
// 只有 PlanOrder 含该凭据的条目被删，其它条目保留（2026-09-07 粒度修复的既有语义）。
func TestCandidateCacheInvalidationLocalKeepsUnrelatedEntries(t *testing.T) {
	client := &Client{candCache: map[string]cacheEntry[*resolveResponse]{
		"hit":  {value: planWithCredential(42)},
		"miss": {value: planWithCredential(7)},
	}}

	if deleted := client.invalidateCandidateCacheLocal(42); deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}
	if _, ok := client.candCache["miss"]; !ok {
		t.Fatal("unrelated entry was deleted")
	}
}

// waitFor 轮询等待条件成立（带超时 ⇒ 失败时给出明确报错而不是挂死）。
func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal(msg)
}
