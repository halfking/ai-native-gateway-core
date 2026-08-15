// metrics_strategy.go - GW-03/GW-04 路由策略 shadow diff 的 Prometheus 指标。
//
// 仅观测，不影响线上路由。labels 刻意低基数：
//   - strategy: p2c|cost-optimized|cache-optimized|context-aware|headroom
//   - outcome: agreed|disagreed
//
// 不使用 model/tenant_id/credential_id/request_id 等高基数维度（GW-00 门禁）。
package executors

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var shadowStrategyOutcomes = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "llmgw_routing_shadow_strategy_outcomes_total",
		Help: "Routing shadow strategy diff outcomes (observed only, does not change selection). " +
			"strategy=the shadow strategy name; outcome=agreed|disagreed vs the actual P2C/bandit winner.",
	},
	[]string{"strategy", "outcome"},
)

func recordShadowStrategyOutcome(strategy, outcome string) {
	shadowStrategyOutcomes.WithLabelValues(strategy, outcome).Inc()
}
