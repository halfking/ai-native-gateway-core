package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// round 4 L5: OmniFree auto/* 路由可观测性指标.
//
// 这些 counter / histogram 直接接入 Prometheus /metrics 端点 (受 admin
// 鉴权保护). 运维可基于此:
//   - 监控 auto/* 流量占比 (OmniFreeAutoRequestsTotal / 总 chat 请求)
//   - 检测 catalog 命中率 (命中率低 → 配置问题)
//   - 配额耗尽触发率 (高频 → 需要扩资源)
//   - 503 no_free_candidates 触发率 (用户体验异常信号)
//   - 429 校准触发率 (上游限流信号)

var (
	// OmniFreeAutoRequestsTotal 按 tenant 统计 auto/* 路由尝试.
	//
	// 2026-08-09: 移除 model label 以符合 GW-00 低基数规范 (model 名空间大,
	// 100+ 模型会导致时序爆炸). 保留 tenant 维度用于租户级流量监控.
	OmniFreeAutoRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "omnifree_auto_requests_total",
		Help: "Total auto/* routing attempts by tenant (model dimension removed per GW-00).",
	}, []string{"tenant"})

	// OmniFreeAutoSuccessTotal 按 tenant 统计成功调用 (executor 返回无 err).
	//
	// 2026-08-09: 同上移除 model label. 与 requests 比值 = 租户成功率.
	OmniFreeAutoSuccessTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "omnifree_auto_success_total",
		Help: "Total auto/* requests completed without error by tenant (model dimension removed per GW-00).",
	}, []string{"tenant"})

	// OmniFreeAutoNoCandidatesTotal 统计 503 no_free_candidates 触发次数.
	//
	// 2026-08-09: 移除 model label, 保留 tenant + reason (reason 为低基数枚举:
	// catalog-empty / provider-resolve-failed / quota-exhausted / unknown).
	OmniFreeAutoNoCandidatesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "omnifree_auto_no_candidates_total",
		Help: "Total 503 no_free_candidates by tenant and reason (model dimension removed per GW-00).",
	}, []string{"tenant", "reason"})

	// OmniFreeInfraFailureTotal 统计基础设施/配置层面失败 (DB 错误、RLS
	// GUC 失败、factory 构建失败). 与 no_candidates (用户意图明确失败)
	// 区分, 用于运维快速定位 "OmniFree 是不是挂了" 而不是 "配额是不是
	// 耗尽了". round 4 补充审计: 这些错误此前会静默 fallback 到普通
	// provider resolver, 掩盖了真实故障.
	OmniFreeInfraFailureTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "omnifree_infra_failure_total",
		Help: "Total 503 responses due to OmniFree infrastructure failure (DB/RLS/factory errors).",
	}, []string{"model", "tenant"})

	// OmniFreeQuotaRecordsTotal 按 window_type 统计 Record 调用次数.
	OmniFreeQuotaRecordsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "omnifree_quota_records_total",
		Help: "Total QuotaTracker.Record calls by window type and success flag.",
	}, []string{"window_type", "success"})

	// OmniFreeQuotaCorrectTotal 统计 429 校准触发.
	OmniFreeQuotaCorrectTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "omnifree_quota_correct_total",
		Help: "Total QuotaTracker.CorrectFromHeaders calls (triggered by upstream 429).",
	})

	// OmniFreeQuotaRecordErrorsTotal 统计 Record 失败.
	OmniFreeQuotaRecordErrorsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "omnifree_quota_record_errors_total",
		Help: "Total QuotaTracker.Record failures.",
	})

	// OmniFreePoolDedupModelsTotal 统计 Pool 去重聚合的 model 数 (gauge).
	OmniFreePoolDedupModelsTotal = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "omnifree_pool_dedup_models_total",
		Help: "Total catalog models in the last pool-dedup aggregation.",
	})

	// OmniFreePoolDedupPoolsTotal 统计 Pool 去重聚合的 pool 数 (gauge).
	OmniFreePoolDedupPoolsTotal = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "omnifree_pool_dedup_pools_total",
		Help: "Total unique pool_keys in the last pool-dedup aggregation.",
	})

	// OmniFreePreflightRejectionsTotal 统计 Preflight 拒绝次数 (按 reason).
	OmniFreePreflightRejectionsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "omnifree_preflight_rejections_total",
		Help: "Total Preflight rejections by reason.",
	}, []string{"reason"})

	// OmniFreeGetCandidatesDuration 测量 GetCandidates 并行总耗时.
	OmniFreeGetCandidatesDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "omnifree_get_candidates_duration_seconds",
		Help:    "Duration of the parallel provider.GetCandidates fan-out for auto/*.",
		Buckets: prometheus.DefBuckets,
	})
)
