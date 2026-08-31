package proxy

import (
	"testing"
)

func TestLoadBalancer_BestOnly(t *testing.T) {
	lb := NewLoadBalancer(StrategyBestOnly)
	
	nodes := []*Node{
		{ID: 1, Name: "node1", ResponseTimeMs: 100},
		{ID: 2, Name: "node2", ResponseTimeMs: 200},
		{ID: 3, Name: "node3", ResponseTimeMs: 300},
	}
	
	// 应该总是选择第一个节点（最优）
	for i := 0; i < 5; i++ {
		selected := lb.SelectNode(nodes, 1, "")
		if selected.ID != 1 {
			t.Errorf("expected node 1, got node %d", selected.ID)
		}
	}
}

func TestLoadBalancer_RoundRobin(t *testing.T) {
	lb := NewLoadBalancer(StrategyRoundRobin)
	
	nodes := []*Node{
		{ID: 1, Name: "node1"},
		{ID: 2, Name: "node2"},
		{ID: 3, Name: "node3"},
	}
	
	// 应该依次选择每个节点
	expected := []int{1, 2, 3, 1, 2, 3}
	for i, expectedID := range expected {
		selected := lb.SelectNode(nodes, 1, "")
		if selected.ID != expectedID {
			t.Errorf("round %d: expected node %d, got node %d", i, expectedID, selected.ID)
		}
	}
}

func TestLoadBalancer_WeightedRoundRobin(t *testing.T) {
	lb := NewLoadBalancer(StrategyWeightedRoundRobin)

	nodes := []*Node{
		{ID: 1, Name: "node1", ResponseTimeMs: 50},  // 高权重
		{ID: 2, Name: "node2", ResponseTimeMs: 200}, // 低权重
		{ID: 3, Name: "node3", ResponseTimeMs: 500}, // 更低权重
	}

	// 选择多次，统计分布（响应时间短的应该被选中更多）
	counts := make(map[int]int)
	for i := 0; i < 100; i++ {
		selected := lb.SelectNode(nodes, 1, "")
		counts[selected.ID]++
	}

	// node1 应该被选中最多（因为响应时间最短）
	if counts[1] == 0 {
		t.Error("node1 should be selected at least once")
	}

	// P2-1 (2026-08-31): 强断言命中分布与权重比例对齐。SWRR 已知误差 ≤
	// O(max_weight)（Nginx 论文定理 4.1），故用 ±15% 相对容差覆盖小样本
	// 抖动。任一节点长期偏离权重比例即视为累积偏差。
	const sampleSize = 100
	weights := map[int]int{}
	totalWeight := 0
	for _, n := range nodes {
		w := nodeWeight(n)
		weights[n.ID] = w
		totalWeight += w
	}
	for _, n := range nodes {
		got := counts[n.ID]
		ratio := float64(got) / float64(sampleSize)
		expected := float64(weights[n.ID]) / float64(totalWeight)
		if expected == 0 {
			continue
		}
		deviation := (ratio - expected) / expected
		if deviation < 0 {
			deviation = -deviation
		}
		if deviation > 0.15 {
			t.Errorf("node %d hit rate %.3f deviates %.1f%% from expected %.3f (counts=%d, weight=%d)",
				n.ID, ratio, deviation*100, expected, got, weights[n.ID])
		}
	}
}

// P2-1 (2026-08-31): 节点集合动态变化时（SWRR 的 weightedCurrent 应只保留
// 当前 active 节点的累积值），验证 stale 节点 ID 被正确清理，counter 不会
// 因残留而让旧 ID 持续"被选中"。
func TestLoadBalancer_WeightedRoundRobin_DynamicNodeSet(t *testing.T) {
	lb := NewLoadBalancer(StrategyWeightedRoundRobin)

	// 第一轮：3 个节点，跑 50 次让 weightedCurrent 累积非零值。
	nodes := []*Node{
		{ID: 1, Name: "node1", ResponseTimeMs: 50},
		{ID: 2, Name: "node2", ResponseTimeMs: 200},
		{ID: 3, Name: "node3", ResponseTimeMs: 500},
	}
	for i := 0; i < 50; i++ {
		if got := lb.SelectNode(nodes, 1, ""); got == nil {
			t.Fatalf("round 1: unexpected nil at i=%d", i)
		}
	}

	// 第二轮：移除 node3，counter 应只基于 {1, 2} 重置；
	// 100 次选择后 counts[3] 必须为 0。
	reduced := []*Node{
		{ID: 1, Name: "node1", ResponseTimeMs: 50},
		{ID: 2, Name: "node2", ResponseTimeMs: 200},
	}
	counts := map[int]int{1: 0, 2: 0, 3: 0}
	for i := 0; i < 100; i++ {
		got := lb.SelectNode(reduced, 1, "")
		if got == nil {
			t.Fatalf("round 2: unexpected nil at i=%d", i)
		}
		counts[got.ID]++
	}
	if counts[3] != 0 {
		t.Errorf("stale node3 selected %d times after removal (must be 0)", counts[3])
	}
	if counts[1] == 0 || counts[2] == 0 {
		t.Errorf("active nodes never selected: counts=%v", counts)
	}
}

func TestLoadBalancer_LeastConnections(t *testing.T) {
	lb := NewLoadBalancer(StrategyLeastConnections)
	
	nodes := []*Node{
		{ID: 1, Name: "node1"},
		{ID: 2, Name: "node2"},
		{ID: 3, Name: "node3"},
	}
	
	// 初始状态，应该选择第一个节点
	selected := lb.SelectNode(nodes, 1, "")
	if selected.ID != 1 {
		t.Errorf("expected node 1, got node %d", selected.ID)
	}
	
	// 增加 node1 的连接数
	lb.IncConnection(1)
	lb.IncConnection(1)
	
	// 现在应该选择 node2（连接数更少）
	selected = lb.SelectNode(nodes, 1, "")
	if selected.ID != 2 {
		t.Errorf("expected node 2, got node %d", selected.ID)
	}
	
	// 减少 node1 的连接数
	lb.DecConnection(1)
	lb.DecConnection(1)
	
	// 现在可能选择 node1 或 node2（连接数相同）
	selected = lb.SelectNode(nodes, 1, "")
	if selected.ID != 1 && selected.ID != 2 {
		t.Errorf("expected node 1 or 2, got node %d", selected.ID)
	}
}

func TestLoadBalancer_ConsistentHash(t *testing.T) {
	lb := NewLoadBalancer(StrategyConsistentHash)
	
	nodes := []*Node{
		{ID: 1, Name: "node1"},
		{ID: 2, Name: "node2"},
		{ID: 3, Name: "node3"},
	}
	
	// 相同的 requestKey 应该总是选择相同的节点
	key1 := "tenant_123"
	selected1 := lb.SelectNode(nodes, 1, key1)
	
	for i := 0; i < 5; i++ {
		selected := lb.SelectNode(nodes, 1, key1)
		if selected.ID != selected1.ID {
			t.Errorf("consistent hash failed: expected node %d, got node %d", selected1.ID, selected.ID)
		}
	}
	
	// 不同的 requestKey 可能选择不同的节点
	key2 := "tenant_456"
	selected2 := lb.SelectNode(nodes, 1, key2)
	
	for i := 0; i < 5; i++ {
		selected := lb.SelectNode(nodes, 1, key2)
		if selected.ID != selected2.ID {
			t.Errorf("consistent hash failed: expected node %d, got node %d", selected2.ID, selected.ID)
		}
	}
}

func TestLoadBalancer_EmptyNodes(t *testing.T) {
	lb := NewLoadBalancer(StrategyRoundRobin)
	
	nodes := []*Node{}
	
	selected := lb.SelectNode(nodes, 1, "")
	if selected != nil {
		t.Error("expected nil for empty nodes")
	}
}

func TestLoadBalancer_SingleNode(t *testing.T) {
	lb := NewLoadBalancer(StrategyRoundRobin)
	
	nodes := []*Node{
		{ID: 1, Name: "node1"},
	}
	
	// 单节点时，所有策略都应该返回这个节点
	for i := 0; i < 5; i++ {
		selected := lb.SelectNode(nodes, 1, "")
		if selected.ID != 1 {
			t.Errorf("expected node 1, got node %d", selected.ID)
		}
	}
}

func TestLoadBalancer_DifferentSubscriptions(t *testing.T) {
	lb := NewLoadBalancer(StrategyRoundRobin)
	
	nodes := []*Node{
		{ID: 1, Name: "node1"},
		{ID: 2, Name: "node2"},
	}
	
	// 不同订阅应该有独立的轮询状态
	selected1 := lb.SelectNode(nodes, 1, "")
	selected2 := lb.SelectNode(nodes, 2, "")
	
	// 两个订阅都应该从第一个节点开始
	if selected1.ID != 1 {
		t.Errorf("subscription 1: expected node 1, got node %d", selected1.ID)
	}
	if selected2.ID != 1 {
		t.Errorf("subscription 2: expected node 1, got node %d", selected2.ID)
	}
	
	// 继续轮询
	selected1 = lb.SelectNode(nodes, 1, "")
	selected2 = lb.SelectNode(nodes, 2, "")
	
	if selected1.ID != 2 {
		t.Errorf("subscription 1: expected node 2, got node %d", selected1.ID)
	}
	if selected2.ID != 2 {
		t.Errorf("subscription 2: expected node 2, got node %d", selected2.ID)
	}
}

// 阶段 3 优化：地域亲和性测试

func TestLoadBalancer_LocationAffinity_Any(t *testing.T) {
	lb := NewLoadBalancer(StrategyBestOnly)
	lb.SetLocationAffinity(AffinityAny)
	
	nodes := []*Node{
		{ID: 1, Name: "node1-us", Location: "US", ResponseTimeMs: 100},
		{ID: 2, Name: "node2-cn", Location: "CN", ResponseTimeMs: 200},
		{ID: 3, Name: "node3-eu", Location: "EU", ResponseTimeMs: 300},
	}
	
	// 不限制地域，应该选择响应时间最短的节点
	selected := lb.SelectNodeWithLocation(nodes, 1, "", "CN")
	if selected.ID != 1 {
		t.Errorf("expected node 1 (best), got node %d", selected.ID)
	}
}

func TestLoadBalancer_LocationAffinity_PreferSame(t *testing.T) {
	lb := NewLoadBalancer(StrategyBestOnly)
	lb.SetLocationAffinity(AffinityPreferSame)
	
	nodes := []*Node{
		{ID: 1, Name: "node1-us", Location: "US", ResponseTimeMs: 100},
		{ID: 2, Name: "node2-cn", Location: "CN", ResponseTimeMs: 200},
		{ID: 3, Name: "node3-cn", Location: "CN", ResponseTimeMs: 250},
		{ID: 4, Name: "node4-eu", Location: "EU", ResponseTimeMs: 300},
	}
	
	// 优先同地域，应该选择 CN 地域中响应时间最短的节点
	selected := lb.SelectNodeWithLocation(nodes, 1, "", "CN")
	if selected.ID != 2 {
		t.Errorf("expected node 2 (CN, best in CN), got node %d", selected.ID)
	}
	
	// 无同地域节点时，选择其他地域
	selected = lb.SelectNodeWithLocation(nodes, 1, "", "JP")
	if selected == nil {
		t.Error("expected a node from other locations, got nil")
	}
	if selected.ID != 1 {
		t.Errorf("expected node 1 (best overall), got node %d", selected.ID)
	}
}

func TestLoadBalancer_LocationAffinity_RequireSame(t *testing.T) {
	lb := NewLoadBalancer(StrategyBestOnly)
	lb.SetLocationAffinity(AffinityRequireSame)
	
	nodes := []*Node{
		{ID: 1, Name: "node1-us", Location: "US", ResponseTimeMs: 100},
		{ID: 2, Name: "node2-cn", Location: "CN", ResponseTimeMs: 200},
		{ID: 3, Name: "node3-cn", Location: "CN", ResponseTimeMs: 250},
	}
	
	// 强制同地域，应该选择 CN 地域中响应时间最短的节点
	selected := lb.SelectNodeWithLocation(nodes, 1, "", "CN")
	if selected.ID != 2 {
		t.Errorf("expected node 2 (CN, best in CN), got node %d", selected.ID)
	}
	
	// 无同地域节点时，返回 nil
	selected = lb.SelectNodeWithLocation(nodes, 1, "", "JP")
	if selected != nil {
		t.Errorf("expected nil for no matching location, got node %d", selected.ID)
	}
}

func TestLoadBalancer_LocationAffinity_WithRoundRobin(t *testing.T) {
	lb := NewLoadBalancer(StrategyRoundRobin)
	lb.SetLocationAffinity(AffinityPreferSame)
	
	nodes := []*Node{
		{ID: 1, Name: "node1-us", Location: "US"},
		{ID: 2, Name: "node2-cn", Location: "CN"},
		{ID: 3, Name: "node3-cn", Location: "CN"},
		{ID: 4, Name: "node4-eu", Location: "EU"},
	}
	
	// 轮询同地域节点
	selected1 := lb.SelectNodeWithLocation(nodes, 1, "", "CN")
	selected2 := lb.SelectNodeWithLocation(nodes, 1, "", "CN")
	
	// 应该在 CN 地域的两个节点间轮询
	if selected1.Location != "CN" || selected2.Location != "CN" {
		t.Error("both selections should be from CN location")
	}
	
	if selected1.ID == selected2.ID {
		t.Error("round robin should select different nodes")
	}
}
