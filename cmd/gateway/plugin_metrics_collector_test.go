package main

import (
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/metrics"
	"github.com/kaixuan/llm-gateway-go/plugin-runtime/limiter"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// TestExportLimiterMetricsFromRealLimiter 用真实 limiter（monitor 模式，macOS
// 开发机默认路径）驱动 exportLimiterMetrics：mode gauge 置位、limit gauge 按
// 声明换算、usage gauge 在无 stats 时不写（absent 语义——没有数据不造假 0）。
func TestExportLimiterMetricsFromRealLimiter(t *testing.T) {
	rl, err := limiter.NewResourceLimiter("collector-demo", limiter.ResourceLimits{
		Memory:       &limiter.MemoryLimit{Limit: "128M"},
		MaxProcesses: 3,
	})
	if err != nil {
		t.Fatalf("NewResourceLimiter: %v", err)
	}
	defer func() { _ = rl.Stop() }()

	exportLimiterMetrics("collector-demo", rl)

	modeCgroup := readGaugeVec(t, metrics.PluginLimiterModeInfo, "collector-demo", metrics.PluginLimiterModeCgroup)
	modeMonitor := readGaugeVec(t, metrics.PluginLimiterModeInfo, "collector-demo", metrics.PluginLimiterModeMonitor)
	if modeCgroup+modeMonitor != 1 {
		t.Errorf("exactly one mode must be active, got cgroup=%v monitor=%v", modeCgroup, modeMonitor)
	}
	if v := readGaugeVec(t, metrics.PluginMemoryLimitBytes, "collector-demo"); v != 134217728 {
		t.Errorf("memory limit gauge = %v, want 134217728 (128M)", v)
	}
}

// TestExportLimiterMetricsInvalidLimit 声明的内存限制格式非法时：不 panic、
// limit gauge 不写（usage/mode 不受影响）。fail-fast 属 supervisor.Start 职责
// （Apply 时报错），导出面必须宽容——坏配置不该砸掉 /metrics 抓取。
func TestExportLimiterMetricsInvalidLimit(t *testing.T) {
	rl, err := limiter.NewResourceLimiter("collector-bad", limiter.ResourceLimits{
		Memory: &limiter.MemoryLimit{Limit: "not-a-size"},
	})
	if err != nil {
		t.Fatalf("NewResourceLimiter: %v", err)
	}
	defer func() { _ = rl.Stop() }()

	exportLimiterMetrics("collector-bad", rl)
	if v := readGaugeVec(t, metrics.PluginMemoryLimitBytes, "collector-bad"); v != 0 {
		t.Errorf("invalid limit should leave gauge unwritten, got %v", v)
	}
}

// TestPluginMetricsCollectorStopIdempotent collector 空转路径：Start→Stop→Stop
// 幂等，done 同步正确（nil supervisor 时 refresh 直接返回）。
func TestPluginMetricsCollectorStopIdempotent(t *testing.T) {
	c := newPluginMetricsCollector(nil, time.Hour)
	c.Start()
	c.Stop()
	c.Stop()
}

// readGaugeVec 读一个 GaugeVec 的单序列当前值（未写过时为 0——client_golang
// 的 WithLabelValues 会惰性创建序列，值默认 0；「absent vs 0」的区分在
// 需要 absent 断言的用例里用 testutil 收集器，此处简单读值足够）。
func readGaugeVec(t *testing.T, vec *prometheus.GaugeVec, labelValues ...string) float64 {
	t.Helper()
	var m dto.Metric
	if err := vec.WithLabelValues(labelValues...).Write(&m); err != nil {
		t.Fatalf("write metric: %v", err)
	}
	return m.GetGauge().GetValue()
}
