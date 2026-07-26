package ursm

// applyRoutingPolicy 应用路由策略
// 策略顺序: Tier > Billing > Sticky
func applyRoutingPolicy(nodes []RouteNode, sessionID string) []RouteNode {
	// TODO: Task 4 - 实现策略应用
	// 当前降级：返回前3个节点
	if len(nodes) > 3 {
		return nodes[:3]
	}
	return nodes
}
