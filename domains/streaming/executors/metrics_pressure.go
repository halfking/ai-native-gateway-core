// metrics_pressure.go - Phase 2.4 压力感知 Prometheus 指标
//
// 提供 A/B 测试期间可观测性所需的关键指标：
//   - 压力惩罚应用次数
//   - 压力信号分布（FpSlots / Limiter）
//   - 权重调整幅度
//
// 这些指标用于：
//   - 验证 Feature flag 生效
//   - 对比 A/B 测试期间流量分布变化
//   - 监控压力感知路由的业务影响

package executors

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// pressurePenaltyAppliedTotal 压力惩罚应用总次数
	// labels:
	//   - backend: "ursm_v2" | "legacy"（来自 StateBackend.Name()）
	//   - model: 候选模型名
	pressurePenaltyAppliedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llmgw_pressure_penalty_applied_total",
			Help: "Total number of times pressure penalty was applied to a candidate",
		},
		[]string{"backend", "model"},
	)

	// pressurePenaltyValue 压力惩罚系数分布（直方图）
	// 反映每次调整时实际使用的惩罚强度
	pressurePenaltyValue = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "llmgw_pressure_penalty_value",
			Help:    "Distribution of pressure penalty values applied",
			Buckets: []float64{0, 0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7},
		},
		[]string{"backend"},
	)

	// pressureSignalValue 实时压力信号采样（Gauge）
	// 反映当前请求路径上 FpSlots/Limiter 的压力
	pressureSignalValue = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "llmgw_pressure_signal",
			Help: "Current pressure signal value (0-1)",
		},
		[]string{"source", "credential_id"},
	)

	// weightAdjustmentTotal 权重调整总次数
	// labels:
	//   - direction: "down"（高压力降权）
	weightAdjustmentTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llmgw_weight_adjustment_total",
			Help: "Total weight adjustments due to pressure",
		},
		[]string{"direction"},
	)

	// pressureAwareRoutingEnabled 标记压力感知路由 Feature flag 状态
	pressureAwareRoutingEnabled = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "llmgw_pressure_aware_routing_enabled",
			Help: "Whether pressure-aware routing is enabled (1=enabled, 0=disabled)",
		},
	)

	// pressureQueryFailures 记录压力查询失败次数
	pressureQueryFailures = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llmgw_pressure_query_failures_total",
			Help: "Total number of pressure query failures by source (fpslot or limiter)",
		},
		[]string{"source"}, // "fpslot" or "limiter"
	)
)

// RecordPressurePenalty 记录一次压力惩罚应用（包级别便捷函数）
//
// 参数:
//   - backend: 后端标识（URSMv2Backend.Name() 或 "db_only"）
//   - model: 候选模型名
//   - penalty: 应用的惩罚系数（0-0.7）
func RecordPressurePenalty(backend, model string, penalty float64) {
	pressurePenaltyAppliedTotal.WithLabelValues(backend, model).Inc()
	pressurePenaltyValue.WithLabelValues(backend).Observe(penalty)
	if penalty > 0 {
		weightAdjustmentTotal.WithLabelValues("down").Inc()
	}
}

// RecordPressureSignal 记录压力信号值（包级别便捷函数）
//
// 参数:
//   - source: 信号来源（"fp_slots" 或 "limiter"）
//   - credentialID: 凭据 ID
//   - value: 压力值（0-1）
func RecordPressureSignal(source string, credentialID int, value float64) {
	pressureSignalValue.WithLabelValues(source, strconv.Itoa(credentialID)).Set(value)
}

// SetPressureAwareRoutingEnabled 设置 Feature flag 状态
func SetPressureAwareRoutingEnabled(enabled bool) {
	if enabled {
		pressureAwareRoutingEnabled.Set(1)
	} else {
		pressureAwareRoutingEnabled.Set(0)
	}
}

// RecordPressureQueryFailure 记录压力查询失败
func RecordPressureQueryFailure(source string) {
	pressureQueryFailures.WithLabelValues(source).Inc()
}
