package transport

import (
	"bufio"
	"io"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// conversionTotal 累计转换次数，按实现/方向打标签。
	conversionTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "transport_conversion_total",
		Help: "Number of protocol conversions by implementation and direction.",
	}, []string{"implementation", "direction"})

	// conversionErrors 累计转换错误次数。
	conversionErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "transport_conversion_errors_total",
		Help: "Number of protocol conversion errors by implementation and direction.",
	}, []string{"implementation", "direction"})

	// activeImplementationGauge 当前活跃实现（0=legacy, 1=ir）。
	activeImplementationGauge = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "transport_active_implementation",
		Help: "Whether the transport implementation is currently active (1=ir, 0=legacy).",
	}, []string{"implementation"})

	// conversionDuration 转换耗时直方图。
	conversionDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "transport_conversion_duration_seconds",
		Help:    "Protocol conversion latency in seconds.",
		Buckets: prometheus.DefBuckets,
	}, []string{"implementation", "direction"})
)

// SetActiveImplementation 设置当前活跃实现。
func SetActiveImplementation(impl string, active bool) {
	v := 0.0
	if active {
		v = 1.0
	}
	activeImplementationGauge.WithLabelValues(impl).Set(v)
}

// bufioReader 返回一个 buffered reader（按行扫描）。
func bufioReader(r io.Reader) *bufio.Reader {
	return bufio.NewReaderSize(r, 64*1024)
}
