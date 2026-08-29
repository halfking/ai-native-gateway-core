package proxy

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// TestNewMetrics 验证所有指标都能正确注册。
func TestNewMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)

	if m == nil {
		t.Fatal("NewMetrics returned nil")
	}

	// 验证指标字段不为 nil
	if m.subscriptionsTotal == nil {
		t.Error("subscriptionsTotal is nil")
	}
	if m.nodesTotal == nil {
		t.Error("nodesTotal is nil")
	}
	if m.subscriptionRefreshTotal == nil {
		t.Error("subscriptionRefreshTotal is nil")
	}
	if m.nodeHealthCheckTotal == nil {
		t.Error("nodeHealthCheckTotal is nil")
	}
	if m.nodeSelectionTotal == nil {
		t.Error("nodeSelectionTotal is nil")
	}
	if m.passwordDecryptFailedTotal == nil {
		t.Error("passwordDecryptFailedTotal is nil")
	}
	if m.transportCacheSize == nil {
		t.Error("transportCacheSize is nil")
	}
	if m.transportInvalidationsTotal == nil {
		t.Error("transportInvalidationsTotal is nil")
	}
}

// TestSubscriptionRefresh 测试订阅刷新指标。
func TestSubscriptionRefresh(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)

	// 记录成功的刷新
	m.ObserveSubscriptionRefresh("sub12345678abcd", "success", 1.5)
	m.ObserveSubscriptionRefresh("sub12345678abcd", "success", 2.3)

	// 记录失败的刷新
	m.ObserveSubscriptionRefresh("sub87654321xyz", "error", 0.5)

	// 验证 Counter
	counter := getCounterValue(t, m.subscriptionRefreshTotal, "sub12345", "success")
	if counter != 2 {
		t.Errorf("expected 2 success refreshes for sub12345, got %f", counter)
	}

	errorCounter := getCounterValue(t, m.subscriptionRefreshTotal, "sub87654", "error")
	if errorCounter != 1 {
		t.Errorf("expected 1 error refresh for sub87654, got %f", errorCounter)
	}

	// 验证 Histogram 有数据
	histogram := getHistogramCount(t, m.subscriptionRefreshDurationSeconds)
	if histogram != 3 {
		t.Errorf("expected 3 histogram observations, got %d", histogram)
	}
}

// TestSubscriptionNodeCount 测试订阅节点数量指标。
func TestSubscriptionNodeCount(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)

	m.SetSubscriptionNodeCount("sub12345678", 10)
	m.SetSubscriptionNodeCount("sub87654321", 5)

	gauge1 := getGaugeValue(t, m.subscriptionNodeCount, "sub12345")
	if gauge1 != 10 {
		t.Errorf("expected node count 10 for sub12345, got %f", gauge1)
	}

	gauge2 := getGaugeValue(t, m.subscriptionNodeCount, "sub87654")
	if gauge2 != 5 {
		t.Errorf("expected node count 5 for sub87654, got %f", gauge2)
	}
}

// TestHealthCheck 测试节点健康检查指标。
func TestHealthCheck(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)

	m.IncHealthCheck("success")
	m.IncHealthCheck("success")
	m.IncHealthCheck("timeout")
	m.IncHealthCheck("error")

	m.ObserveHealthCheckDuration(0.05)
	m.ObserveHealthCheckDuration(0.15)

	successCount := getCounterValue(t, m.nodeHealthCheckTotal, "success")
	if successCount != 2 {
		t.Errorf("expected 2 success health checks, got %f", successCount)
	}

	timeoutCount := getCounterValue(t, m.nodeHealthCheckTotal, "timeout")
	if timeoutCount != 1 {
		t.Errorf("expected 1 timeout health check, got %f", timeoutCount)
	}

	histogram := getHistogramCount(t, m.nodeHealthCheckDurationSeconds)
	if histogram != 2 {
		t.Errorf("expected 2 duration observations, got %d", histogram)
	}
}

// TestNodeResponseTime 测试节点响应时间指标。
func TestNodeResponseTime(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)

	m.ObserveNodeResponseTime(50)
	m.ObserveNodeResponseTime(150)
	m.ObserveNodeResponseTime(500)

	histogram := getHistogramCount(t, m.nodeResponseTimeMs)
	if histogram != 3 {
		t.Errorf("expected 3 response time observations, got %d", histogram)
	}
}

// TestNodeConsecutiveFailures 测试节点连续失败次数指标。
func TestNodeConsecutiveFailures(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)

	m.ObserveNodeConsecutiveFailures(1)
	m.ObserveNodeConsecutiveFailures(3)
	m.ObserveNodeConsecutiveFailures(5)

	histogram := getHistogramCount(t, m.nodeConsecutiveFailures)
	if histogram != 3 {
		t.Errorf("expected 3 consecutive failure observations, got %d", histogram)
	}
}

// TestNodeSelection 测试节点选择指标。
func TestNodeSelection(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)

	m.ObserveNodeSelection("success", 0.001)
	m.ObserveNodeSelection("success", 0.002)
	m.ObserveNodeSelection("no_healthy", 0.0005)

	successCount := getCounterValue(t, m.nodeSelectionTotal, "success")
	if successCount != 2 {
		t.Errorf("expected 2 successful node selections, got %f", successCount)
	}

	noHealthyCount := getCounterValue(t, m.nodeSelectionTotal, "no_healthy")
	if noHealthyCount != 1 {
		t.Errorf("expected 1 no_healthy node selection, got %f", noHealthyCount)
	}

	histogram := getHistogramCount(t, m.nodeSelectionDurationSeconds)
	if histogram != 3 {
		t.Errorf("expected 3 selection duration observations, got %d", histogram)
	}
}

// TestPasswordDecryptFailed 测试密码解密失败指标。
func TestPasswordDecryptFailed(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)

	m.IncPasswordDecryptFailed()
	m.IncPasswordDecryptFailed()

	counter := getCounterValueSimple(t, m.passwordDecryptFailedTotal)
	if counter != 2 {
		t.Errorf("expected 2 password decrypt failures, got %f", counter)
	}
}

// TestTransportCache 测试 Transport 连接池指标。
func TestTransportCache(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)

	m.SetTransportCacheSize(10)
	gauge := getGaugeValueSimple(t, m.transportCacheSize)
	if gauge != 10 {
		t.Errorf("expected transport cache size 10, got %f", gauge)
	}

	m.IncTransportInvalidation()
	m.IncTransportInvalidation()
	counter := getCounterValueSimple(t, m.transportInvalidationsTotal)
	if counter != 2 {
		t.Errorf("expected 2 transport invalidations, got %f", counter)
	}
}

// TestTruncateID 测试 ID 截取函数。
func TestTruncateID(t *testing.T) {
	tests := []struct {
		id       string
		n        int
		expected string
	}{
		{"sub12345678abcd", 8, "sub12345"},
		{"short", 8, "short"},
		{"exact123", 8, "exact123"},
		{"", 8, ""},
	}

	for _, tt := range tests {
		result := truncateID(tt.id, tt.n)
		if result != tt.expected {
			t.Errorf("truncateID(%q, %d) = %q, expected %q", tt.id, tt.n, result, tt.expected)
		}
	}
}

// TestMetricsReregistration 测试指标重复注册不会 panic。
func TestMetricsReregistration(t *testing.T) {
	reg := prometheus.NewRegistry()
	m1 := NewMetrics(reg)
	m2 := NewMetrics(reg) // 应该返回已存在的指标

	if m1 == nil || m2 == nil {
		t.Fatal("NewMetrics returned nil")
	}

	// 两次调用应该返回相同的底层 collector
	m1.IncHealthCheck("success")
	successCount := getCounterValue(t, m2.nodeHealthCheckTotal, "success")
	if successCount != 1 {
		t.Errorf("expected shared counter to show 1, got %f", successCount)
	}
}

// 辅助函数：获取 CounterVec 的值
func getCounterValue(t *testing.T, vec *prometheus.CounterVec, labels ...string) float64 {
	t.Helper()
	metric := &dto.Metric{}
	counter, err := vec.GetMetricWithLabelValues(labels...)
	if err != nil {
		t.Fatalf("failed to get counter with labels %v: %v", labels, err)
	}
	if err := counter.Write(metric); err != nil {
		t.Fatalf("failed to write counter metric: %v", err)
	}
	return metric.Counter.GetValue()
}

// 辅助函数：获取 Counter 的值
func getCounterValueSimple(t *testing.T, counter prometheus.Counter) float64 {
	t.Helper()
	metric := &dto.Metric{}
	if err := counter.Write(metric); err != nil {
		t.Fatalf("failed to write counter metric: %v", err)
	}
	return metric.Counter.GetValue()
}

// 辅助函数：获取 GaugeVec 的值
func getGaugeValue(t *testing.T, vec *prometheus.GaugeVec, labels ...string) float64 {
	t.Helper()
	metric := &dto.Metric{}
	gauge, err := vec.GetMetricWithLabelValues(labels...)
	if err != nil {
		t.Fatalf("failed to get gauge with labels %v: %v", labels, err)
	}
	if err := gauge.Write(metric); err != nil {
		t.Fatalf("failed to write gauge metric: %v", err)
	}
	return metric.Gauge.GetValue()
}

// 辅助函数：获取 Gauge 的值
func getGaugeValueSimple(t *testing.T, gauge prometheus.Gauge) float64 {
	t.Helper()
	metric := &dto.Metric{}
	if err := gauge.Write(metric); err != nil {
		t.Fatalf("failed to write gauge metric: %v", err)
	}
	return metric.Gauge.GetValue()
}

// 辅助函数：获取 Histogram 的观测次数
func getHistogramCount(t *testing.T, histogram prometheus.Histogram) uint64 {
	t.Helper()
	metric := &dto.Metric{}
	if err := histogram.Write(metric); err != nil {
		t.Fatalf("failed to write histogram metric: %v", err)
	}
	return metric.Histogram.GetSampleCount()
}
