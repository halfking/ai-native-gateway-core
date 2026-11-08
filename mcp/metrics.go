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
	// toolAllowResultTotal 「key × 工具」判定的**分结果**计数（治理面视角）。
	//
	// ★ 指标名仍是 mcp_gateway_tool_allow_checks_total；Go 标识符与遗留旧名族
	//   toolAllowChecksTotal 区分开，避免同名冲突。
	toolAllowResultTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "mcp_gateway_tool_allow_checks_total",
		Help: "MCP gateway per-key tool allow-list enforcement counts.",
	}, []string{"result"})
	// bindingsResultTotal 一 key 多后端绑定判定的**分结果**计数（治理面视角）。
	//
	// ★ 指标名仍是 mcp_gateway_bindings_checks_total；Go 标识符与遗留旧名族
	//   bindingsChecksTotal 区分开，避免同名冲突。
	bindingsResultTotal = promauto.NewCounterVec(prometheus.CounterOpts{
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

	// ────────────────────────────────────────────────────────────────────────
	// 既有 tools/mcp-gateway 的**旧名**指标族（逐个沿用，便于既有告警规则与
	// 看板平移：老规则写的是裸名 tool_allow_* / bindings_* / keys_*）。
	//
	// ★ 为什么不并进 mcp_gateway_* ：既有告警规则查的是**旧族名**，Prometheus
	//   的族名必须一致才会命中 ⇒ 新增一个前缀不同的族，老规则永远静默。
	//   ⇒ 这里按旧名**原样**建族，并让同一事件同时可见于新族与旧族。
	// ────────────────────────────────────────────────────────────────────────

	// toolAllowChecksTotal 「key × 工具」判定次数。
	toolAllowChecksTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "tool_allow_checks_total",
		Help: "Per-key tool allow-list checks (legacy name kept for alert-rule parity).",
	})
	// toolAllowDeniedTotal 判定为拒绝的次数。
	toolAllowDeniedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "tool_allow_denied_total",
		Help: "Tool allow-list checks that denied the call (legacy name kept for alert-rule parity).",
	})
	// toolAllowNotEnforcedTotal **允许表为空 ⇒ 执法跳过**（不收口）的次数。
	//
	// ★ 这是既有最重要的可观测点之一：它高 ⇒ 收口根本没生效（空白名单）。
	toolAllowNotEnforcedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "tool_allow_not_enforced_total",
		Help: "Tool allow-list checks skipped because the credential has no non-empty allow list (legacy name).",
	})
	// toolAllowLookupErrorsTotal 允许表查询出错 ⇒ **fail-open** 的次数。
	toolAllowLookupErrorsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "tool_allow_lookup_errors_total",
		Help: "Allow-list lookup errors that caused fail-open (legacy name).",
	})
	// toolAllowLookupMissesTotal key 不在远端账本的次数。
	toolAllowLookupMissesTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "tool_allow_lookup_misses_total",
		Help: "Allow-list lookups for keys that are absent from the remote ledger (legacy name).",
	})
	// toolAllowAccRouteMissingTotal 远端账本**没有该路由**（版本未部署）的次数。
	toolAllowAccRouteMissingTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "tool_allow_acc_route_missing_total",
		Help: "Remote ledger route missing (enforcement plane not deployed) (legacy name).",
	})
	// toolAllowAcc404UnrecognizedTotal 远端 404 的**形状不认得**的次数。
	toolAllowAcc404UnrecognizedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "tool_allow_acc_404_unrecognized_total",
		Help: "Remote ledger returned a 404 whose shape is not recognized (legacy name).",
	})

	// bindingsChecksTotal 一 key 多后端绑定检查次数。
	bindingsChecksTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "bindings_checks_total",
		Help: "Per-key backend binding checks (legacy name kept for alert-rule parity).",
	})
	// bindingsDeniedTotal 绑定被单边禁用而拒绝的次数。
	bindingsDeniedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "bindings_denied_total",
		Help: "Backend binding checks denied by an administrator (legacy name).",
	})
	// bindingsInactiveTotal 凭证失效/吊销而拒绝的次数。
	bindingsInactiveTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "bindings_inactive_total",
		Help: "Backend binding checks rejected because the credential is inactive (legacy name).",
	})
	// bindingsLookupErrorsTotal 绑定查询出错 ⇒ fail-open 的次数。
	bindingsLookupErrorsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "bindings_lookup_errors_total",
		Help: "Backend binding lookup errors that caused fail-open (legacy name).",
	})
	// bindingsLookupMissesTotal key 不在远端账本的次数。
	bindingsLookupMissesTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "bindings_lookup_misses_total",
		Help: "Backend binding lookups for keys absent from the remote ledger (legacy name).",
	})
)
