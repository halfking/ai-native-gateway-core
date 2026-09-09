package proxy

import (
	"context"
	"errors"
	"testing"
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
func (f *fakeStoreForBans) GetNode(context.Context, int) (*Node, error) {
	return nil, errors.New("not used")
}
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
