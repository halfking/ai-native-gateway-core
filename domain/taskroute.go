package domain

// TaskRouteContext 封装任务路由领域的上下文。
type TaskRouteContext struct {
	ClientModel       string
	OutboundModel     string
	Resolution        *Resolution
	RoutingReason     string
	MatchedRule       string
	CostEstimateUSD   float64
	ToolRoutingPolicy string
}

// Resolution 是路由解析结果。
type Resolution struct {
	OutboundModel string
	Provider      string
	Endpoint      string
	Reason        string
}
