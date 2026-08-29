package proxy

import (
	"hash/fnv"
	"sort"
	"sync"
)

// LoadBalanceStrategy 负载均衡策略（阶段 3 优化）
type LoadBalanceStrategy string

const (
	// StrategyBestOnly 只选择最优节点（默认策略，向后兼容）
	StrategyBestOnly LoadBalanceStrategy = "best_only"
	// StrategyRoundRobin 轮询策略
	StrategyRoundRobin LoadBalanceStrategy = "round_robin"
	// StrategyWeightedRoundRobin 加权轮询策略（按响应时间权重）
	StrategyWeightedRoundRobin LoadBalanceStrategy = "weighted_rr"
	// StrategyLeastConnections 最少连接策略
	StrategyLeastConnections LoadBalanceStrategy = "least_conn"
	// StrategyConsistentHash 一致性哈希策略（按请求 key）
	StrategyConsistentHash LoadBalanceStrategy = "consistent_hash"
)

// LocationAffinityPolicy 地域亲和性策略（阶段 3 优化）
type LocationAffinityPolicy string

const (
	// AffinityAny 不限制地域（默认）
	AffinityAny LocationAffinityPolicy = "any"
	// AffinityPreferSame 优先选择同地域节点，无同地域节点时选择其他地域
	AffinityPreferSame LocationAffinityPolicy = "prefer_same"
	// AffinityRequireSame 强制同地域节点，无同地域节点时返回 nil
	AffinityRequireSame LocationAffinityPolicy = "require_same"
)

// LoadBalancer 负载均衡器
type LoadBalancer struct {
	strategy         LoadBalanceStrategy
	locationAffinity LocationAffinityPolicy // 阶段 3 优化：地域亲和性策略
	
	// 轮询策略的状态：subscription_id -> 当前索引
	roundRobinIndex sync.Map
	
	// 最少连接策略的状态：node_id -> 当前连接数
	connectionCounts sync.Map
}

// NewLoadBalancer 创建负载均衡器
func NewLoadBalancer(strategy LoadBalanceStrategy) *LoadBalancer {
	return &LoadBalancer{
		strategy:         strategy,
		locationAffinity: AffinityAny, // 默认不限制地域
	}
}

// SetLocationAffinity 设置地域亲和性策略（阶段 3 优化）
func (lb *LoadBalancer) SetLocationAffinity(policy LocationAffinityPolicy) {
	lb.locationAffinity = policy
}

// SelectNode 根据策略从候选节点中选择一个节点
func (lb *LoadBalancer) SelectNode(nodes []*Node, subscriptionID int, requestKey string) *Node {
	if len(nodes) == 0 {
		return nil
	}
	
	if len(nodes) == 1 {
		return nodes[0]
	}
	
	switch lb.strategy {
	case StrategyRoundRobin:
		return lb.selectRoundRobin(nodes, subscriptionID)
	case StrategyWeightedRoundRobin:
		return lb.selectWeightedRoundRobin(nodes)
	case StrategyLeastConnections:
		return lb.selectLeastConnections(nodes)
	case StrategyConsistentHash:
		return lb.selectConsistentHash(nodes, requestKey)
	case StrategyBestOnly:
		fallthrough
	default:
		return nodes[0] // 默认返回第一个（已排序的最优节点）
	}
}

// SelectNodeWithLocation 根据策略和地域亲和性选择节点（阶段 3 优化）
func (lb *LoadBalancer) SelectNodeWithLocation(nodes []*Node, subscriptionID int, requestKey string, preferredLocation string) *Node {
	if len(nodes) == 0 {
		return nil
	}
	
	// 如果没有设置地域亲和性或没有指定位置，使用标准选择
	if lb.locationAffinity == AffinityAny || preferredLocation == "" {
		return lb.SelectNode(nodes, subscriptionID, requestKey)
	}
	
	// 按地域过滤节点
	sameLocationNodes := make([]*Node, 0, len(nodes))
	for _, node := range nodes {
		if node.Location == preferredLocation {
			sameLocationNodes = append(sameLocationNodes, node)
		}
	}
	
	// 根据策略处理
	switch lb.locationAffinity {
	case AffinityRequireSame:
		// 强制同地域，无同地域节点时返回 nil
		if len(sameLocationNodes) == 0 {
			return nil
		}
		return lb.SelectNode(sameLocationNodes, subscriptionID, requestKey)
		
	case AffinityPreferSame:
		// 优先同地域，无同地域节点时选择其他地域
		if len(sameLocationNodes) > 0 {
			return lb.SelectNode(sameLocationNodes, subscriptionID, requestKey)
		}
		return lb.SelectNode(nodes, subscriptionID, requestKey)
		
	default:
		return lb.SelectNode(nodes, subscriptionID, requestKey)
	}
}

// selectRoundRobin 轮询策略
func (lb *LoadBalancer) selectRoundRobin(nodes []*Node, subscriptionID int) *Node {
	// 获取当前索引
	var index int
	if v, ok := lb.roundRobinIndex.Load(subscriptionID); ok {
		index = v.(int)
	}
	
	// 选择节点
	selected := nodes[index%len(nodes)]
	
	// 更新索引
	lb.roundRobinIndex.Store(subscriptionID, (index+1)%len(nodes))
	
	return selected
}

// selectWeightedRoundRobin 加权轮询策略
// 权重计算：响应时间越短权重越高
func (lb *LoadBalancer) selectWeightedRoundRobin(nodes []*Node) *Node {
	// 计算权重（响应时间的倒数，避免除零）
	type weightedNode struct {
		node   *Node
		weight int
	}
	
	weighted := make([]weightedNode, 0, len(nodes))
	totalWeight := 0
	
	for _, node := range nodes {
		// 权重 = 1000 / (响应时间 + 10)，响应时间越短权重越高
		weight := 1000 / (node.ResponseTimeMs + 10)
		if weight < 1 {
			weight = 1
		}
		weighted = append(weighted, weightedNode{node: node, weight: weight})
		totalWeight += weight
	}
	
	if totalWeight == 0 {
		return nodes[0]
	}
	
	// 简化实现：按权重比例选择（权重越高，在列表中出现越多）
	// 这里使用简单的权重选择，选择权重最高的节点
	maxWeight := 0
	var selected *Node
	for _, wn := range weighted {
		if wn.weight > maxWeight {
			maxWeight = wn.weight
			selected = wn.node
		}
	}
	
	if selected == nil {
		return nodes[0]
	}
	
	return selected
}

// selectLeastConnections 最少连接策略
func (lb *LoadBalancer) selectLeastConnections(nodes []*Node) *Node {
	var selected *Node
	minConnections := int(^uint(0) >> 1) // max int
	
	for _, node := range nodes {
		count := 0
		if v, ok := lb.connectionCounts.Load(node.ID); ok {
			count = v.(int)
		}
		
		if count < minConnections {
			minConnections = count
			selected = node
		}
	}
	
	if selected == nil {
		return nodes[0]
	}
	
	return selected
}

// selectConsistentHash 一致性哈希策略
func (lb *LoadBalancer) selectConsistentHash(nodes []*Node, requestKey string) *Node {
	if requestKey == "" {
		return nodes[0]
	}
	
	// 使用 FNV-1a 哈希
	hash := fnv.New32a()
	hash.Write([]byte(requestKey))
	hashValue := hash.Sum32()
	
	// 按节点 ID 排序确保一致性
	sorted := make([]*Node, len(nodes))
	copy(sorted, nodes)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].ID < sorted[j].ID
	})
	
	// 选择节点
	index := int(hashValue) % len(sorted)
	return sorted[index]
}

// IncConnection 增加节点连接数（用于最少连接策略）
func (lb *LoadBalancer) IncConnection(nodeID int) {
	if lb.strategy != StrategyLeastConnections {
		return
	}
	
	count := 0
	if v, ok := lb.connectionCounts.Load(nodeID); ok {
		count = v.(int)
	}
	lb.connectionCounts.Store(nodeID, count+1)
}

// DecConnection 减少节点连接数（用于最少连接策略）
func (lb *LoadBalancer) DecConnection(nodeID int) {
	if lb.strategy != StrategyLeastConnections {
		return
	}
	
	count := 0
	if v, ok := lb.connectionCounts.Load(nodeID); ok {
		count = v.(int)
	}
	if count > 0 {
		lb.connectionCounts.Store(nodeID, count-1)
	}
}
