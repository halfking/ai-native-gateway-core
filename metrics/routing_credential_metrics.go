// Package metrics - Routing 凭据状态机可观测性指标.
//
// 2026-08-23 hzx-2 audit: 增加以下指标用于诊断和监控凭据状态机故障：
//   - admin reset 触发的次数（按 surface / result）
//   - 后台 credential_recovery 30s tick 的恢复动作（按 outcome）
//   - 后台 health_auto_recover 1min tick 的运行情况
//   - 主动自检（autoheal）worker 的调度结果
//   - 路由层 "全员冷却降级" 的触发计数
//   - 当前 in-memory 凭据状态（gauge）
//
// 这些指标的命名遵循 llmgw_ 前缀（项目惯例）和 GW-00 低基数规范。
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// RoutingCredentialResetTotal 统计 admin 端点触发的凭据状态 reset。
	//
	// surface ∈ {lookup, db, in_memory_circuit, in_memory_credstate, redis_fpslot, ursmv2}
	// result ∈ {ok, skipped, error, not_found}
	//
	// surface=lookup/result=not_found 区分 404（credential id 不存在，操作员
	// 敲错 ID）与 500（reset 链路内部错误）；两者在 HTTP 层返回不同状态码，
	// 指标层也应可区分（hzx-2 round-3 follow-up）。
	//
	// 运维可通过 "result=error" 的趋势判断 resetter 注入是否完整；
	// 通过 "result=ok" 的总量判断节点被反复救活的频率（过高表示状态机有 bug）。
	RoutingCredentialResetTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "llmgw_routing_credential_reset_total",
		Help: "Admin-triggered credential state resets, by surface and result.",
	}, []string{"surface", "result"})

	// RoutingCredentialRecoveryTotal 统计 bg/credential_recovery.go 30s tick
	// 每行 SQL 的恢复结果。
	//
	// outcome ∈ {recovered, blocked_by_quota, blocked_by_permanent_kind,
	//             already_healthy, no_row, error}
	//
	// 大量 "blocked_by_permanent_kind" 是预期的（写 NULL recover_at 的 kind
	// 不会被自动恢复），但持续上涨意味着状态机设计需要扩展守卫。
	RoutingCredentialRecoveryTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "llmgw_routing_credential_recovery_total",
		Help: "credential_recovery 30s tick per-row recovery outcomes.",
	}, []string{"sql_kind", "outcome"})

	// RoutingCredentialRecoveryNotifyTotal counts the cache-invalidate /
	// probe-submit notifications fired from credential_recovery.recover()
	// after a state-machine flip. action ∈ {invalidate, probe_submit}.
	// sql_kind reuses the same labels as RoutingCredentialRecoveryTotal so
	// operators can correlate "rows recovered" with "notifications fired":
	// a recovered row with no notify counter increment indicates the hook
	// is nil-wired (early boot, partial config) or the dispatch path is
	// degraded.
	//
	// 2026-08-26 (quota-recovery-notify fix): without these counters the
	// "recovery happens but routing layer keeps stale state" regression is
	// invisible — the 30s tick logs "recovered=4" while the candidate
	// cache continues to exclude the credential until the next TTL or
	// probe tick.
	RoutingCredentialRecoveryNotifyTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "llmgw_routing_credential_recovery_notify_total",
		Help: "credential_recovery 30s tick post-flip notifications (invalidate / probe_submit).",
	}, []string{"sql_kind", "action"})

	// RoutingCredentialQuotaRecoveredNotifyTotal counts the dispatcher-facing
	// notifications fired by the probe success paths (cycleAll / fastProbe /
	// probeQueueWorker) once a credential's quota / availability state has
	// been flipped back to healthy after a recharge-style recovery. The label
	// `source` distinguishes the origin so operators can correlate the
	// notification with the originating probe path:
	//   - "cycle_all": CredentialProbeV2.cycleAll hourly sweep
	//   - "fast_probe": ProbeQueueWorker.processTask (5-min delayed reprobe,
	//     includes the SubmitFastProbe / ProbeNowAsync queue path)
	//
	// 2026-08-26 quota-recovery-notify fix: the counter exists so an operator
	// can confirm the wiring in main.go fired (probe-side metric, side of
	// the boundary) and reconcile it against the dispatcher's
	// invalidate_count (cache-side metric).
	RoutingCredentialQuotaRecoveredNotifyTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "llmgw_routing_credential_quota_recovered_notify_total",
		Help: "Probe-side quota-recovery notifications, labeled by source probe path.",
	}, []string{"source"})

	// RoutingCredentialRecoveryTickDurationSeconds bg/credential_recovery
	// 主循环最近一次完整跑的耗时。
	RoutingCredentialRecoveryTickDurationSeconds = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "llmgw_routing_credential_recovery_tick_duration_seconds",
		Help: "Last credential_recovery.run() wall-clock duration.",
	})

	// RoutingHealthAutoRecoverTotal health_auto_recover 1min tick 的运行结果。
	RoutingHealthAutoRecoverTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "llmgw_routing_health_auto_recover_total",
		Help: "health_auto_recover 1min tick outcomes.",
	}, []string{"outcome"})

	// RoutingHealthAutoRecoverTickDurationSeconds health_auto_recover 主循环耗时。
	RoutingHealthAutoRecoverTickDurationSeconds = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "llmgw_routing_health_auto_recover_tick_duration_seconds",
		Help: "Last health_auto_recover.run() wall-clock duration.",
	})

	// RoutingAutoHealSubmitTotal 主动自检（bg/credential_autoheal.go）的
	// NodeProbeWorker.Submit 调用计数。
	//
	// outcome ∈ {submitted, skipped_no_due, error}
	RoutingAutoHealSubmitTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "llmgw_routing_auto_heal_submit_total",
		Help: "bg/credential_autoheal scheduled probe submits.",
	}, []string{"outcome"})

	// RoutingCoolingFallbackTotal router "全员冷却降级" 的触发计数。
	//
	// 当前唯一 reason 为 all_unusable（router.go planCandidates 中所有候选
	// 均冷却时选最浅冷却候选兜底）。
	RoutingCoolingFallbackTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "llmgw_routing_cooling_fallback_total",
		Help: "All-candidates-cooling fallback triggers in router.go planCandidates.",
	}, []string{"reason"})

	// RoutingPriorityCandidatesSelectedTotal records the selected routing
	// bucket at the end of planCandidates, classified by the first-attempt
	// candidate. outcome ∈ {priority_only, spillover_to_non_priority,
	// no_priority_candidates}:
	//   - priority_only: ordered[0] is priority-eligible (priority bucket won)
	//   - spillover_to_non_priority: a priority candidate existed in the pool
	//     but a standard candidate is attempted first
	//   - no_priority_candidates: no priority-eligible candidate in the pool
	//     (baseline; expected when the feature flag is unused)
	RoutingPriorityCandidatesSelectedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "llmgw_routing_priority_candidates_selected_total",
		Help: "Routing selections by priority candidate outcome.",
	}, []string{"outcome"})
)
