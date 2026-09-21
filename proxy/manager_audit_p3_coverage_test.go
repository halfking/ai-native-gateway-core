package proxy

// manager_audit_p3_coverage_test.go — R4 deferred #14（覆盖测试）收口。
//
// 补齐 handoff（/tmp/handoff-20260909-195000.md）明确列出的三个未覆盖场景：
//  1. cache TTL 过期路径：过期快照仍可服务本次请求（stale-while-refresh），
//     fresh=false，且异步刷新经 cacheRefreshes 去重恰好触发一次；
//  2. HealthCheckSubscription 全节点失败：summary 报告 Failed=N / OK=0，
//     AvgMs 保持 0（除零守卫），每个节点的失败计数各 +1 并落库；
//  3. ForceSwap 无备用节点：返回 no-alternative 错误，当前 active 的
//     consecutiveFails 保持不重置——故障证据不得被失败的切换尝试抹掉。

import (
	"context"
	"strings"
	"testing"
	"time"
)

// countingListStore 在 fakeStoreForBans 上加 ListNodes 调用计数，
// 用于断言 TTL 过期后异步刷新确实回源了一次。
type countingListStore struct {
	fakeStoreForBans
	listCalls int
}

func (c *countingListStore) ListNodes(ctx context.Context, subscriptionID *int) ([]*Node, error) {
	c.listCalls++
	return c.fakeStoreForBans.ListNodes(ctx, subscriptionID)
}

// TestCacheTTLExpiryServesStaleAndRefreshes（#14-1）：TTL 过期后
// getNodesFromCacheWithTTL 必须返回 fresh=false 但仍提供旧快照供本次请求
// 使用（stale-while-refresh 契约），同时触发一次异步刷新；刷新经
// cacheRefreshes 去重不会产生 DB 惊群，完成后缓存恢复 fresh。
func TestCacheTTLExpiryServesStaleAndRefreshes(t *testing.T) {
	store := &countingListStore{
		fakeStoreForBans: fakeStoreForBans{
			subs:  []*Subscription{{ID: 1, Status: "active"}},
			nodes: []*Node{{ID: 1, SubscriptionID: 1, Name: "n", Protocol: ProtocolHTTP, Server: "1.1.1.1", Port: 80, Status: "active"}},
		},
	}
	mgr := NewManager(store, nil, nil)
	mgr.cacheTTL = 60 * time.Millisecond

	if !mgr.loadNodesIntoCache(context.Background(), 1) {
		t.Fatal("loadNodesIntoCache failed")
	}
	listCallsAfterLoad := store.listCalls
	if listCallsAfterLoad == 0 {
		t.Fatal("initial load must read from store")
	}

	// 未过期：fresh=true、covered=true、快照完整。
	nodes, fresh, present, covered := mgr.getNodesFromCacheWithTTL(1, time.Now())
	if !present || !fresh || !covered || len(nodes) != 1 {
		t.Fatalf("fresh entry: present=%v fresh=%v covered=%v len=%d, want true/true/true/1", present, fresh, covered, len(nodes))
	}

	// 等待 TTL 过期。
	time.Sleep(90 * time.Millisecond)

	// 过期后：fresh=false，但旧快照仍可服务本次请求。
	nodes, fresh, present, covered = mgr.getNodesFromCacheWithTTL(1, time.Now())
	if !present || fresh || !covered {
		t.Fatalf("expired entry: present=%v fresh=%v covered=%v, want true/false/true", present, fresh, covered)
	}
	if len(nodes) != 1 {
		t.Fatalf("expired entry must still serve the stale snapshot, got %d nodes", len(nodes))
	}

	// 异步刷新已被触发：等待其完成（cacheRefreshes 键被删除）。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, inflight := mgr.cacheRefreshes.Load(1); !inflight {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, inflight := mgr.cacheRefreshes.Load(1); inflight {
		t.Fatal("async refresh still in flight after 2s")
	}
	if store.listCalls <= listCallsAfterLoad {
		t.Fatalf("ListNodes calls = %d, want > %d (async refresh must re-read the store)", store.listCalls, listCallsAfterLoad)
	}

	// 刷新完成后缓存恢复 fresh（若测试耗时导致再次过期，则再等一轮刷新完成）。
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, fresh, _, _ = mgr.getNodesFromCacheWithTTL(1, time.Now()); fresh {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !fresh {
		t.Fatal("cache entry never returned to fresh after refresh")
	}
}

// TestHealthCheckSubscriptionNowAllNodesFail（#14-2）：订阅批量探活全部
// 失败时，summary 必须报告 Failed=N / OK=0，AvgMs 保持 0（OK==0 除零守卫），
// 且每个节点的连续失败计数各 +1、LastHealthCheckStatus 落为 failed。
func TestHealthCheckSubscriptionNowAllNodesFail(t *testing.T) {
	store := &fakeStoreForManager{
		fakeStoreForBans: fakeStoreForBans{
			subs: []*Subscription{{ID: 1, Status: "active"}},
			nodes: []*Node{
				{ID: 1, SubscriptionID: 1, Name: "a", Protocol: ProtocolHTTP, Server: "1.1.1.1", Port: 80, Status: "active"},
				{ID: 2, SubscriptionID: 1, Name: "b", Protocol: ProtocolHTTP, Server: "1.1.1.2", Port: 80, Status: "active"},
			},
		},
	}
	mgr := NewManager(store, nil, &failingChecker{})

	summary, err := mgr.HealthCheckSubscriptionNow(context.Background(), 1)
	if err != nil {
		t.Fatalf("HealthCheckSubscriptionNow: %v", err)
	}
	if summary.Total != 2 || summary.OK != 0 || summary.Failed != 2 {
		t.Fatalf("summary = %+v, want Total=2 OK=0 Failed=2 (all nodes failed)", summary)
	}
	if summary.AvgMs != 0 {
		t.Fatalf("summary.AvgMs = %d, want 0 when OK==0 (division guard)", summary.AvgMs)
	}
	for _, n := range store.nodes {
		if n.ConsecutiveFailures != 1 {
			t.Fatalf("node %d ConsecutiveFailures = %d, want 1", n.ID, n.ConsecutiveFailures)
		}
		if n.LastHealthCheckStatus != "failed" {
			t.Fatalf("node %d LastHealthCheckStatus = %q, want %q", n.ID, n.LastHealthCheckStatus, "failed")
		}
	}
	if store.updateNodeCalls != 2 {
		t.Fatalf("UpdateNode calls = %d, want 2 (one per failed node)", store.updateNodeCalls)
	}
}

// TestForceSwapNoBackupPreservesConsecutiveFails（#14-3）：当前节点是唯一
// 节点时 ForceSwap 必须返回 no-alternative 错误，且 active selection 保持
// 原节点与累计连续失败计数——失败证据不得被一次失败的切换尝试重置。
func TestForceSwapNoBackupPreservesConsecutiveFails(t *testing.T) {
	store := &fakeStoreForBans{
		subs:  []*Subscription{{ID: 1, Status: "active"}},
		nodes: []*Node{{ID: 1, SubscriptionID: 1, Name: "only", Protocol: ProtocolHTTP, Server: "127.0.0.1", Port: 8007, Status: "active", Location: "HK"}},
	}
	mgr := NewManager(store, nil, &stubHealthChecker{})

	if _, err := mgr.SelectBestNode(context.Background(), intPtr(1)); err != nil {
		t.Fatalf("initial select: %v", err)
	}

	// 模拟两次探测失败（默认 SwapFailureThreshold=2）。
	mgr.recordProbeResult(intPtr(1), 1, false)
	mgr.recordProbeResult(intPtr(1), 1, false)

	_, err := mgr.ForceSwap(context.Background(), intPtr(1))
	if err == nil {
		t.Fatal("ForceSwap must fail when the current node is the only node")
	}
	if !strings.Contains(err.Error(), "no alternative proxy node") {
		t.Fatalf("ForceSwap error = %v, want no-alternative-proxy-node error", err)
	}

	mgr.activeMu.Lock()
	sel := mgr.active[selectionKey(intPtr(1))]
	mgr.activeMu.Unlock()
	if sel == nil {
		t.Fatal("active selection must be preserved when no backup node exists")
	}
	if sel.nodeID != 1 {
		t.Fatalf("active nodeID = %d, want 1 (must keep the failing node rather than clear state)", sel.nodeID)
	}
	if sel.consecutiveFails != 2 {
		t.Fatalf("active consecutiveFails = %d, want 2 (failed swap must not reset the failure counter)", sel.consecutiveFails)
	}
}
