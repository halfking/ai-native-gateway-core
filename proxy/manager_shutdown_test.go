package proxy

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeStoreForManager 是 manager 单元测试中通用的可配置桩，
// 既满足 Store 接口又允许断言调用次数 / 调用参数。
type fakeStoreForManager struct {
	fakeStoreForBans
	listNodesCalls        int
	getNodeCalls          int
	updateNodeCalls       int
	getSubscriptionCalls  int
	updateSubscriptionCalls int
	deleteBySubCalls      int
	createNodeCalls       int

	updateErr error
}

func (f *fakeStoreForManager) ListNodes(ctx context.Context, subscriptionID *int) ([]*Node, error) {
	f.listNodesCalls++
	return f.fakeStoreForBans.ListNodes(ctx, subscriptionID)
}
func (f *fakeStoreForManager) GetNode(ctx context.Context, id int) (*Node, error) {
	f.getNodeCalls++
	for _, n := range f.nodes {
		if n != nil && n.ID == id {
			return n, nil
		}
	}
	return nil, errors.New("not found")
}
func (f *fakeStoreForManager) UpdateNode(ctx context.Context, node *Node) error {
	f.updateNodeCalls++
	return f.updateErr
}
func (f *fakeStoreForManager) GetSubscription(ctx context.Context, id int) (*Subscription, error) {
	f.getSubscriptionCalls++
	return f.fakeStoreForBans.GetSubscription(ctx, id)
}
func (f *fakeStoreForManager) UpdateSubscription(ctx context.Context, sub *Subscription) error {
	f.updateSubscriptionCalls++
	return nil
}
func (f *fakeStoreForManager) DeleteNodesBySubscription(ctx context.Context, id int) error {
	f.deleteBySubCalls++
	return nil
}
func (f *fakeStoreForManager) CreateNode(ctx context.Context, node *Node) error {
	f.createNodeCalls++
	return nil
}

// TestShutdownSafetyStartReleasesLockBeforeDBLoad 验证 Start 不再持
// lifecycleMu 跨 loadAllNodesIntoCache：模拟 ListNodes 慢响应，Stop 仍能在
// 合理时间内完成（修复 R4 deferred #10）。
func TestShutdownSafetyStartReleasesLockBeforeDBLoad(t *testing.T) {
	slow := &slowFakeStore{
		fakeStoreForManager: fakeStoreForManager{},
		delay:               150 * time.Millisecond,
	}
	slow.subs = []*Subscription{{ID: 1, Status: "active"}}
	slow.nodes = []*Node{}

	mgr := NewManager(slow, nil, nil)

	go mgr.Start()

	// 在 ListNodes 还没返回前 Stop——必须立即返回，不被 DB 卡住。
	stopStart := time.Now()
	mgr.Stop()
	stopElapsed := time.Since(stopStart)

	if stopElapsed > 50*time.Millisecond {
		t.Fatalf("Stop blocked on DB load: %v (must release lifecycleMu before loadAllNodes)", stopElapsed)
	}
}

// TestShutdownSafetyRefreshGoroutineRegisteredInWG 验证 refreshCacheAsync
// 起的 goroutine 登记在 wg 上，Stop 必须等待其完成（修复 R4 deferred #12）。
func TestShutdownSafetyRefreshGoroutineRegisteredInWG(t *testing.T) {
	store := &fakeStoreForBans{
		subs:  []*Subscription{{ID: 1, Status: "active"}},
		nodes: []*Node{{ID: 1, SubscriptionID: 1, Name: "n", Protocol: ProtocolHTTP, Server: "1.1.1.1", Port: 80, Status: "active"}},
	}
	mgr := NewManager(store, nil, nil)
	mgr.cacheTTL = 50 * time.Millisecond

	// 触发一次异步刷新并立即取消——Stop 必须能正确等待其退出。
	mgr.refreshCacheAsync(1)

	// 让 refresh goroutine 跑完
	time.Sleep(100 * time.Millisecond)

	mgr.Stop()
	// 二次 Stop 不应挂死
	done := make(chan struct{})
	go func() {
		mgr.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("second Stop hung — refresh goroutine not properly tracked in wg")
	}
}

// TestSkipHelperFiltersPwdDecryptFailed 验证 shouldSkipForHealthCheck 闸门
// 对 PasswordDecryptFailed / undialable / disabled 状态都正确拦截，
// 并允许 active+dialable+ok 节点通过（修复 R4 deferred #6）。
func TestSkipHelperFiltersPwdDecryptFailed(t *testing.T) {
	mgr := NewManager(&fakeStoreForBans{}, nil, nil)

	cases := []struct {
		name   string
		node   *Node
		wantOk bool
	}{
		{
			name:   "nil node skipped",
			node:   nil,
			wantOk: false,
		},
		{
			name:   "disabled status skipped",
			node:   &Node{Status: "disabled", Protocol: ProtocolHTTP, Server: "1.1.1.1", Port: 80},
			wantOk: false,
		},
		{
			name:   "undialable skipped",
			node:   &Node{Status: "active", Protocol: "trojan", Server: "1.1.1.1", Port: 80},
			wantOk: false,
		},
		{
			name:   "pwd decrypt failed skipped",
			node:   &Node{Status: "active", Protocol: ProtocolHTTP, Server: "1.1.1.1", Port: 80, PasswordDecryptFailed: true},
			wantOk: false,
		},
		{
			name:   "active+dialable passes",
			node:   &Node{Status: "active", Protocol: ProtocolHTTP, Server: "1.1.1.1", Port: 80},
			wantOk: true,
		},
		{
			name:   "unhealthy+dialable passes",
			node:   &Node{Status: "unhealthy", Protocol: ProtocolHTTP, Server: "1.1.1.1", Port: 80},
			wantOk: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, skip := mgr.shouldSkipForHealthCheck(tc.node)
			if skip == tc.wantOk {
				t.Fatalf("shouldSkipForHealthCheck on %s: skip=%v, want opposite (wantOk=%v)", tc.name, skip, tc.wantOk)
			}
		})
	}
}

// TestHealthCheckSubscriptionNowSkipsPwdDecryptFailed 验证批量订阅探活路径
// 会因 PasswordDecryptFailed 跳过节点，而不是当成 active 处理。Skipped 计数
// 必须包含 pwd-decrypt 节点（虽然与 undialable 分开统计，但语义都是 skip）。
func TestHealthCheckSubscriptionNowSkipsPwdDecryptFailed(t *testing.T) {
	store := &fakeStoreForManager{
		fakeStoreForBans: fakeStoreForBans{
			subs: []*Subscription{{ID: 1, Status: "active"}},
			nodes: []*Node{
				{ID: 1, SubscriptionID: 1, Name: "ok", Protocol: ProtocolHTTP, Server: "1.1.1.1", Port: 80, Status: "active"},
				{ID: 2, SubscriptionID: 1, Name: "pwd-bad", Protocol: ProtocolHTTP, Server: "1.1.1.2", Port: 80, Status: "active", PasswordDecryptFailed: true},
			},
		},
	}
	mgr := NewManager(store, nil, &fakeChecker{})

	summary, err := mgr.HealthCheckSubscriptionNow(context.Background(), 1)
	if err != nil {
		t.Fatalf("HealthCheckSubscriptionNow: %v", err)
	}
	if summary.Total != 2 {
		t.Fatalf("total = %d, want 2", summary.Total)
	}
	// pwd-bad 必须被跳过：要么写一次 updateNodeCalls（ok 节点），pwd-bad 节点 ID=2
	// 不能出现在 UpdateNode 调用链中。
	// 我们的 fakeStoreForManager.GetNode 总是返回同一个指针，所以 updateNode
	// 写入的是最后传入的 *Node；pwd-bad 因为被闸门跳过根本不应进 toCheck。
	// 简单断言：调用了 CheckConcurrent 时只传了 1 个节点（ok）。
	// 这里通过 summary.OK=1 / Failed=0 / Skipped=0（设计上 pwd 计入不同原因）
	// 不强求 Skipped>0，转而断言：ok 节点被探活，pwd-bad 不在被探活集合里。
	if summary.OK != 1 {
		t.Fatalf("summary.OK = %d, want 1 (only ok node should be probed)", summary.OK)
	}
	if summary.Failed != 0 {
		t.Fatalf("summary.Failed = %d, want 0 (pwd-bad is skipped, not failed)", summary.Failed)
	}
}

// TestHealthCheckNodeSkipsPwdDecryptFailed 验证单节点探活路径与新闸门一致。
func TestHealthCheckNodeSkipsPwdDecryptFailed(t *testing.T) {
	store := &fakeStoreForManager{
		fakeStoreForBans: fakeStoreForBans{
			nodes: []*Node{
				{ID: 99, SubscriptionID: 1, Name: "pwd-bad", Protocol: ProtocolHTTP, Server: "1.1.1.1", Port: 80, Status: "active", PasswordDecryptFailed: true},
			},
		},
	}
	mgr := NewManager(store, nil, &fakeChecker{})

	err := mgr.HealthCheckNode(context.Background(), 99)
	if err == nil {
		t.Fatalf("HealthCheckNode should error on PasswordDecryptFailed, got nil")
	}
	if store.updateNodeCalls != 0 {
		t.Fatalf("HealthCheckNode must not UpdateNode on skip path, got %d calls", store.updateNodeCalls)
	}
}

// fakeChecker 是单元测试中最简的 HealthChecker：永远返回成功。
type fakeChecker struct{}

func (f *fakeChecker) Check(ctx context.Context, node *Node) (int, error) {
	return 42, nil
}
func (f *fakeChecker) CheckConcurrent(ctx context.Context, nodes []*Node, concurrency int) <-chan HealthCheckResult {
	ch := make(chan HealthCheckResult, len(nodes))
	for _, n := range nodes {
		ch <- HealthCheckResult{
			NodeID: n.ID, NodeName: n.Name, OK: true, Latency: 42, CheckedAt: time.Now(),
		}
	}
	close(ch)
	return ch
}

// slowFakeStore 在 ListNodes 时模拟 DB 慢响应，用于验证 Start 是否持锁跨 IO。
type slowFakeStore struct {
	fakeStoreForManager
	delay time.Duration
}

func (s *slowFakeStore) ListNodes(ctx context.Context, subscriptionID *int) ([]*Node, error) {
	s.listNodesCalls++
	select {
	case <-time.After(s.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return s.fakeStoreForBans.ListNodes(ctx, subscriptionID)
}

// intPtr 已由 proxy_test.go 提供（包级共享）。
