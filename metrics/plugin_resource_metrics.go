// Package metrics - 插件资源限制可观测性指标（resource-limiter 集成 §5.3）.
//
// 背景：plugin-runtime/limiter 把插件进程接线到 cgroup v2 硬限制（Linux）
// 或监控模式（macOS / cgroup 不可用降级）。阶段 1 交付后限制「是否生效、
// 是否够用」没有任何观察面：macOS 监控循环只在超限时打 fmt WARN（进网关
// 日志流，无聚合无告警面）；Linux cgroup 模式写完 memory.max 就不再读回。
// 没有埋点，灰度期 §5.2 的默认限制（512M/50%/10）是否够用只能靠猜。
//
// 本文件补上那个观察面。指标族与 resource-limiter-integration-plan.md §5.3
// 一一对应（命名改为 llmgw_ 前缀 + plugin_id label 的仓库惯例形式）：
//
//	llmgw_plugin_memory_usage_bytes{plugin_id}          内存使用（gauge）
//	llmgw_plugin_memory_limit_bytes{plugin_id}          内存硬限制（gauge）
//	llmgw_plugin_cpu_usage_percent{plugin_id}           CPU 使用（gauge）
//	llmgw_plugin_threads{plugin_id}                     线程/进程数（gauge）
//	llmgw_plugin_limiter_mode_info{plugin_id,mode}      生效模式（info=1）
//	llmgw_plugin_limit_violations_total{plugin_id,kind} 超限警告计数（cgroup+monitor）
//	llmgw_plugin_oom_killed_total{plugin_id}            cgroup OOM 终止计数（Linux）
//
// 数据来源：
//   - monitor 模式：limiter 自身的 5s 轮询已产出 ProcessStats（GetStats 读取，
//     无额外往返）；本文件的超限检测回调在既有 WARN 点同步触发。
//   - cgroup 模式：limiter 新增同周期读回（memory.current / cpu.stat /
//     pids.current / memory.events），写 lastStats + violation/oom 计数，
//     与 monitor 模式共享同一导出面。
//   - 导出面：cmd/gateway 侧的 PluginMetricsCollector 周期遍历
//     Supervisor.LimiterOf 把快照写进 gauge（见 plugin_metrics_collector.go）。
//
// 命名与基数（GW-00）：`llmgw_` 前缀；plugin_id 是部署期枚举（enabled 插件
// 清单，非请求维度），随插件安装数线性、有界；kind 是三值闭集
// （memory|cpu|threads）；mode 是三值闭集（cgroup|monitor|disabled）。
// request_id / model / tenant_id 等高基数维度绝不进入 label
// （metrics/label_cardinality_guard_test.go 会拦）。
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// 资源限制超限种类闭集。
const (
	// PluginLimitKindMemory: RSS 超过声明内存限制（monitor 模式检测）。
	PluginLimitKindMemory = "memory"
	// PluginLimitKindCPU: 采样窗口 CPU 百分比超过声明限制（monitor 模式检测）。
	PluginLimitKindCPU = "cpu"
	// PluginLimitKindThreads: 线程数超过 max_processes（monitor 模式检测）。
	PluginLimitKindThreads = "threads"
)

// limiter 生效模式闭集（与 pluginruntime/limiter.LimiterMode 对应）。
const (
	PluginLimiterModeCgroup   = "cgroup"
	PluginLimiterModeMonitor  = "monitor"
	PluginLimiterModeDisabled = "disabled"
)

var (
	// PluginMemoryUsageBytes 插件进程 RSS。cgroup 模式读 memory.current
	// （含 page cache，比 RSS 偏大——判断「是否逼近上限」时注意口径）；
	// monitor 模式读 /proc/<pid>/status VmRSS。未启用限制的插件无序列。
	PluginMemoryUsageBytes = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "llmgw_plugin_memory_usage_bytes",
		Help: "Plugin process memory usage (cgroup memory.current or RSS), by plugin.",
	}, []string{"plugin_id"})

	// PluginMemoryLimitBytes 声明的内存硬限制（memory.max 写入值）。
	// 与 usage 同图对比可直接算水位；limit 变更（热重载）后自动跟随。
	PluginMemoryLimitBytes = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "llmgw_plugin_memory_limit_bytes",
		Help: "Configured plugin memory limit in bytes (memory.max), by plugin.",
	}, []string{"plugin_id"})

	// PluginCPUPercent 插件 CPU 使用百分比（0-100 单核口径 × 核数）。
	// cgroup 模式由 cpu.stat usage_usec 差分推算；monitor 模式 macOS 侧
	// 简化实现暂为 0（见 limiter TODO），有值后本指标自动生效。
	PluginCPUPercent = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "llmgw_plugin_cpu_usage_percent",
		Help: "Plugin process CPU usage percent, by plugin.",
	}, []string{"plugin_id"})

	// PluginThreads 插件线程数（cgroup: pids.current；monitor: /proc Threads）。
	PluginThreads = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "llmgw_plugin_threads",
		Help: "Plugin process thread/task count, by plugin.",
	}, []string{"plugin_id"})

	// PluginLimiterModeInfo 每插件当前生效的限制模式（值为 1）。
	// mode ∈ {cgroup, monitor, disabled}；monitor 大量出现说明 cgroup 创建
	// 在降级（权限/内核不支持），是灰度前必须看清的信号。
	PluginLimiterModeInfo = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "llmgw_plugin_limiter_mode_info",
		Help: "Plugin resource limiter enforcement mode (1 = active), by plugin and mode.",
	}, []string{"plugin_id", "mode"})

	// PluginLimitViolationsTotal 插件超过声明限制的次数。monitor 模式来自
	// 采样检测；cgroup 模式来自软限检测（memory.current > 90% 阈值同款 WARN
	// 逻辑/线程超限）。OOM 造成的终止另计 llmgw_plugin_oom_killed_total。
	PluginLimitViolationsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "llmgw_plugin_limit_violations_total",
		Help: "Plugin resource limit violations observed by the limiter, by plugin and limit kind.",
	}, []string{"plugin_id", "kind"})

	// PluginOOMKilledTotal cgroup memory.events oom_kill 计数（Linux only）。
	// 直接读内核计数器，进程被杀也会累计——比进程侧检测更可靠。
	// > 0 即应有告警（§5.3 阈值表）；持续增长说明默认限制过小。
	PluginOOMKilledTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "llmgw_plugin_oom_killed_total",
		Help: "Plugin processes OOM-killed by cgroup memory limit, by plugin.",
	}, []string{"plugin_id"})
)

func init() {
	// 预热 kind 闭集（plugin_id 用 unknown 占位一次）：保证 violation 指标的
	// 三个 kind 序列声明可见（Registry.Describe 门禁按声明扫描，不依赖样本）。
	// plugin_id 维度不预热——它是部署期开放枚举，占位预热会造出误导序列。
	PluginLimitViolationsTotal.WithLabelValues("unknown", PluginLimitKindMemory).Add(0)
}

// RecordPluginLimitViolation 在 limiter 的超限检测点调用（纯指标，不改控制
// 流——既有 fmt WARN 行为保持）。kind 必须是闭集成员；pluginID 由调用方
// （Supervisor/limiter）持有，理论上有界，仍做空值归一防意外空串序列。
// 注：不预热 0 值序列——plugin_id 是部署期开放枚举，用占位值预热会造出
// plugin_id="unknown" 的误导序列；零违限时序列缺席是标准行为，告警用
// `rate(...) > 0` 自然不触发。
func RecordPluginLimitViolation(pluginID, kind string) {
	PluginLimitViolationsTotal.WithLabelValues(normalizePluginID(pluginID), normalizeKind(kind)).Inc()
}

// RecordPluginOOMKill 读取到 cgroup memory.events oom_kill 增量时调用。
func RecordPluginOOMKill(pluginID string, delta uint64) {
	if delta == 0 {
		return
	}
	PluginOOMKilledTotal.WithLabelValues(normalizePluginID(pluginID)).Add(float64(delta))
}

// normalizePluginID 空插件 id 归一为 "unknown"，避免空 label 值序列。
func normalizePluginID(id string) string {
	if id == "" {
		return "unknown"
	}
	return id
}

// normalizeKind 超限种类归一到闭集，任何未知值落入 "unknown" 桶。
func normalizeKind(kind string) string {
	switch kind {
	case PluginLimitKindMemory, PluginLimitKindCPU, PluginLimitKindThreads:
		return kind
	}
	return "unknown"
}
