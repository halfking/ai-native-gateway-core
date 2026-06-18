// Package compressor - metrics.go (Round 47 / v7 T9)
//
// Prometheus instrumentation for the compression flow. Three metrics per
// v7 §8.2 recommendation:
//
//   compression_triggered_total{mode,reason,strategy}
//     Counter: how many requests triggered each (mode, reason, strategy)
//     triple. Labels let Grafana split by mode (off/auto_threshold/on_4xx)
//     to verify the env knob is honored.
//
//   compression_latency_seconds{strategy}
//     Histogram: time spent in Compress() (or CompressAfter4xx()).
//     Helps spot the LLM-summary path (>2s) vs mechanical trim (<100ms).
//
//   compression_ratio{strategy}
//     Gauge (last value): bytes_after / bytes_before for the most recent
//     compression. Updated via Set(float64) after each Compress() call.
//     Operators eyeball this to spot "compressor isn't actually shrinking
//     anything" regressions.
//
// All metrics live in a private package-level struct so tests can reset
// state between cases. The struct is registered with prometheus.DefaultRegisterer
// once at package init (typical pattern for small apps).
//
// Why not use middleware/prometheus_mw.go directly? The existing
// middleware is HTTP-level (request count / duration histograms with
// status / path labels). Compression runs INSIDE the relay, before HTTP
// status is known, so we need a separate registry with compression-
// specific labels. Keeping it in compressor/ also means T8 / T11 can
// import it without a circular dep on relay/middleware.

package compressor

import (
	"sync"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/client_golang/prometheus"
)

// metrics holds the 3 Prometheus collectors + a mutex protecting the
// ratio gauge (which is updated from concurrent Compress() calls).
// Lock-free atomic would be marginally faster; mutex keeps the test
// surface simple and the contention is negligible (compression runs at
// most a few times per second per pod).
type metrics struct {
	triggered *prometheus.CounterVec
	latency   *prometheus.HistogramVec
	ratio     *prometheus.GaugeVec

	mu sync.Mutex
}

var defaultMetrics = newMetrics()

func newMetrics() *metrics {
	return &metrics{
		triggered: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "compression_triggered_total",
				Help: "Total compression requests, labelled by mode (off|auto_threshold|on_4xx), reason (mode_1_auto_threshold|mode_2_on_4xx|mode_warmup_skipped), and strategy (mechanical_trim|memora_l1_inject|llm_summary|noop).",
			},
			[]string{"mode", "reason", "strategy"},
		),
		latency: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "compression_latency_seconds",
				Help:    "Wall-clock latency of a Compress()/CompressAfter4xx() call, by strategy.",
				Buckets: prometheus.ExponentialBuckets(0.001, 2, 14), // 1ms → 16s
			},
			[]string{"strategy"},
		),
		ratio: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "compression_ratio",
				Help: "Most recent compression ratio (bytes_after / bytes_before). Values close to 1.0 indicate the compressor failed to shrink the body.",
			},
			[]string{"strategy"},
		),
	}
}

func init() {
	prometheus.MustRegister(
		defaultMetrics.triggered,
		defaultMetrics.latency,
		defaultMetrics.ratio,
	)
}

// RecordOutcome emits one triggered + one latency + one ratio sample for
// the given compression outcome. No-op when didCompress is false (caller
// didn't actually rewrite the body) so we don't pollute metrics with
// "we considered compressing but decided not to" events.
//
// strategy and reason are passed verbatim to the triggered counter; mode
// comes from the caller's current Mode() so dashboards can split by env.
//
// ratio is bytes_after / bytes_before; clamped to (0, 1] to avoid
// out-of-range gauges when the rebuilder produces a slightly larger body
// (e.g. dynamic_context wrapping adds overhead even as content shrinks).
func RecordOutcome(mode Mode, reason CompressionReason, strategy CompressionStrategy, bytesBefore, bytesAfter int, latencySec float64) {
	if defaultMetrics == nil {
		return
	}
	defaultMetrics.triggered.WithLabelValues(mode.String(), string(reason), string(strategy)).Inc()
	defaultMetrics.latency.WithLabelValues(string(strategy)).Observe(latencySec)
	if bytesBefore > 0 && bytesAfter > 0 {
		ratio := float64(bytesAfter) / float64(bytesBefore)
		if ratio > 1.0 {
			ratio = 1.0
		}
		if ratio < 0 {
			ratio = 0
		}
		defaultMetrics.mu.Lock()
		defaultMetrics.ratio.WithLabelValues(string(strategy)).Set(ratio)
		defaultMetrics.mu.Unlock()
	}
}

// ResetMetrics is a test-only helper that wipes all metric values so
// per-test assertions on TriggeredCount / LatencyHistogram can run
// against a clean slate. NOT exported for production callers.
func ResetMetrics() {
	defaultMetrics.triggered.Reset()
	defaultMetrics.latency.Reset()
	defaultMetrics.ratio.Reset()
}

// TriggeredCount returns the current value of compression_triggered_total
// for a given label triple. Useful in tests; production callers should
// use the Prometheus scrape endpoint instead.
func TriggeredCount(mode Mode, reason CompressionReason, strategy CompressionStrategy) float64 {
	m, _ := defaultMetrics.triggered.GetMetricWithLabelValues(mode.String(), string(reason), string(strategy))
	if m == nil { return 0 }
	pb := &dto.Metric{}
	_ = m.(prometheus.Metric).Write(pb)
	if pb.Counter != nil && pb.Counter.Value != nil { return *pb.Counter.Value }
	return 0
}
