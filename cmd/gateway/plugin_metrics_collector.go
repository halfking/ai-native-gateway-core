// Package main - 插件资源指标导出器（resource-limiter 集成 §5.3）。
//
// 数据流：limiter（cgroup 读回 / monitor 轮询产出 lastStats + violation/oom
// 计数，经回调写入 Prometheus）→ 本 collector 周期遍历 Supervisor 的全部
// limiter，把「声明限制 / 生效模式」这类静态-慢变信息写进 gauge。usage 类
// gauge 同样由本 collector 刷新（读 GetStats 快照），保持「一处遍历」——
// 不在 limiter 内部直接打 gauge，避免双写竞争与 plugin-runtime → metrics
// gauge 语义耦合。
//
// 生命周期与 healthLoop 一致：插件启用时启动，gateway 退出时停止。
package main

import (
	"time"

	"github.com/kaixuan/llm-gateway-go/metrics"
	"github.com/kaixuan/llm-gateway-go/plugin-runtime"
	"github.com/kaixuan/llm-gateway-go/plugin-runtime/limiter"
)

// pluginMetricsCollector 周期把 Supervisor 下每个插件的资源限制快照导出为
// Prometheus gauge。零 limiter（未启用资源限制）时天然空转、零开销。
type pluginMetricsCollector struct {
	sup      *pluginruntime.Supervisor
	interval time.Duration
	cancel   chan struct{}
	done     chan struct{}
}

// newPluginMetricsCollector 构造导出器（interval<=0 用默认 15s——gauge 刷新
// 频率无需达到 limiter 采样的 5s，Prometheus 抓取间隔通常 15-30s）。
func newPluginMetricsCollector(sup *pluginruntime.Supervisor, interval time.Duration) *pluginMetricsCollector {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	return &pluginMetricsCollector{
		sup:      sup,
		interval: interval,
		cancel:   make(chan struct{}),
		done:     make(chan struct{}),
	}
}

// Start 启动后台刷新循环（立即刷一次，之后按 interval）。多次调用不支持。
func (c *pluginMetricsCollector) Start() {
	go func() {
		defer close(c.done)
		ticker := time.NewTicker(c.interval)
		defer ticker.Stop()
		c.refresh() // 立即一次：短生命周期测试也能采到样本
		for {
			select {
			case <-c.cancel:
				return
			case <-ticker.C:
				c.refresh()
			}
		}
	}()
}

// Stop 停止刷新循环（等当前一轮刷完）。幂等。
func (c *pluginMetricsCollector) Stop() {
	select {
	case <-c.cancel:
	default:
		close(c.cancel)
	}
	<-c.done
}

// refresh 遍历 supervisor 当前登记的 limiter 并刷新 gauge。
// pluginID 枚举面：受 manifest 声明的 ResourceLimits 存在性约束（无限制的
// 插件不登记 limiter），随启用插件数线性、有界（GW-00 合规）。
func (c *pluginMetricsCollector) refresh() {
	if c.sup == nil {
		return
	}
	for _, st := range c.sup.States() {
		rl := c.sup.LimiterOf(st.PluginID)
		if rl == nil {
			continue
		}
		exportLimiterMetrics(st.PluginID, rl)
	}
}

// exportLimiterMetrics 把单个插件的 limiter 快照写入 metrics gauge。
// 运行中插件停止后其序列残留最后一次读数（Prometheus 惯例：由抓取侧按
// up/时间戳判断失效，不在 collector 里做删除——Delete 会让面板出现
// 「序列消失」断点，比停在末值更难解读）。
func exportLimiterMetrics(pluginID string, rl *limiter.ResourceLimiter) {
	switch rl.Mode() {
	case limiter.ModeCgroup:
		metrics.PluginLimiterModeInfo.WithLabelValues(pluginID, metrics.PluginLimiterModeCgroup).Set(1)
		metrics.PluginLimiterModeInfo.WithLabelValues(pluginID, metrics.PluginLimiterModeMonitor).Set(0)
	case limiter.ModeMonitor:
		metrics.PluginLimiterModeInfo.WithLabelValues(pluginID, metrics.PluginLimiterModeMonitor).Set(1)
		metrics.PluginLimiterModeInfo.WithLabelValues(pluginID, metrics.PluginLimiterModeCgroup).Set(0)
	default:
		metrics.PluginLimiterModeInfo.WithLabelValues(pluginID, metrics.PluginLimiterModeDisabled).Set(1)
	}

	if stats := rl.GetStats(); stats != nil {
		metrics.PluginMemoryUsageBytes.WithLabelValues(pluginID).Set(float64(stats.MemoryBytes))
		metrics.PluginCPUPercent.WithLabelValues(pluginID).Set(stats.CPUPercent)
		metrics.PluginThreads.WithLabelValues(pluginID).Set(float64(stats.NumThreads))
	}

	if limits := rl.Limits(); limits != nil && limits.Memory != nil {
		if bytes, err := limiter.ParseMemoryLimit(limits.Memory.Limit); err == nil {
			metrics.PluginMemoryLimitBytes.WithLabelValues(pluginID).Set(float64(bytes))
		}
	}
}
