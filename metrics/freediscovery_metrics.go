package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// FreeDiscovery Prometheus 指标 (FreeDiscovery 自动发现模块可观测性).
//
// 这些指标接入 /metrics 端点 (受 admin 鉴权保护). 运维可基于此:
//   - 监控扫描任务成功率与耗时 (freediscovery_scans_total / freediscovery_scan_duration_seconds)
//   - 发现资源数量与下游导入转化率
//   - ToS 违规拒绝次数 (运维信号: provider 上游策略可能变更)
//   - URL safety 阻断次数 (SSRF / 私网绕过尝试信号)
//   - 并发活跃扫描数 (gauge, 调度决策依据)
//
// 标签纪律遵循 GW-00: provider / status / reason 等低基数枚举, 不引入 model/tenant 维度
// 以避免时序爆炸 (参考 omnifree_metrics.go 的修订记录).

var (
	// FreeDiscoveryScansTotal 扫描任务完成总数, 按 provider + 终态 status 分桶.
	//
	// status 取值: success / failed / template_disabled / template_not_found.
	FreeDiscoveryScansTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "freediscovery_scans_total",
		Help: "Total FreeDiscovery scan tasks completed, by provider and final status.",
	}, []string{"provider", "status"})

	// FreeDiscoveryScanDurationSeconds 单次扫描耗时 (从 pending 转 running 到终态).
	//
	// 默认 buckets; 用于分析扫描慢点 (上游响应慢 / DNS 解析慢 / 规则匹配慢).
	FreeDiscoveryScanDurationSeconds = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "freediscovery_scan_duration_seconds",
		Help:    "Duration of a FreeDiscovery scan task from running to terminal state.",
		Buckets: prometheus.DefBuckets,
	})

	// FreeDiscoveryModelsDiscoveredTotal 单次扫描成功识别的模型数 (直方图桶计).
	FreeDiscoveryModelsDiscoveredTotal = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "freediscovery_models_discovered_total",
		Help:    "Distribution of models discovered per scan task (counted at saveResults).",
		Buckets: []float64{0, 1, 5, 10, 25, 50, 100, 250, 500},
	})

	// FreeDiscoveryResourcesDiscoveredTotal 全量累计的发现模型数 (counter).
	FreeDiscoveryResourcesDiscoveredTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "freediscovery_resources_discovered_total",
		Help: "Cumulative count of models discovered across all scans (success path).",
	})

	// FreeDiscoveryTosViolationsTotal ToS 关键词命中违规计数.
	//
	// verdict 取值: avoid / caution (与 tos_checker.go 的 verdict 字符串对齐).
	FreeDiscoveryTosViolationsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "freediscovery_tos_violations_total",
		Help: "Total ToS rule hits that produced avoid / caution verdict during scan.",
	}, []string{"provider", "verdict"})

	// FreeDiscoveryImportTotal 导入结果总数, 按状态分桶.
	//
	// status 取值: imported / skipped / conflicted (与 ImportSummary 字段对齐).
	FreeDiscoveryImportTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "freediscovery_import_total",
		Help: "Total FreeDiscovery import outcomes, by outcome status.",
	}, []string{"status"})

	// FreeDiscoveryURLSafetyBlockedTotal URL safety 校验拒绝次数, 按 reason 分桶.
	//
	// reason 取值: base_url_<reason> / models_endpoint_<reason> 拼接, 见 url_safety.go.
	FreeDiscoveryURLSafetyBlockedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "freediscovery_url_safety_blocked_total",
		Help: "Total URL safety rejections, by reason (low-cardinality bucket).",
	}, []string{"reason"})

	// FreeDiscoveryActiveScans 当前活跃 (running) 扫描任务数 (gauge).
	//
	// 在 updateTask(running) 增 1, 在 success/failed 减 1. 用于调度决策:
	// 单租户并发上限触发判定的输入之一.
	FreeDiscoveryActiveScans = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "freediscovery_active_scans",
		Help: "Current number of FreeDiscovery scan tasks in running state.",
	})
)
