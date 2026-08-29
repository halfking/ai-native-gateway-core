package proxy

import (
	"fmt"
	"hash/fnv"
	"sort"
	"sync"
)

// LoadBalanceStrategy 负载均衡策略（阶段 3 优化）。
type LoadBalanceStrategy string

const (
	StrategyBestOnly           LoadBalanceStrategy = "best_only"
	StrategyRoundRobin         LoadBalanceStrategy = "round_robin"
	StrategyWeightedRoundRobin LoadBalanceStrategy = "weighted_rr"
	StrategyLeastConnections   LoadBalanceStrategy = "least_conn"
	StrategyConsistentHash     LoadBalanceStrategy = "consistent_hash"
)

// LocationAffinityPolicy 地域亲和性策略。
type LocationAffinityPolicy string

const (
	AffinityAny         LocationAffinityPolicy = "any"
	AffinityPreferSame  LocationAffinityPolicy = "prefer_same"
	AffinityRequireSame LocationAffinityPolicy = "require_same"
)

const virtualNodesPerNode = 64

// LoadBalancer owns the mutable state required by stateful strategies. Its
// methods are safe for direct concurrent use as well as Manager-mediated use.
type LoadBalancer struct {
	mu               sync.Mutex
	strategy         LoadBalanceStrategy
	locationAffinity LocationAffinityPolicy
	roundRobinIndex  map[int]uint64
	connectionCounts map[int]int
	weightedCurrent  map[int]map[int]int
}

func NewLoadBalancer(strategy LoadBalanceStrategy) *LoadBalancer {
	if !validLoadBalanceStrategy(strategy) {
		strategy = StrategyBestOnly
	}
	return &LoadBalancer{
		strategy:         strategy,
		locationAffinity: AffinityAny,
		roundRobinIndex:  make(map[int]uint64),
		connectionCounts: make(map[int]int),
		weightedCurrent:  make(map[int]map[int]int),
	}
}

func validLoadBalanceStrategy(strategy LoadBalanceStrategy) bool {
	switch strategy {
	case StrategyBestOnly, StrategyRoundRobin, StrategyWeightedRoundRobin, StrategyLeastConnections, StrategyConsistentHash:
		return true
	default:
		return false
	}
}

// SetStrategy changes the selection strategy. Unknown values are ignored.
func (lb *LoadBalancer) SetStrategy(strategy LoadBalanceStrategy) {
	if !validLoadBalanceStrategy(strategy) {
		return
	}
	lb.mu.Lock()
	lb.strategy = strategy
	lb.mu.Unlock()
}

// SetLocationAffinity 设置地域亲和性策略。未知值被忽略。
func (lb *LoadBalancer) SetLocationAffinity(policy LocationAffinityPolicy) {
	switch policy {
	case AffinityAny, AffinityPreferSame, AffinityRequireSame:
	default:
		return
	}
	lb.mu.Lock()
	lb.locationAffinity = policy
	lb.mu.Unlock()
}

func (lb *LoadBalancer) LocationAffinity() LocationAffinityPolicy {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	return lb.locationAffinity
}

// SelectNode 根据策略从候选节点中选择一个节点。候选项由调用方按健康度排序。
func (lb *LoadBalancer) SelectNode(nodes []*Node, subscriptionID int, requestKey string) *Node {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	return lb.selectLocked(nodes, subscriptionID, requestKey)
}

// SelectNodeWithLocation 根据策略和地域亲和性选择节点。
func (lb *LoadBalancer) SelectNodeWithLocation(nodes []*Node, subscriptionID int, requestKey, preferredLocation string) *Node {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	if len(nodes) == 0 || lb.locationAffinity == AffinityAny || preferredLocation == "" {
		return lb.selectLocked(nodes, subscriptionID, requestKey)
	}

	sameLocation := make([]*Node, 0, len(nodes))
	for _, node := range nodes {
		if node.Location == preferredLocation {
			sameLocation = append(sameLocation, node)
		}
	}
	if lb.locationAffinity == AffinityRequireSame {
		return lb.selectLocked(sameLocation, subscriptionID, requestKey)
	}
	if len(sameLocation) > 0 {
		return lb.selectLocked(sameLocation, subscriptionID, requestKey)
	}
	return lb.selectLocked(nodes, subscriptionID, requestKey)
}

func (lb *LoadBalancer) selectLocked(nodes []*Node, subscriptionID int, requestKey string) *Node {
	if len(nodes) == 0 {
		return nil
	}
	if len(nodes) == 1 {
		return nodes[0]
	}

	switch lb.strategy {
	case StrategyRoundRobin:
		index := lb.roundRobinIndex[subscriptionID]
		selected := nodes[index%uint64(len(nodes))]
		lb.roundRobinIndex[subscriptionID] = index + 1
		return selected
	case StrategyWeightedRoundRobin:
		return lb.selectWeightedRoundRobinLocked(nodes, subscriptionID)
	case StrategyLeastConnections:
		return lb.selectLeastConnectionsLocked(nodes)
	case StrategyConsistentHash:
		return selectConsistentHash(nodes, requestKey)
	default:
		return nodes[0]
	}
}

// selectWeightedRoundRobinLocked implements smooth weighted round robin so the
// lowest-latency node receives more traffic without starving other candidates.
func (lb *LoadBalancer) selectWeightedRoundRobinLocked(nodes []*Node, subscriptionID int) *Node {
	current := lb.weightedCurrent[subscriptionID]
	if current == nil {
		current = make(map[int]int, len(nodes))
		lb.weightedCurrent[subscriptionID] = current
	}

	totalWeight := 0
	bestWeight := -1 << 30
	var selected *Node
	active := make(map[int]struct{}, len(nodes))
	for _, node := range nodes {
		weight := nodeWeight(node)
		totalWeight += weight
		current[node.ID] += weight
		active[node.ID] = struct{}{}
		if selected == nil || current[node.ID] > bestWeight || (current[node.ID] == bestWeight && node.ID < selected.ID) {
			selected, bestWeight = node, current[node.ID]
		}
	}
	for id := range current {
		if _, ok := active[id]; !ok {
			delete(current, id)
		}
	}
	current[selected.ID] -= totalWeight
	return selected
}

func nodeWeight(node *Node) int {
	latency := node.ResponseTimeMs
	if latency < 0 {
		latency = 0
	}
	weight := 1000 / (latency + 10)
	if weight < 1 {
		return 1
	}
	return weight
}

func (lb *LoadBalancer) selectLeastConnectionsLocked(nodes []*Node) *Node {
	selected := nodes[0]
	least := lb.connectionCounts[selected.ID]
	for _, node := range nodes[1:] {
		count := lb.connectionCounts[node.ID]
		if count < least || (count == least && node.ID < selected.ID) {
			selected, least = node, count
		}
	}
	return selected
}

// selectConsistentHash builds a small virtual-node hash ring. Unlike modulo
// hashing, changing the candidate set remaps only keys near the changed node.
func selectConsistentHash(nodes []*Node, requestKey string) *Node {
	if requestKey == "" {
		return nodes[0]
	}
	type point struct {
		hash uint32
		node *Node
	}
	ring := make([]point, 0, len(nodes)*virtualNodesPerNode)
	for _, node := range nodes {
		for replica := 0; replica < virtualNodesPerNode; replica++ {
			ring = append(ring, point{hashString(fmt.Sprintf("%d#%d", node.ID, replica)), node})
		}
	}
	sort.Slice(ring, func(i, j int) bool {
		if ring[i].hash == ring[j].hash {
			return ring[i].node.ID < ring[j].node.ID
		}
		return ring[i].hash < ring[j].hash
	})
	key := hashString(requestKey)
	index := sort.Search(len(ring), func(i int) bool { return ring[i].hash >= key })
	if index == len(ring) {
		index = 0
	}
	return ring[index].node
}

func hashString(value string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(value))
	return h.Sum32()
}

// IncConnection 和 DecConnection 必须由最少连接策略的调用方在请求生命周期两端配对调用。
func (lb *LoadBalancer) IncConnection(nodeID int) {
	lb.mu.Lock()
	lb.connectionCounts[nodeID]++
	lb.mu.Unlock()
}

func (lb *LoadBalancer) DecConnection(nodeID int) {
	lb.mu.Lock()
	if count := lb.connectionCounts[nodeID]; count <= 1 {
		delete(lb.connectionCounts, nodeID)
	} else {
		lb.connectionCounts[nodeID] = count - 1
	}
	lb.mu.Unlock()
}
