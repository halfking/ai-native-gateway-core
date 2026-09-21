package proxy

import (
	"context"
	"testing"
)

// R46 F7: 订阅 Priority 死配置接线——此前 selectNodeExcluding 只按
// ConsecutiveFailures→SuccessRate→ResponseTimeMs 排序，Priority 全包零读取，
// 运营配置无任何效果。本测试钉住：健康等级相同时，优先级订阅的节点胜出
// （即使其 RTT 更差）；删除该排序键即测试红。
func TestSelectBestNode_SubscriptionPriorityDecidesAmongHealthyNodes(t *testing.T) {
	store := &fakeStoreForBans{
		subs: []*Subscription{
			{ID: 1, Status: "active", Priority: 5}, // 低优先
			{ID: 2, Status: "active", Priority: 1}, // 高优先（小值优先）
		},
		nodes: []*Node{
			// 低优先订阅的节点 RTT 更好——若无优先级排序它必胜出。
			{ID: 11, SubscriptionID: 1, Name: "slow-sub", Protocol: ProtocolHTTP, Server: "1.1.1.1", Port: 80, Status: "active", Location: "JP", ResponseTimeMs: 10},
			{ID: 22, SubscriptionID: 2, Name: "prio-sub", Protocol: ProtocolHTTP, Server: "2.2.2.2", Port: 80, Status: "active", Location: "US", ResponseTimeMs: 50},
		},
	}
	mgr := NewManager(store, nil, nil)
	mgr.refreshAllSubscriptionBans(context.Background())

	got, err := mgr.SelectBestNode(context.Background(), nil)
	if err != nil {
		t.Fatalf("SelectBestNode: %v", err)
	}
	if got == nil || got.SubscriptionID != 2 {
		t.Fatalf("expected high-priority subscription 2 node to win, got %+v", got)
	}
}

// 健康等级优先于运营偏好：高优先订阅的连败节点不得压过低优先订阅的
// 健康节点（排序键顺序：ConsecutiveFailures 在 Priority 之前）。
func TestSelectBestNode_HealthBeatsSubscriptionPriority(t *testing.T) {
	store := &fakeStoreForBans{
		subs: []*Subscription{
			{ID: 1, Status: "active", Priority: 1}, // 高优先但节点有连败
			{ID: 2, Status: "active", Priority: 9}, // 低优先但节点健康
		},
		nodes: []*Node{
			{ID: 11, SubscriptionID: 1, Name: "flaky-prio", Protocol: ProtocolHTTP, Server: "1.1.1.1", Port: 80, Status: "active", Location: "JP", ConsecutiveFailures: 2, ResponseTimeMs: 10},
			{ID: 22, SubscriptionID: 2, Name: "healthy-low", Protocol: ProtocolHTTP, Server: "2.2.2.2", Port: 80, Status: "active", Location: "US", ConsecutiveFailures: 0, ResponseTimeMs: 80},
		},
	}
	mgr := NewManager(store, nil, nil)
	// 阈值默认 3，ConsecutiveFailures=2 未达 auto-disable 但排序上必须输。
	mgr.refreshAllSubscriptionBans(context.Background())

	got, err := mgr.SelectBestNode(context.Background(), nil)
	if err != nil {
		t.Fatalf("SelectBestNode: %v", err)
	}
	if got == nil || got.SubscriptionID != 2 {
		t.Fatalf("expected healthy node of low-priority subscription to win, got %+v", got)
	}
}

// R47：订阅缺失于 map（store 里没有该订阅记录）时 Priority 回落 0，
// 与"未设置"同桶——不得 panic 或被当作最高/最低优先。
func TestSelectBestNode_MissingSubscriptionPriorityFallsBackToZero(t *testing.T) {
	store := &fakeStoreForBans{
		subs: []*Subscription{}, // 节点 77 的订阅 9 无记录
		nodes: []*Node{
			{ID: 77, SubscriptionID: 9, Name: "orphan-sub", Protocol: ProtocolHTTP, Server: "3.3.3.3", Port: 80, Status: "active", Location: "SG", ResponseTimeMs: 20},
		},
	}
	mgr := NewManager(store, nil, nil)
	mgr.refreshAllSubscriptionBans(context.Background())

	got, err := mgr.SelectBestNode(context.Background(), nil)
	if err != nil {
		t.Fatalf("SelectBestNode: %v", err)
	}
	if got == nil || got.ID != 77 {
		t.Fatalf("expected orphan node to be selectable, got %+v", got)
	}
}
