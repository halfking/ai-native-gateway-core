package transport

import (
	"bufio"
	"errors"
	"io"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	conversionTotal           *prometheus.CounterVec
	conversionErrors          *prometheus.CounterVec
	activeImplementationGauge *prometheus.GaugeVec
	conversionDuration        *prometheus.HistogramVec
)

func init() {
	conversionTotal = registerCounterVec("transport_conversion_total", "Number of protocol conversions by implementation and direction.", []string{"implementation", "direction"})
	conversionErrors = registerCounterVec("transport_conversion_errors_total", "Number of protocol conversion errors by implementation and direction.", []string{"implementation", "direction"})
	conversionDuration = registerHistogramVec("transport_conversion_duration_seconds", "Protocol conversion latency in seconds.", []string{"implementation", "direction"})
	activeImplementationGauge = registerGaugeVec("transport_active_implementation", "Whether the transport implementation is currently active (1=ir, 0=legacy).", []string{"implementation"})
}

func registerCounterVec(name, help string, labels []string) *prometheus.CounterVec {
	v := prometheus.NewCounterVec(prometheus.CounterOpts{Name: name, Help: help}, labels)
	if err := prometheus.Register(v); err != nil {
		var are prometheus.AlreadyRegisteredError
		if !errors.As(err, &are) {
			panic(err)
		}
		return are.ExistingCollector.(*prometheus.CounterVec)
	}
	return v
}

func registerGaugeVec(name, help string, labels []string) *prometheus.GaugeVec {
	v := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: name, Help: help}, labels)
	if err := prometheus.Register(v); err != nil {
		var are prometheus.AlreadyRegisteredError
		if !errors.As(err, &are) {
			panic(err)
		}
		return are.ExistingCollector.(*prometheus.GaugeVec)
	}
	return v
}

func registerHistogramVec(name, help string, labels []string) *prometheus.HistogramVec {
	v := prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: name, Help: help, Buckets: prometheus.DefBuckets}, labels)
	if err := prometheus.Register(v); err != nil {
		var are prometheus.AlreadyRegisteredError
		if !errors.As(err, &are) {
			panic(err)
		}
		return are.ExistingCollector.(*prometheus.HistogramVec)
	}
	return v
}

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
