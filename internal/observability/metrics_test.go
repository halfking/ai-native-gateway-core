package observability

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestMockProbeScopeConst 钉死 scope 常量值（/metrics 验收依据）。
func TestMockProbeScopeConst(t *testing.T) {
	if ScopeMockProbe != "mock_probe" {
		t.Fatalf("ScopeMockProbe = %q, want %q", ScopeMockProbe, "mock_probe")
	}
}

// TestMockProbeMetricsRegistered 验证两个 collector 均已注册到
// DefaultRegisterer（gateway-v2 /metrics 用该注册表输出）。重复注册会
// panic，能通过即证明注册成功且唯一。
func TestMockProbeMetricsRegistered(t *testing.T) {
	for _, c := range []prometheus.Collector{MockProbeRequestTotal, MockProbeLatencySeconds} {
		if err := prometheus.DefaultRegisterer.Register(c); err == nil {
			t.Fatal("collector was not pre-registered by promauto")
		}
	}
}

// TestMockProbeMetricExposition 验证打点后指标以 scope="mock_probe" 维度
// 暴露（ acceptance：Prometheus 抓 /metrics 看到 scope="mock_probe"），
// 且 histogram 序列名为 mock_probe_latency_seconds_bucket（client_golang
// 自动追加 _bucket，Name 不应再带后缀）。
func TestMockProbeMetricExposition(t *testing.T) {
	MockProbeRequestTotal.WithLabelValues(ScopeMockProbe, "mock-fast", "true", "ok").Inc()
	MockProbeRequestTotal.WithLabelValues(ScopeMockProbe, "mock-slow", "false", "error").Add(2)
	MockProbeLatencySeconds.WithLabelValues(ScopeMockProbe, "mock-fast", "true").Observe(0.042)

	counterText := testutil.CollectAndCount(MockProbeRequestTotal)
	if counterText < 2 {
		t.Fatalf("expected >=2 counter series, got %d", counterText)
	}
	if got := testutil.ToFloat64(MockProbeRequestTotal.WithLabelValues(ScopeMockProbe, "mock-fast", "true", "ok")); got != 1 {
		t.Fatalf("fast:stream:ok counter = %v, want 1", got)
	}
	if got := testutil.ToFloat64(MockProbeRequestTotal.WithLabelValues(ScopeMockProbe, "mock-slow", "false", "error")); got != 2 {
		t.Fatalf("slow:nonstream:error counter = %v, want 2", got)
	}

	histCount := testutil.CollectAndCount(MockProbeLatencySeconds, "mock_probe_latency_seconds")
	if histCount == 0 {
		t.Fatal("histogram family mock_probe_latency_seconds has no series")
	}
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	// proto family 用基础名；_bucket/_sum/_count 是文本暴露层的展开。
	for _, f := range families {
		if f.GetName() == "mock_probe_latency_seconds" {
			for _, m := range f.GetMetric() {
				if m.GetHistogram().GetSampleCount() != 1 {
					t.Fatalf("histogram sample count = %d, want 1", m.GetHistogram().GetSampleCount())
				}
			}
		}
		if f.GetName() == "mock_probe_request_total" || f.GetName() == "mock_probe_latency_seconds" {
			for _, m := range f.GetMetric() {
				found := false
				for _, l := range m.GetLabel() {
					if l.GetName() == "scope" && l.GetValue() == "mock_probe" {
						found = true
					}
				}
				if !found {
					t.Fatalf("metric %s missing scope=\"mock_probe\" label", f.GetName())
				}
			}
		}
	}
}
