package proxy

// manager_health_race_test.go — R4 deferred #3（并发 health-check 写竞态）守门测试。
//
// 重建于 2026-09-09 审计修正批次：原版本随 backup-r5-work 分支基于旧实现
// （nodeWriteLocks），本版本适配 main 的 nodeProbeLock 命名与
// RefreshSubscriptionAndMetadata 等远程并行修复，并新增批量路径
// （healthCheckAllNodesInner → PgStore.BatchUpdateNodes 快速路径）的
// P0 回归守门——5486952e8 修复的正是这条路径绕过 per-node 锁的问题。
//
// fake 设计原则：
//   - ListNodes / GetNode 返回 cloneNode 隔离快照（模拟真实 PgStore 的
//     sql.Row 扫描隔离），避免测试桩自身的数据竞态掩盖被测行为；
//   - UpdateNode 把传入快照的健康字段同步回源节点（模拟"落库后下次读到"），
//     这是断言丢失更新的观测点；
//   - UpdateNode 用 per-node atomic 计数器观测最大并发度：同一 node_id
//     的并发 UpdateNode 必须 == 1（per-node 锁串行化），不同节点间允许并行。

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// perNodeState 追踪单节点 UpdateNode 并发度（race detector 友好的纯原子实现）。
type perNodeState struct {
	cur int32
	max int32
}

func (p *perNodeState) enter() {
	cur := atomic.AddInt32(&p.cur, 1)
	for {
		prev := atomic.LoadInt32(&p.max)
		if cur <= prev || atomic.CompareAndSwapInt32(&p.max, prev, cur) {
			return
		}
	}
}

func (p *perNodeState) leave() { atomic.AddInt32(&p.cur, -1) }

func (p *perNodeState) maxObserved() int32 { return atomic.LoadInt32(&p.max) }

// sharedNodeStore 满足 Store 接口的并发测试桩（不含 BatchUpdateNodes，
// 用于触发 healthCheckAllNodesInner 的非 PgStore 降级路径）。
type sharedNodeStore struct {
	mu          sync.Mutex
	nodes       map[int]*Node
	updateCount map[int]int
	perNode     sync.Map // node_id -> *perNodeState
}

func newSharedNodeStore(nodes []*Node) *sharedNodeStore {
	m := make(map[int]*Node, len(nodes))
	for _, n := range nodes {
		m[n.ID] = n
	}
	return &sharedNodeStore{nodes: m, updateCount: make(map[int]int)}
}

func (s *sharedNodeStore) stateFor(id int) *perNodeState {
	if v, ok := s.perNode.Load(id); ok {
		return v.(*perNodeState)
	}
	v, _ := s.perNode.LoadOrStore(id, &perNodeState{})
	return v.(*perNodeState)
}

func (s *sharedNodeStore) ListNodes(_ context.Context, subscriptionID *int) ([]*Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Node
	for _, n := range s.nodes {
		if subscriptionID != nil && n.SubscriptionID != *subscriptionID {
			continue
		}
		out = append(out, cloneNode(n))
	}
	return out, nil
}

func (s *sharedNodeStore) GetNode(_ context.Context, id int) (*Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.nodes[id]
	if !ok {
		return nil, errors.New("not found")
	}
	return cloneNode(n), nil
}

func (s *sharedNodeStore) UpdateNode(_ context.Context, node *Node) error {
	s.stateFor(node.ID).enter()
	// 模拟 DB 写延迟，放大无锁时的竞态窗口（500µs 在 -race 下足够稳定）。
	time.Sleep(500 * time.Microsecond)
	s.stateFor(node.ID).leave()

	s.mu.Lock()
	if src, ok := s.nodes[node.ID]; ok {
		src.ConsecutiveFailures = node.ConsecutiveFailures
		src.LastHealthCheckStatus = node.LastHealthCheckStatus
		src.Status = node.Status
	}
	s.updateCount[node.ID]++
	s.mu.Unlock()
	return nil
}

func (s *sharedNodeStore) updateCountFor(id int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updateCount[id]
}

func (s *sharedNodeStore) maxParallelFor(id int) int32 {
	return s.stateFor(id).maxObserved()
}

// —— Store 接口其余方法：零值行为 ——

func (s *sharedNodeStore) CreateSubscription(context.Context, *Subscription) error { return nil }
func (s *sharedNodeStore) GetSubscription(_ context.Context, id int) (*Subscription, error) {
	return nil, errors.New("not used")
}
func (s *sharedNodeStore) ListSubscriptions(context.Context) ([]*Subscription, error) {
	return nil, nil
}
func (s *sharedNodeStore) UpdateSubscription(context.Context, *Subscription) error { return nil }
func (s *sharedNodeStore) DeleteSubscription(context.Context, int) error           { return nil }
func (s *sharedNodeStore) CreateNode(context.Context, *Node) error                 { return nil }
func (s *sharedNodeStore) DeleteNode(context.Context, int) error                   { return nil }
func (s *sharedNodeStore) DeleteNodesBySubscription(context.Context, int) error    { return nil }
func (s *sharedNodeStore) CreateDomain(context.Context, *Domain) error             { return nil }
func (s *sharedNodeStore) GetDomain(context.Context, string) (*Domain, error) {
	return nil, errors.New("not used")
}
func (s *sharedNodeStore) ListDomains(context.Context) ([]*Domain, error) { return nil, nil }
func (s *sharedNodeStore) UpdateDomain(context.Context, *Domain) error    { return nil }
func (s *sharedNodeStore) DeleteDomain(context.Context, int) error        { return nil }

func (s *sharedNodeStore) LoadSelectionPolicy(context.Context) (SelectionPolicy, error) {
	return DefaultSelectionPolicy(), nil
}
func (s *sharedNodeStore) UpsertSelectionPolicy(context.Context, SelectionPolicy) error {
	return nil
}

// failingChecker 所有探测返回失败，驱动 applyHealthCheckResult 的 ++ 分支。
type failingChecker struct{ calls int32 }

func (f *failingChecker) Check(_ context.Context, _ *Node) (int, error) {
	atomic.AddInt32(&f.calls, 1)
	return 0, errors.New("simulated failure")
}

func (f *failingChecker) CheckConcurrent(_ context.Context, nodes []*Node, _ int) <-chan HealthCheckResult {
	ch := make(chan HealthCheckResult, len(nodes))
	for _, n := range nodes {
		atomic.AddInt32(&f.calls, 1)
		ch <- HealthCheckResult{NodeID: n.ID, NodeName: n.Name, OK: false, Err: errors.New("simulated failure"), CheckedAt: time.Now()}
	}
	close(ch)
	return ch
}

// batchStore 在 sharedNodeStore 上实现 BatchUpdateNodes（与 Store 接口对齐；
// manager 全局路径审计修正后已不走此方法，保留接口兼容）。
type batchStore struct {
	*sharedNodeStore
}

func (b *batchStore) BatchUpdateNodes(ctx context.Context, nodes []*Node) error {
	for _, n := range nodes {
		if err := b.sharedNodeStore.UpdateNode(ctx, n); err != nil {
			return err
		}
	}
	return nil
}

// TestHealthCheckNodeNoLostUpdates：32 并发单节点探活，失败计数必须精确累加。
func TestHealthCheckNodeNoLostUpdates(t *testing.T) {
	node := &Node{ID: 7, SubscriptionID: 1, Name: "race-target", Protocol: ProtocolHTTP, Server: "127.0.0.1", Port: 8000, Status: "active"}
	store := newSharedNodeStore([]*Node{node})
	mgr := NewManager(store, nil, &failingChecker{})
	mgr.SetAutoDisablePolicy(1_000_000, true, false) // 关闭 auto-disable，保持 status=active 可重复探测

	const goroutines = 32
	var wg sync.WaitGroup
	wg.Add(goroutines)
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			<-start
			_ = mgr.HealthCheckNode(context.Background(), node.ID)
		}()
	}
	close(start)
	wg.Wait()

	store.mu.Lock()
	got := store.nodes[node.ID].ConsecutiveFailures
	store.mu.Unlock()
	if got != goroutines {
		t.Fatalf("ConsecutiveFailures = %d, want %d (lost updates)", got, goroutines)
	}
	if calls := store.updateCountFor(node.ID); calls != goroutines {
		t.Fatalf("UpdateNode calls = %d, want %d", calls, goroutines)
	}
}

// TestPerNodeLockSerializesUpdateNode：同一节点并发探测时 UpdateNode 必须串行。
func TestPerNodeLockSerializesUpdateNode(t *testing.T) {
	node := &Node{ID: 99, SubscriptionID: 1, Name: "serial-target", Protocol: ProtocolHTTP, Server: "127.0.0.1", Port: 8002, Status: "active"}
	store := newSharedNodeStore([]*Node{node})
	mgr := NewManager(store, nil, &failingChecker{})

	const goroutines = 16
	var wg sync.WaitGroup
	wg.Add(goroutines)
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			<-start
			_ = mgr.HealthCheckNode(context.Background(), node.ID)
		}()
	}
	close(start)
	wg.Wait()

	if got := store.maxParallelFor(node.ID); got != 1 {
		t.Fatalf("max parallel UpdateNode for node %d = %d, want 1 (per-node lock not serializing)", node.ID, got)
	}
}

// TestHealthCheckSubscriptionNowNoLostUpdates：订阅批量路径并发不丢更新。
func TestHealthCheckSubscriptionNowNoLostUpdates(t *testing.T) {
	node := &Node{ID: 11, SubscriptionID: 5, Name: "race-batch", Protocol: ProtocolHTTP, Server: "127.0.0.1", Port: 8001, Status: "active"}
	store := newSharedNodeStore([]*Node{node})
	mgr := NewManager(store, nil, &failingChecker{})
	mgr.SetAutoDisablePolicy(1_000_000, true, false)

	const goroutines = 16
	var wg sync.WaitGroup
	wg.Add(goroutines)
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			<-start
			_, _ = mgr.HealthCheckSubscriptionNow(context.Background(), 5)
		}()
	}
	close(start)
	wg.Wait()

	store.mu.Lock()
	got := store.nodes[node.ID].ConsecutiveFailures
	store.mu.Unlock()
	if got != goroutines {
		t.Fatalf("ConsecutiveFailures = %d, want %d (lost updates in batch path)", got, goroutines)
	}
}

// TestBatchPathRacesWithSingleNodeProbe（P0 守门）：healthCheckAllNodesInner
// 的全局落库路径与单节点 HealthCheckNode 并发时，失败计数必须精确累加。
// 审计修正后全局路径在 per-node 锁内完成 GetNode→apply→UpdateNode→cache，
// 不再走无锁的 BatchUpdateNodes 快速路径（5486952e8 + 后续重构）。
func TestBatchPathRacesWithSingleNodeProbe(t *testing.T) {
	node := &Node{ID: 1, SubscriptionID: 1, Name: "batch-race", Protocol: ProtocolHTTP, Server: "127.0.0.1", Port: 8003, Status: "active"}
	store := &batchStore{sharedNodeStore: newSharedNodeStore([]*Node{node})}
	mgr := NewManager(store, nil, &failingChecker{})
	mgr.SetAutoDisablePolicy(1_000_000, true, false)

	const singles, globals = 16, 8
	var wg sync.WaitGroup
	wg.Add(singles + globals)
	start := make(chan struct{})
	for i := 0; i < singles; i++ {
		go func() {
			defer wg.Done()
			<-start
			_ = mgr.HealthCheckNode(context.Background(), node.ID)
		}()
	}
	for i := 0; i < globals; i++ {
		go func() {
			defer wg.Done()
			<-start
			_ = mgr.HealthCheckAllNodesNow(context.Background())
		}()
	}
	close(start)
	wg.Wait()

	// 16 次单节点 + 8 次全局批量（每轮对 node 1 各 +1）= 24 次失败必须全数落库。
	store.mu.Lock()
	got := store.nodes[node.ID].ConsecutiveFailures
	store.mu.Unlock()
	if want := int32(singles + globals); int32(got) != want {
		t.Fatalf("ConsecutiveFailures = %d, want %d (global path lost updates)", got, want)
	}
	if maxPar := store.maxParallelFor(node.ID); maxPar != 1 {
		t.Fatalf("max parallel UpdateNode = %d, want 1 (global path not serialized per node)", maxPar)
	}
}

// TestSwapProbeOneSkipsPwdDecryptFailed（#6 守门）：swap 探测不再以密文密码探测。
func TestSwapProbeOneSkipsPwdDecryptFailed(t *testing.T) {
	node := &Node{ID: 21, SubscriptionID: 1, Name: "pwd-bad", Protocol: ProtocolHTTP, Server: "127.0.0.1", Port: 8004, Status: "active", PasswordDecryptFailed: true}
	store := newSharedNodeStore([]*Node{node})
	checker := &failingChecker{}
	mgr := NewManager(store, nil, checker)

	sel := &activeSelection{subscriptionID: intPtr(1), nodeID: node.ID}
	mgr.swapProbeOne(context.Background(), sel, 2)

	if calls := atomic.LoadInt32(&checker.calls); calls != 0 {
		t.Fatalf("checker.Check called %d times for PasswordDecryptFailed node, want 0", calls)
	}
	if calls := store.updateCountFor(node.ID); calls != 0 {
		t.Fatalf("UpdateNode called %d times for PasswordDecryptFailed node, want 0", calls)
	}
}

// TestGlobalHealthCheckSkipsPwdDecryptFailed（#6 守门）：全局批量路径同样跳过。
func TestGlobalHealthCheckSkipsPwdDecryptFailed(t *testing.T) {
	node := &Node{ID: 22, SubscriptionID: 1, Name: "pwd-bad-global", Protocol: ProtocolHTTP, Server: "127.0.0.1", Port: 8005, Status: "active", PasswordDecryptFailed: true}
	store := newSharedNodeStore([]*Node{node})
	checker := &failingChecker{}
	mgr := NewManager(store, nil, checker)

	summary := mgr.HealthCheckAllNodesNow(context.Background())

	if calls := atomic.LoadInt32(&checker.calls); calls != 0 {
		t.Fatalf("checker.Check called %d times, want 0", calls)
	}
	if summary.Failed != 0 {
		t.Fatalf("summary.Failed = %d, want 0 (pwd-decrypt node must be skipped, not failed)", summary.Failed)
	}
}

// TestGlobalHealthCheckFeedsActiveSwapCounter（R4 #5 守门）：调度全局探活
// （healthCheckAllNodesInner 步骤 5）的结果必须经 recordProbeResult 回灌
// active swap 计数器——失败递增、成功归零。若回灌缺失，ForceSwap 的
// consecutiveFails >= threshold 触发条件永远等不到全局探活证据。
// 双计数核对：applyHealthCheckResult 只动 node 级 ConsecutiveFailures，
// recordProbeResult 只动 selection 级 consecutiveFails，一次探测各计一次。
func TestGlobalHealthCheckFeedsActiveSwapCounter(t *testing.T) {
	subID := 1
	newActiveManager := func(checker HealthChecker) (*sharedNodeStore, *Manager) {
		store := newSharedNodeStore([]*Node{{
			ID: 1, SubscriptionID: 1, Name: "active", Protocol: ProtocolHTTP,
			Server: "127.0.0.1", Port: 8006, Status: "active", Location: "HK",
		}})
		mgr := NewManager(store, nil, checker)
		mgr.activeMu.Lock()
		mgr.active[selectionKey(&subID)] = &activeSelection{subscriptionID: &subID, nodeID: 1}
		mgr.activeMu.Unlock()
		return store, mgr
	}
	activeFails := func(mgr *Manager) (int, time.Time) {
		mgr.activeMu.Lock()
		defer mgr.activeMu.Unlock()
		sel := mgr.active[selectionKey(&subID)]
		return sel.consecutiveFails, sel.lastProbeAt
	}

	// 失败方向：全局探活失败 → active consecutiveFails 递增。
	store, mgr := newActiveManager(&failingChecker{})
	mgr.HealthCheckAllNodesNow(context.Background())
	fails, probedAt := activeFails(mgr)
	if fails != 1 {
		t.Fatalf("after failed global probe: active consecutiveFails = %d, want 1", fails)
	}
	if probedAt.IsZero() {
		t.Fatal("after failed global probe: lastProbeAt not set (recordProbeResult not called)")
	}
	if got := store.updateCountFor(1); got != 1 {
		t.Fatalf("node-level UpdateNode count = %d, want 1", got)
	}

	// 成功方向：已有失败计数的 active 节点被全局探活确认健康 → 归零。
	_, mgr2 := newActiveManager(&stubHealthChecker{})
	mgr2.activeMu.Lock()
	mgr2.active[selectionKey(&subID)] = &activeSelection{subscriptionID: &subID, nodeID: 1, consecutiveFails: 2}
	mgr2.activeMu.Unlock()
	mgr2.HealthCheckAllNodesNow(context.Background())
	fails2, _ := activeFails(mgr2)
	if fails2 != 0 {
		t.Fatalf("after successful global probe: active consecutiveFails = %d, want 0 (reset)", fails2)
	}
}
