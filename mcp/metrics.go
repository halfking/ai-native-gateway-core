package mcp

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Prometheus 指标。
//
// 指标名与既有 tools/mcp-gateway 保持同名（便于既有告警规则与看板平移），
// 另加 llm_gateway_ 前缀的一组企业治理指标。
var (
	// requestsTotal 总请求数。
	requestsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mcp_gateway_requests_total",
		Help: "Total MCP gateway data-plane requests.",
	})
	// requestsByCapability 按能力域分类的请求数。
	requestsByCapability = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "mcp_gateway_requests_by_capability",
		Help: "MCP gateway requests grouped by capability.",
	}, []string{"capability"})
	// requestsByMethod 按 JSON-RPC 方法分类的请求数。
	requestsByMethod = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "mcp_gateway_requests_by_method",
		Help: "MCP gateway requests grouped by JSON-RPC method.",
	}, []string{"method"})
	// rateLimitedTotal 被限流请求数。
	rateLimitedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mcp_gateway_rate_limited_total",
		Help: "MCP gateway requests rejected by rate limiting.",
	})
	// upstreamErrorsTotal 上游错误数。
	upstreamErrorsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mcp_gateway_upstream_errors_total",
		Help: "MCP gateway upstream errors.",
	})
	// healthCheckFailuresTotal 健康检查失败数。
	healthCheckFailuresTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mcp_gateway_health_check_failures_total",
		Help: "MCP gateway upstream health check failures.",
	})
	// uptimeSeconds 网关运行时长。
	uptimeSeconds = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "mcp_gateway_uptime_seconds",
		Help: "MCP gateway uptime in seconds.",
	})
	// upstreamHealthy 上游健康状态（1/0）。
	upstreamHealthy = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "mcp_gateway_upstream_healthy",
		Help: "Upstream MCP adapter health state (1 healthy, 0 unhealthy).",
	}, []string{"upstream"})

	// toolCallsTotal 按工具统计的调用数。
	toolCallsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "mcp_gateway_tool_calls_total",
		Help: "MCP tool calls grouped by tool and outcome.",
	}, []string{"tool", "outcome"})
	// toolLatency 工具调用耗时。
	toolLatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "mcp_gateway_tool_call_latency_seconds",
		Help:    "MCP tool call latency in seconds.",
		Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30},
	}, []string{"tool"})
	// authzDeniedTotal 授权拒绝数（按原因）。
	authzDeniedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "mcp_gateway_authz_denied_total",
		Help: "MCP gateway authorization denials grouped by reason.",
	}, []string{"reason"})
	// keyChecksTotal 凭证校验计数（按结果）。
	keyChecksTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "mcp_gateway_key_checks_total",
		Help: "MCP gateway credential checks grouped by result.",
	}, []string{"result"})
	// toolAllowChecksTotal 「key × 工具」允许表执法计数。
	toolAllowChecksTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "mcp_gateway_tool_allow_checks_total",
		Help: "MCP gateway per-key tool allow-list enforcement counts.",
	}, []string{"result"})
	// bindingsChecksTotal 一 key 多后端绑定执法计数。
	bindingsChecksTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "mcp_gateway_bindings_checks_total",
		Help: "MCP gateway per-key backend binding enforcement counts.",
	}, []string{"result"})
	// circuitOpenTotal 熔断拒绝数。
	circuitOpenTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mcp_gateway_circuit_open_total",
		Help: "MCP gateway calls rejected because the upstream circuit is open.",
	})
	// approvalsRequiredTotal 需要审批而缺失审批凭据的调用数。
	approvalsRequiredTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mcp_gateway_approvals_required_total",
		Help: "MCP tool calls rejected because an approval grant is required.",
	})
	// redactionsTotal 出参脱敏次数。
	redactionsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mcp_gateway_redactions_total",
		Help: "MCP tool results redacted before returning to the caller.",
	})
)
