package proxy

import (
	"context"
	"errors"
	"testing"
	"time"
)

// R4 audit tests — P1 items not yet covered.
// Tests in this file focus on:
//   - Partial-cache coverage: global SelectBestNode with cache containing
//     only one subscription must still return nodes for other subscriptions
//     (because the implementation falls back to ListNodes(nil)).
//   - End-to-end swap path: HealthCheckNode failure → swapProbeOne →
//     recordProbeResult → ForceSwap selecting a different node.
//   - Password-decryption skip on HealthCheckNode (single-node path).
//   - Different HealthCheckURLs producing distinct dedup groups
//     (proxyURL + "\x00" + HealthCheckURL).
//   - Stop cancels a slow swapProbeOne (use a stuck HealthChecker.Check).

// TestR4PartialCacheCoverageSelectBestNode 全球 SelectBestNode 在缓存只覆盖一个
// 订阅时，仍能命中其它订阅的节点（实现已经回退到 ListNodes(nil)）。
func TestR4PartialCacheCoverageSelectBestNode(t *testing.T) {
	store := &fakeStoreForBans{
		subs: []*Subscription{
			{ID: 1, Status: "active"},
			{ID: 2, Status: "active"},
		},
		nodes: []*Node{
			{ID: 11, SubscriptionID: 1, Name: "A1", Protocol: ProtocolHTTP, Server: "1.1.1.1", Port: 80, Status: "active", Location: "HK"},
			{ID: 22, SubscriptionID: 2, Name: "B1", Protocol: ProtocolHTTP, Server: "2.2.2.2", Port: 80, Status: "active", Location: "JP"},
		},
	}
	mgr := NewManager(store, nil, nil)
	mgr.refreshAllSubscriptionBans(context.Background())
	// 仅缓存订阅 1 的节点；订阅 2 必须通过 ListNodes(nil) 回退命中。
	if !mgr.loadNodesIntoCache(context.Background(), 1) {
		t.Fatalf("loadNodesIntoCache returned false")
	}

	got, err := mgr.SelectBestNode(context.Background(), nil)
	if err != nil {
		t.Fatalf("SelectBestNode: %v", err)
	}
	if got == nil || got.SubscriptionID == 0 || got.SubscriptionID != 1 && got.SubscriptionID != 2 {
		t.Fatalf("SelectBestNode picked node with sub_id=%d, want 1 or 2", got.SubscriptionID)
	}
	// 强制让"subscription 2 only"路径覆盖：把订阅 1 全部标记为 unhealthy 并
	// 加大 threshold；SelectBestNode 必须仍能命中订阅 2 节点。
	for _, n := range store.nodes {
		if n.SubscriptionID == 1 {
			n.Status = "unhealthy"
			n.ConsecutiveFailures = 999
		}
	}
	if !mgr.loadNodesIntoCache(context.Background(), 1) {
		t.Fatalf("reload cache returned false")
	}
	got, err = mgr.SelectBestNode(context.Background(), nil)
	if err != nil {
		t.Fatalf("SelectBestNode fallback: %v", err)
	}
	if got == nil || got.SubscriptionID != 2 {
		t.Fatalf("fallback SelectBestNode picked %+v, want subscription 2 node", got)
	}
}

// TestR4SwapPathEndToEnd HealthCheckNode 失败 → swapProbeOne → recordProbeResult
// → ForceSwap 选择另一个节点的端到端路径。
func TestR4SwapPathEndToEnd(t *testing.T) {
	store := &fakeStoreForBans{
		subs: []*Subscription{{ID: 1, Status: "active"}},
		nodes: []*Node{
			{ID: 1, SubscriptionID: 1, Name: "primary", Protocol: ProtocolHTTP, Server: "127.0.0.1", Port: 9001, Status: "active", Location: "HK", ResponseTimeMs: 10},
			{ID: 2, SubscriptionID: 1, Name: "backup", Protocol: ProtocolHTTP, Server: "127.0.0.1", Port: 9002, Status: "active", Location: "JP", ResponseTimeMs: 50},
		},
	}
	checker := &stubHealthChecker{results: map[int]HealthCheckResult{
		1: {NodeID: 1, OK: false, Err: errors.New("dial timeout"), Latency: 0, CheckedAt: time.Now()},
		2: {NodeID: 2, OK: true, Latency: 10, CheckedAt: time.Now()},
	}}
	mgr := NewManager(store, nil, checker)
	mgr.refreshAllSubscriptionBans(context.Background())
	// threshold=1：单次失败即触发切流。
	mgr.SetAutoDisablePolicy(1, true, true)

	// 初次选择，落到 primary。
	primary, err := mgr.SelectBestNode(context.Background(), intPtr(1))
	if err != nil {
		t.Fatalf("initial select: %v", err)
	}
	if primary.ID != 1 {
		t.Fatalf("initial primary = %d, want 1", primary.ID)
	}

	// 端到端：HealthCheckNode → applyHealthCheckResult → recordProbeResult。
	// 失败一次递增 ConsecutiveFailures；同时 active selection 的 consecutiveFails 也 = 1。
	if err := mgr.HealthCheckNode(context.Background(), 1); err != nil {
		t.Fatalf("HealthCheckNode on failing node: %v", err)
	}
	primaryNode, _ := store.GetNode(context.Background(), 1)
	if primaryNode.ConsecutiveFailures < 1 {
		t.Fatalf("ConsecutiveFailures=%d, want >=1 after HealthCheckNode failure", primaryNode.ConsecutiveFailures)
	}

	// 验证 active selection 已通过 recordProbeResult 同步失败计数。
	mgr.activeMu.Lock()
	sel := mgr.active[selectionKey(intPtr(1))]
	mgr.activeMu.Unlock()
	if sel == nil {
		t.Fatalf("active selection missing after HealthCheckNode")
	}
	if sel.consecutiveFails < 1 {
		t.Fatalf("active consecutiveFails=%d, want >=1", sel.consecutiveFails)
	}

	// 端到端最后一步：threshold=1 时 ForceSwap 必定选择另一个节点。
	swapped, err := mgr.ForceSwap(context.Background(), intPtr(1))
	if err != nil {
		t.Fatalf("ForceSwap: %v", err)
	}
	if swapped == nil || swapped.ID == 1 {
		t.Fatalf("ForceSwap picked %+v, want node id != 1", swapped)
	}
	if swapped.ID != 2 {
		t.Fatalf("ForceSwap picked id=%d, want 2 (the only alternative)", swapped.ID)
	}
}

// TestR4HealthCheckNodeSkipsPasswordDecryptFailed 单节点 HealthCheckNode 跳过
// PasswordDecryptFailed 的节点，不应触达 checker。批量路径已在 C4 覆盖。
func TestR4HealthCheckNodeSkipsPasswordDecryptFailed(t *testing.T) {
	store := &fakeStoreForBans{
		subs: []*Subscription{{ID: 1, Status: "active"}},
	}
	checker := &strictChecker{}
	broken := &Node{
		ID: 2, SubscriptionID: 1, Name: "broken",
		Protocol: ProtocolHTTP, Server: "127.0.0.1", Port: 9002,
		Status: "active", PasswordDecryptFailed: true, Location: "JP",
	}
	store.nodes = []*Node{broken}
	mgr := NewManager(store, nil, checker)
	mgr.refreshAllSubscriptionBans(context.Background())

	err := mgr.HealthCheckNode(context.Background(), 2)
	if err == nil {
		t.Fatalf("HealthCheckNode must surface error for PasswordDecryptFailed node")
	}
	if checker.called[2] {
		t.Fatalf("checker must not be invoked for PasswordDecryptFailed node id=2")
	}
}

// TestR4DistinctHealthCheckURLsSeparateDedupGroups 验证去重键
// proxyURL + "\x00" + HealthCheckURL：同一 proxy、不同 HealthCheckURL 必须
// 产生两个独立去重组。
func TestR4DistinctHealthCheckURLsSeparateDedupGroups(t *testing.T) {
	store := &fakeStoreForBans{
		subs: []*Subscription{{ID: 1, Status: "active"}},
		nodes: []*Node{
			{ID: 11, SubscriptionID: 1, Name: "nA", Protocol: ProtocolHTTP, Server: "127.0.0.1", Port: 9101, Status: "active", Location: "HK", HealthCheckURL: "https://a.example/204"},
			{ID: 12, SubscriptionID: 1, Name: "nB", Protocol: ProtocolHTTP, Server: "127.0.0.1", Port: 9101, Status: "active", Location: "JP", HealthCheckURL: "https://b.example/204"},
		},
	}
	// 两个不同健康 URL 都返回成功，延迟不同，便于区分。
	checker := &stubHealthChecker{results: map[int]HealthCheckResult{
		11: {NodeID: 11, OK: true, Latency: 25, CheckedAt: time.Now()},
		12: {NodeID: 12, OK: true, Latency: 75, CheckedAt: time.Now()},
	}}
	mgr := NewManager(store, nil, checker)
	mgr.refreshAllSubscriptionBans(context.Background())
	summary := mgr.HealthCheckAllNodesNow(context.Background())

	// 两次独立探活：每个代理+URL 对单独检查，OK 总数=2，MaxMs=75（最大值），
	// AvgMs 在两个去重组间取均值 = (25+75)/2 = 50。
	if summary.OK != 2 {
		t.Fatalf("OK=%d, want 2 (distinct health URLs must probe separately)", summary.OK)
	}
	if summary.MaxMs != 75 {
		t.Fatalf("MaxMs=%d, want 75", summary.MaxMs)
	}
	if summary.AvgMs != 50 {
		t.Fatalf("AvgMs=%d, want 50 (averaged across two dedup groups)", summary.AvgMs)
	}
}

// stuckHealthChecker 在收到 stopCh 之前一直阻塞；用于验证 Stop() 能取消
// 尚未结束的 swapProbeOne（其内部 ctx 来自 m.ctx，Stop 调用 cancel() 后
// checker.Check 会因为 ctx 取消而返回）。
type stuckHealthChecker struct {
	stopCh chan struct{}
}

func (s *stuckHealthChecker) Check(ctx context.Context, _ *Node) (int, error) {
	select {
	case <-s.stopCh:
		return 0, errors.New("stopped")
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}
func (s *stuckHealthChecker) CheckConcurrent(ctx context.Context, nodes []*Node, _ int) <-chan HealthCheckResult {
	ch := make(chan HealthCheckResult, len(nodes))
	for _, n := range nodes {
		ch <- HealthCheckResult{NodeID: n.ID, NodeName: n.Name, OK: true, Latency: 1, CheckedAt: time.Now()}
	}
	close(ch)
	return ch
}

// TestR4StopCancelsSlowSwapProbeOne 验证 Stop() 的 cancel() 能让卡住的
// swapProbeOne 通过 checker.Check 感知 ctx.Done() 后退出，从而 wg.Wait() 返回。
func TestR4StopCancelsSlowSwapProbeOne(t *testing.T) {
	store := &fakeStoreForBans{
		subs: []*Subscription{{ID: 1, Status: "active"}},
		nodes: []*Node{
			{ID: 1, SubscriptionID: 1, Name: "stuck", Protocol: ProtocolHTTP, Server: "127.0.0.1", Port: 9201, Status: "active", Location: "HK"},
		},
	}
	stuck := &stuckHealthChecker{stopCh: make(chan struct{})}
	mgr := NewManager(store, nil, stuck)
	mgr.refreshAllSubscriptionBans(context.Background())
	mgr.SetAutoDisablePolicy(1, true, true)

	subID := 1
	sel := &activeSelection{subscriptionID: &subID, nodeID: 1}
	mgr.activeMu.Lock()
	mgr.active[selectionKey(&subID)] = sel
	mgr.activeMu.Unlock()

	go func() {
		// swapProbeOne 内部使用 15s ctx.FromTimeout(m.ctx, 15s)；我们用更短
		// 的 m.ctx 父 ctx（NewManager 默认是 Background），这里靠 Stop cancel
		// m.ctx 让 stuckHealthChecker 收到 ctx.Done 并返回。
		mgr.swapProbeOne(context.Background(), sel, 1)
	}()

	// 给 swapProbeOne 一点点时间进入 Check；然后 Stop。
	time.Sleep(20 * time.Millisecond)
	stopDone := make(chan struct{})
	go func() {
		mgr.Stop()
		close(stopDone)
	}()
	select {
	case <-stopDone:
		// Stop 已返回 → wg.Done() 已被执行，swapProbeOne 已经退出。
	case <-time.After(3 * time.Second):
		t.Fatalf("Stop did not return within 3s; swapProbeOne is stuck on Check despite ctx cancel")
	}
	// 释放 stuckHealthChecker 的 stopCh 以便复用的测试桩不会泄露。
	close(stuck.stopCh)
}

// slowListStore 让 ListNodes(nil) 阻塞直到 ctx 取消或 stopCh 关闭；
// 用于验证 Start() 不再持 lifecycleMu 同步等待 DB 加载（audit #10）。
type slowListStore struct {
	fakeStoreForBans
	stopCh chan struct{}
}

func (s *slowListStore) ListNodes(ctx context.Context, _ *int) ([]*Node, error) {
	select {
	case <-s.stopCh:
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// TestR4AuditP2_StartDoesNotBlockOnDBLoad 验证 Start() 立即返回，Stop() 也不会
// 在 slowListStore 阻塞 ListNodes 时挂起——Stop 应通过 ctx 取消让 DB 加载 goroutine
// 退出，并在合理时间内返回（audit #10 P2）。
func TestR4AuditP2_StartDoesNotBlockOnDBLoad(t *testing.T) {
	store := &slowListStore{stopCh: make(chan struct{})}
	mgr := NewManager(store, nil, nil)

	startReturned := make(chan struct{})
	go func() {
		mgr.Start()
		close(startReturned)
	}()
	select {
	case <-startReturned:
	case <-time.After(1 * time.Second):
		t.Fatalf("Start did not return within 1s; lifecycleMu was held across DB load")
	}

	stopReturned := make(chan struct{})
	go func() {
		mgr.Stop()
		close(stopReturned)
	}()
	// 给 Stop 一个短窗期：wg.Wait 必须等到 initialCacheLoad goroutine 退出。
	// slowListStore 看到 ctx.Done() 后立刻返回 ListNodes(nil, nil)，预期总耗时 < 1s。
	select {
	case <-stopReturned:
	case <-time.After(2 * time.Second):
		t.Fatalf("Stop did not return within 2s; initial DB load goroutine did not honor ctx")
	}
	close(store.stopCh)
}
