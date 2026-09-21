package proxy

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// TestIsRegionBanned 覆盖订阅层 + 节点层禁用地区的并集语义。
func TestIsRegionBanned(t *testing.T) {
	cases := []struct {
		name string
		loc  string
		sub  []string
		node []string
		want bool
	}{
		{"empty region", "", []string{"US"}, nil, false},
		{"no ban", "US", nil, nil, false},
		{"sub ban hit", "US", []string{"US"}, nil, true},
		{"node ban hit (uppercase)", "US", nil, []string{"US"}, true},
		{"node ban mixed case", "US", nil, []string{"us"}, true},
		{"both miss", "JP", []string{"US"}, []string{"HK"}, false},
		{"whitespace tolerant", " US ", []string{" US "}, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsRegionBanned(tc.loc, tc.sub, tc.node); got != tc.want {
				t.Fatalf("IsRegionBanned(%q, %v, %v) = %v, want %v",
					tc.loc, tc.sub, tc.node, got, tc.want)
			}
		})
	}
}

// fakeStoreForBans 测试桩：仅实现 Manager.SelectBestNode 路径所需。
type fakeStoreForBans struct {
	nodes []*Node
	subs  []*Subscription
}

func (f *fakeStoreForBans) ListNodes(_ context.Context, subscriptionID *int) ([]*Node, error) {
	if subscriptionID == nil {
		return f.nodes, nil
	}
	var out []*Node
	for _, n := range f.nodes {
		if n.SubscriptionID == *subscriptionID {
			out = append(out, n)
		}
	}
	return out, nil
}
func (f *fakeStoreForBans) ListSubscriptions(_ context.Context) ([]*Subscription, error) {
	return f.subs, nil
}
func (f *fakeStoreForBans) GetSubscription(_ context.Context, id int) (*Subscription, error) {
	for _, s := range f.subs {
		if s != nil && s.ID == id {
			return s, nil
		}
	}
	return nil, errors.New("not found")
}

func (f *fakeStoreForBans) GetNode(_ context.Context, id int) (*Node, error) {
	for _, n := range f.nodes {
		if n != nil && n.ID == id {
			// Return a pointer that mirrors the in-memory node; tests that mutate
			// the returned node expect to see the mutation.
			return n, nil
		}
	}
	return nil, errors.New("not found")
}

// 其他接口方法返回零值/无操作。
func (f *fakeStoreForBans) CreateSubscription(context.Context, *Subscription) error {
	return nil
}
func (f *fakeStoreForBans) UpdateSubscription(context.Context, *Subscription) error {
	return nil
}
func (f *fakeStoreForBans) DeleteSubscription(context.Context, int) error {
	return nil
}
func (f *fakeStoreForBans) CreateNode(context.Context, *Node) error { return nil }
func (f *fakeStoreForBans) UpdateNode(context.Context, *Node) error { return nil }
func (f *fakeStoreForBans) DeleteNode(context.Context, int) error   { return nil }
func (f *fakeStoreForBans) DeleteNodesBySubscription(context.Context, int) error {
	return nil
}
func (f *fakeStoreForBans) CreateDomain(context.Context, *Domain) error { return nil }
func (f *fakeStoreForBans) GetDomain(context.Context, string) (*Domain, error) {
	return nil, errors.New("not used")
}
func (f *fakeStoreForBans) ListDomains(context.Context) ([]*Domain, error) {
	return nil, nil
}
func (f *fakeStoreForBans) UpdateDomain(context.Context, *Domain) error { return nil }
func (f *fakeStoreForBans) DeleteDomain(context.Context, int) error     { return nil }

// LoadSelectionPolicy / UpsertSelectionPolicy 默认无操作；测试覆盖 R4 时由
// 具体测试用例重写或注入行为。
func (f *fakeStoreForBans) LoadSelectionPolicy(context.Context) (SelectionPolicy, error) {
	return DefaultSelectionPolicy(), nil
}
func (f *fakeStoreForBans) UpsertSelectionPolicy(context.Context, SelectionPolicy) error {
	return nil
}

// TestSelectBestNodeRespectsRegionBan 验证订阅层 / 节点层禁用地区都能生效。
func TestSelectBestNodeRespectsRegionBan(t *testing.T) {
	store := &fakeStoreForBans{
		subs: []*Subscription{
			{ID: 1, Name: "subA", Status: "active", BannedRegions: []string{"US"}},
			{ID: 2, Name: "subB", Status: "active", BannedRegions: nil},
		},
		nodes: []*Node{
			{ID: 11, SubscriptionID: 1, Name: "HK-A", Protocol: ProtocolHTTP, Server: "1.1.1.1", Port: 80, Status: "active", Location: "HK"},
			{ID: 12, SubscriptionID: 1, Name: "US-A", Protocol: ProtocolHTTP, Server: "1.1.1.2", Port: 80, Status: "active", Location: "US"},
			{ID: 21, SubscriptionID: 2, Name: "JP-B", Protocol: ProtocolHTTP, Server: "2.2.2.1", Port: 80, Status: "active", Location: "JP"},
			{ID: 22, SubscriptionID: 2, Name: "JP-B2", Protocol: ProtocolHTTP, Server: "2.2.2.2", Port: 80, Status: "active", Location: "JP", BannedRegions: []string{"JP"}},
		},
	}
	mgr := NewManager(store, nil, nil)
	mgr.refreshAllSubscriptionBans(context.Background())

	got, err := mgr.SelectBestNode(context.Background(), nil)
	if err != nil {
		t.Fatalf("SelectBestNode: %v", err)
	}
	// subA 的 US 节点被禁；subB 的两个 JP 节点一个被节点层禁。
	// 唯一可选的是 subA 的 HK 节点。
	if got == nil || got.ID != 11 {
		t.Fatalf("SelectBestNode picked %v, want node id=11 (HK on subA)", got)
	}

	// 解禁 US 后再选，subA 的 HK 或 US 节点之一会被选中（都是 active dialable）；
	// 不能是 subB 的 JP（被节点层禁）。
	mgr.subscriptionBans.Delete(1)
	got, err = mgr.SelectBestNode(context.Background(), nil)
	if err != nil {
		t.Fatalf("SelectBestNode after unban: %v", err)
	}
	if got == nil || got.ID == 21 || got.ID == 22 {
		t.Fatalf("SelectBestNode after unban picked %v, must not pick subB JP nodes", got)
	}
}

// TestSelectNodeWithLocationNormalizesRegion (R4 #7)
// 地域亲和匹配必须与 IsRegionBanned 同口径：大小写/首尾空白不分裂同一地区。
func TestSelectNodeWithLocationNormalizesRegion(t *testing.T) {
	nodes := []*Node{
		{ID: 1, Name: "US-A", Location: "US"},
		{ID: 2, Name: "JP-B", Location: "JP"},
	}
	cases := []struct {
		name     string
		affinity LocationAffinityPolicy
		preferred string
		wantID   int
	}{
		{"require_same mixed case", AffinityRequireSame, "us", 1},
		{"require_same whitespace", AffinityRequireSame, " us ", 1},
		{"prefer_same whitespace", AffinityPreferSame, "us ", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lb := NewLoadBalancer(StrategyBestOnly)
			lb.SetLocationAffinity(tc.affinity)
			got := lb.SelectNodeWithLocation(nodes, 1, "req", tc.preferred)
			if got == nil || got.ID != tc.wantID {
				t.Fatalf("SelectNodeWithLocation(%q, affinity=%s) = %+v, want node id=%d",
					tc.preferred, tc.affinity, got, tc.wantID)
			}
		})
	}

	// 纯空白 preferred 等价于无偏好：require_same 下不能把候选集清空成 nil。
	lb := NewLoadBalancer(StrategyBestOnly)
	lb.SetLocationAffinity(AffinityRequireSame)
	if got := lb.SelectNodeWithLocation(nodes, 1, "req", "   "); got == nil {
		t.Fatalf("whitespace-only preferred must fall back to full candidate set, got nil")
	}
}

// TestRegionStatsReportMergesCaseVariants (R4 #7)
// RegionStatsReport 分桶走 normalizeRegion："US" 与 "us " 必须合并为一个桶。
func TestRegionStatsReportMergesCaseVariants(t *testing.T) {
	store := &fakeStoreForBans{
		subs: []*Subscription{{ID: 1, Status: "active"}},
		nodes: []*Node{
			{ID: 1, SubscriptionID: 1, Name: "a", Protocol: ProtocolHTTP, Server: "127.0.0.1", Port: 8001, Status: "active", Location: "US"},
			{ID: 2, SubscriptionID: 1, Name: "b", Protocol: ProtocolHTTP, Server: "127.0.0.1", Port: 8002, Status: "active", Location: " us "},
			{ID: 3, SubscriptionID: 1, Name: "c", Protocol: ProtocolHTTP, Server: "127.0.0.1", Port: 8003, Status: "active", Location: "jp"},
		},
	}
	mgr := NewManager(store, nil, nil)
	stats, err := mgr.RegionStatsReport(context.Background())
	if err != nil {
		t.Fatalf("RegionStatsReport: %v", err)
	}
	byRegion := make(map[string]RegionStats, len(stats))
	for _, s := range stats {
		byRegion[s.Region] = s
	}
	if len(stats) != 2 {
		t.Fatalf("got %d buckets (%v), want 2 (US merged, JP)", len(stats), stats)
	}
	us, ok := byRegion["US"]
	if !ok || us.Total != 2 {
		t.Fatalf("US bucket = %+v, want Total=2 under key \"US\"", us)
	}
	jp, ok := byRegion["JP"]
	if !ok || jp.Total != 1 {
		t.Fatalf("JP bucket = %+v, want Total=1 under key \"JP\"", jp)
	}
}

// TestSetSelectionPolicyAppliesToLoadBalancer 验证策略切换能下发到 LoadBalancer。
func TestSetSelectionPolicyAppliesToLoadBalancer(t *testing.T) {
	mgr := NewManager(&fakeStoreForBans{}, nil, nil)
	policy := DefaultSelectionPolicy()
	policy.LoadBalanceStrategy = StrategyRoundRobin
	policy.LocationAffinity = AffinityPreferSame
	mgr.SetSelectionPolicy(policy)

	if mgr.GetSelectionPolicy().LoadBalanceStrategy != StrategyRoundRobin {
		t.Fatalf("strategy not applied: got %v", mgr.GetSelectionPolicy().LoadBalanceStrategy)
	}
	if mgr.loadBalancer.LocationAffinity() != AffinityPreferSame {
		t.Fatalf("affinity not applied: got %v", mgr.loadBalancer.LocationAffinity())
	}
}

// TestSwapStateResetsOnSelect 验证 selectNode 会更新当前 active selection。
func TestSwapStateResetsOnSelect(t *testing.T) {
	store := &fakeStoreForBans{
		nodes: []*Node{
			{ID: 1, SubscriptionID: 1, Name: "A", Protocol: ProtocolHTTP, Server: "1.1.1.1", Port: 80, Status: "active", Location: "HK"},
			{ID: 2, SubscriptionID: 1, Name: "B", Protocol: ProtocolHTTP, Server: "1.1.1.2", Port: 80, Status: "active", Location: "HK", ResponseTimeMs: 50, SuccessRate: 0.99},
		},
		subs: []*Subscription{{ID: 1, Status: "active"}},
	}
	mgr := NewManager(store, nil, nil)
	mgr.refreshAllSubscriptionBans(context.Background())

	best, err := mgr.SelectBestNode(context.Background(), nil)
	if err != nil {
		t.Fatalf("SelectBestNode: %v", err)
	}
	state := mgr.CurrentSelection()
	if state == nil || state.CurrentNodeID != best.ID {
		t.Fatalf("CurrentSelection = %+v, want node_id=%d", state, best.ID)
	}
}

func TestForceSwapExcludesCurrentNode(t *testing.T) {
	store := &fakeStoreForBans{
		subs: []*Subscription{{ID: 1, Status: "active"}},
		nodes: []*Node{
			{ID: 1, SubscriptionID: 1, Name: "current", Protocol: ProtocolHTTP, Server: "127.0.0.1", Port: 8001, Status: "active", Location: "HK", ResponseTimeMs: 10},
			{ID: 2, SubscriptionID: 1, Name: "backup", Protocol: ProtocolHTTP, Server: "127.0.0.1", Port: 8002, Status: "active", Location: "JP", ResponseTimeMs: 20},
		},
	}
	mgr := NewManager(store, nil, nil)
	mgr.refreshAllSubscriptionBans(context.Background())
	if _, err := mgr.SelectBestNode(context.Background(), intPtr(1)); err != nil {
		t.Fatalf("initial selection: %v", err)
	}
	selected, err := mgr.ForceSwap(context.Background(), intPtr(1))
	if err != nil {
		t.Fatalf("ForceSwap: %v", err)
	}
	if selected == nil || selected.ID == 1 {
		t.Fatalf("ForceSwap selected current node: %+v", selected)
	}
}

// stubHealthChecker 让测试精确控制每次 Check 的结果，便于断言 AvgMs /
// applyHealthCheckResult / PasswordDecryptFailed 跳过语义。
type stubHealthChecker struct {
	results map[int]HealthCheckResult
}

func (s *stubHealthChecker) Check(_ context.Context, n *Node) (int, error) {
	if r, ok := s.results[n.ID]; ok {
		return r.Latency, r.Err
	}
	return 1, nil
}
func (s *stubHealthChecker) CheckConcurrent(_ context.Context, nodes []*Node, _ int) <-chan HealthCheckResult {
	ch := make(chan HealthCheckResult, len(nodes))
	for _, n := range nodes {
		if n == nil {
			continue
		}
		r, ok := s.results[n.ID]
		if !ok {
			r = HealthCheckResult{NodeID: n.ID, NodeName: n.Name, OK: true, Latency: 1, CheckedAt: time.Now()}
		}
		ch <- r
	}
	close(ch)
	return ch
}

// TestC2HealthCheckAllNodesAvgMsDenominatorUsesRepresentatives (C2)
// 三个节点共享同一 proxy+health URL（应被去重为 1 个代表），代表延迟 30ms，
// 旧实现把分母当成节点数 3 → AvgMs=10；新实现除以 representative 1 → AvgMs=30。
func TestC2HealthCheckAllNodesAvgMsDenominatorUsesRepresentatives(t *testing.T) {
	store := &fakeStoreForBans{
		subs: []*Subscription{{ID: 1, Status: "active"}},
		nodes: []*Node{
			{ID: 11, SubscriptionID: 1, Name: "dup-1", Protocol: ProtocolHTTP, Server: "127.0.0.1", Port: 8001, Status: "active", Location: "HK", HealthCheckURL: "https://h.example/204"},
			{ID: 12, SubscriptionID: 1, Name: "dup-2", Protocol: ProtocolHTTP, Server: "127.0.0.1", Port: 8001, Status: "active", Location: "HK", HealthCheckURL: "https://h.example/204"},
			{ID: 13, SubscriptionID: 1, Name: "dup-3", Protocol: ProtocolHTTP, Server: "127.0.0.1", Port: 8001, Status: "active", Location: "HK", HealthCheckURL: "https://h.example/204"},
		},
	}
	checker := &stubHealthChecker{results: map[int]HealthCheckResult{
		11: {NodeID: 11, NodeName: "dup-1", OK: true, Latency: 30, CheckedAt: time.Now()},
		12: {NodeID: 12, NodeName: "dup-2", OK: true, Latency: 30, CheckedAt: time.Now()},
		13: {NodeID: 13, NodeName: "dup-3", OK: true, Latency: 30, CheckedAt: time.Now()},
	}}
	mgr := NewManager(store, nil, checker)
	mgr.refreshAllSubscriptionBans(context.Background())
	summary := mgr.HealthCheckAllNodesNow(context.Background())
	if summary.OK != 3 {
		t.Fatalf("OK=%d, want 3", summary.OK)
	}
	if summary.AvgMs != 30 {
		t.Fatalf("AvgMs=%d, want 30 (denominator must be representative count, not node count)", summary.AvgMs)
	}
	if summary.MaxMs != 30 {
		t.Fatalf("MaxMs=%d, want 30", summary.MaxMs)
	}
}

// TestC3SwapProbeOnUnhealthyBumpsConsecutiveFailures (C3)
// 验证 swapProbeOne 在 active 节点被全局标记为 unhealthy 时调用
// applyHealthCheckResult：节点 ConsecutiveFailures 必须递增、Status 必须保持
// unhealthy、active selection 的 consecutiveFails 计数同步刷新。
func TestC3SwapProbeOnUnhealthyBumpsConsecutiveFailures(t *testing.T) {
	store := &fakeStoreForBans{
		subs: []*Subscription{{ID: 1, Status: "active"}},
	}
	unhealthy := &Node{
		ID: 1, SubscriptionID: 1, Name: "unhealthy",
		Protocol: ProtocolHTTP, Server: "127.0.0.1", Port: 8001,
		Status: "unhealthy", Location: "HK",
	}
	store.nodes = []*Node{unhealthy}
	mgr := NewManager(store, nil, &stubHealthChecker{})
	mgr.refreshAllSubscriptionBans(context.Background())
	mgr.SetAutoDisablePolicy(2, true, true)

	subID := 1
	sel := &activeSelection{subscriptionID: &subID, nodeID: 1}
	mgr.activeMu.Lock()
	mgr.active[selectionKey(&subID)] = sel
	mgr.activeMu.Unlock()

	mgr.swapProbeOne(context.Background(), sel, 5)

	if unhealthy.ConsecutiveFailures != 1 {
		t.Fatalf("ConsecutiveFailures=%d, want 1 (swapProbeOne must call applyHealthCheckResult on unhealthy nodes)",
			unhealthy.ConsecutiveFailures)
	}
	if sel.consecutiveFails != 1 {
		t.Fatalf("active consecutiveFails=%d, want 1", sel.consecutiveFails)
	}
}

// TestC4HealthCheckSubscriptionSkipsPasswordDecryptFailed (C4)
// PasswordDecryptFailed 的节点不应被批量探活，也不会触达 checker。
func TestC4HealthCheckSubscriptionSkipsPasswordDecryptFailed(t *testing.T) {
	store := &fakeStoreForBans{
		subs: []*Subscription{{ID: 1, Status: "active"}},
	}
	checker := &strictChecker{}
	store.nodes = []*Node{
		{ID: 1, SubscriptionID: 1, Name: "good", Protocol: ProtocolHTTP, Server: "127.0.0.1", Port: 8001, Status: "active", Location: "HK"},
		{ID: 2, SubscriptionID: 1, Name: "broken", Protocol: ProtocolHTTP, Server: "127.0.0.1", Port: 8002, Status: "active", Location: "JP", PasswordDecryptFailed: true},
	}
	mgr := NewManager(store, nil, checker)
	mgr.refreshAllSubscriptionBans(context.Background())
	summary, err := mgr.HealthCheckSubscriptionNow(context.Background(), 1)
	if err != nil {
		t.Fatalf("HealthCheckSubscriptionNow: %v", err)
	}
	if summary.Skipped < 1 {
		t.Fatalf("Skipped=%d, want >=1 (PasswordDecryptFailed node must be skipped)", summary.Skipped)
	}
	if checker.called[2] {
		t.Fatalf("checker must not be invoked for PasswordDecryptFailed node id=2")
	}
}

type strictChecker struct{ called map[int]bool }

func (s *strictChecker) Check(_ context.Context, n *Node) (int, error) {
	if s.called == nil {
		s.called = map[int]bool{}
	}
	s.called[n.ID] = true
	return 1, nil
}
func (s *strictChecker) CheckConcurrent(_ context.Context, nodes []*Node, _ int) <-chan HealthCheckResult {
	ch := make(chan HealthCheckResult, len(nodes))
	for _, n := range nodes {
		if n == nil {
			continue
		}
		ch <- HealthCheckResult{NodeID: n.ID, NodeName: n.Name, OK: true, Latency: 1, CheckedAt: time.Now()}
	}
	close(ch)
	return ch
}

// TestC5ReloadCacheSweepsActiveSelectionsForDeletedSubscriptions (C5)
// 验证 ReloadCache 正确清空已不存在的订阅的 active selection，避免 swapLoop
// 持续探测已删订阅的节点。被 store 重新报告且节点仍存在的订阅必须保留。
func TestC5ReloadCacheSweepsActiveSelectionsForDeletedSubscriptions(t *testing.T) {
	store := &fakeStoreForBans{
		subs: []*Subscription{{ID: 2, Status: "active"}},
		nodes: []*Node{
			{ID: 20, SubscriptionID: 2, Name: "keep", Protocol: ProtocolHTTP, Server: "127.0.0.1", Port: 9002, Status: "active", Location: "JP"},
		},
	}
	mgr := NewManager(store, nil, &stubHealthChecker{})
	s1, s2 := 1, 2
	mgr.activeMu.Lock()
	mgr.active["sub:1"] = &activeSelection{subscriptionID: &s1, nodeID: 1}
	mgr.active["sub:2"] = &activeSelection{subscriptionID: &s2, nodeID: 20}
	mgr.activeMu.Unlock()

	if err := mgr.ReloadCache(); err != nil {
		t.Fatalf("ReloadCache: %v", err)
	}

	mgr.activeMu.Lock()
	_, hasOne := mgr.active["sub:1"]
	_, hasTwo := mgr.active["sub:2"]
	mgr.activeMu.Unlock()
	if hasOne {
		t.Errorf("active selection for removed subscription 1 must be cleared")
	}
	if !hasTwo {
		t.Errorf("active selection for subscription 2 (still present in store) must be preserved")
	}
}

// fakeSettingsBackend 是最小 settings 后端：从 map 供值（值须为 JSON 编码）。
type fakeSettingsBackend struct {
	store map[string][]byte
}

func (f *fakeSettingsBackend) Get(_ settings.Scope, key string) ([]byte, error) {
	return f.store[key], nil
}
func (f *fakeSettingsBackend) Set(_ settings.Scope, _ string, _ any) ([]byte, error) {
	return nil, nil
}
func (f *fakeSettingsBackend) GetTenant(_, key string) ([]byte, error) {
	return f.store[key], nil
}
func (f *fakeSettingsBackend) SetTenant(_, _ string, _ any) ([]byte, error) {
	return nil, nil
}

// registerDefaultBannedRegionsSpec 在测试 registry 里只注册 R51 的
// proxy.default_banned_regions（默认 "HK" 来自 Spec.Default）。
func registerDefaultBannedRegionsSpec(t *testing.T, registry *settings.Registry) {
	t.Helper()
	for _, sp := range settings.PlatformSpecs() {
		if sp.Key == "proxy.default_banned_regions" {
			if err := registry.RegisterSpec(sp); err != nil {
				t.Fatalf("register %s: %v", sp.Key, err)
			}
			return
		}
	}
	t.Fatal("spec proxy.default_banned_regions not found in settings.PlatformSpecs()")
}

// TestSelectBestNodeAppliesDefaultBannedRegionsOverlay (R51)
// 订阅未禁 HK，但平台级默认禁用（proxy.default_banned_regions，Spec 默认 HK）
// 时，HK 节点在选择路径被禁；settings 改为空字符串后恢复放行——热更新生效。
func TestSelectBestNodeAppliesDefaultBannedRegionsOverlay(t *testing.T) {
	prevGlobal := settings.Global
	t.Cleanup(func() { settings.Global = prevGlobal })

	store := map[string][]byte{}
	registry := settings.NewRegistry()
	registry.RegisterBackend(settings.ScopePlatform, &fakeSettingsBackend{store: store})
	registry.RegisterBackend(settings.EnvBackendScope, settings.NewStoreEnv())
	registerDefaultBannedRegionsSpec(t, registry)
	settings.Global = registry

	newStore := func() *fakeStoreForBans {
		return &fakeStoreForBans{
			subs:  []*Subscription{{ID: 1, Name: "subA", Status: "active"}},
			nodes: []*Node{{ID: 11, SubscriptionID: 1, Name: "HK-A", Protocol: ProtocolHTTP, Server: "1.1.1.1", Port: 80, Status: "active", Location: "HK"}},
		}
	}

	// 1) Spec 默认（DB/env 均未配置）→ overlay=HK，唯一 HK 节点被禁。
	mgr := NewManager(newStore(), nil, nil)
	mgr.refreshAllSubscriptionBans(context.Background())
	if _, err := mgr.SelectBestNode(context.Background(), nil); err == nil {
		t.Fatal("SelectBestNode with overlay=HK should reject the only HK node")
	} else if !strings.Contains(err.Error(), "region ban") {
		t.Fatalf("SelectBestNode error = %v, want region ban failure", err)
	}

	// 2) settings 改为空字符串 → overlay 关闭，节点放行。
	store["proxy.default_banned_regions"] = []byte(`""`)
	mgr2 := NewManager(newStore(), nil, nil)
	mgr2.refreshAllSubscriptionBans(context.Background())
	got, err := mgr2.SelectBestNode(context.Background(), nil)
	if err != nil {
		t.Fatalf("SelectBestNode with empty overlay: %v", err)
	}
	if got == nil || got.ID != 11 {
		t.Fatalf("SelectBestNode picked %v, want node id=11 after overlay cleared", got)
	}

	// 3) settings 改为多地区列表（大小写/空白混合）→ overlay 重新生效。
	store["proxy.default_banned_regions"] = []byte(`" hk , US ,"`)
	if got := defaultBannedRegionsOverlay(); len(got) != 2 || got[0] != "HK" || got[1] != "US" {
		t.Fatalf("defaultBannedRegionsOverlay() = %v, want [HK US]", got)
	}
	mgr3 := NewManager(newStore(), nil, nil)
	mgr3.refreshAllSubscriptionBans(context.Background())
	if _, err := mgr3.SelectBestNode(context.Background(), nil); err == nil {
		t.Fatal("SelectBestNode with overlay=[HK US] should reject the HK node")
	}
}

// TestDefaultBannedRegionsOverlayUnregistered (R51)
// settings 未注册该键（如独立单测环境）时不启用 overlay，避免隐式禁区。
func TestDefaultBannedRegionsOverlayUnregistered(t *testing.T) {
	prevGlobal := settings.Global
	t.Cleanup(func() { settings.Global = prevGlobal })
	settings.Global = settings.NewRegistry()

	if got := defaultBannedRegionsOverlay(); got != nil {
		t.Fatalf("defaultBannedRegionsOverlay() = %v, want nil when spec unregistered", got)
	}
}
