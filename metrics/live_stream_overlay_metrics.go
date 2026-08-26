package metrics

import (
	"strings"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

var (
	liveStreamOverlayMetricsOnce sync.Once
	liveStreamOverlayOutcomes    *prometheus.CounterVec
)

var liveStreamOverlayOutcomeLabels = []string{"success", "failure", "locked", "unknown"}

// RecordLiveStreamTileOverlayDBLookup records a terminal status copied from
// request logs into a stale live-stream tile. Tenant IDs are intentionally not
// labels: they would leak tenancy and create unbounded metric cardinality.
func RecordLiveStreamTileOverlayDBLookup(status string) {
	registerLiveStreamOverlayMetrics()
	liveStreamOverlayOutcomes.WithLabelValues(normalizeLiveStreamOverlayOutcome(status)).Inc()
}

func LiveStreamTileOverlayDBLookupVec(outcome string) interface{ Write(*dto.Metric) error } {
	registerLiveStreamOverlayMetrics()
	return liveStreamOverlayOutcomes.WithLabelValues(normalizeLiveStreamOverlayOutcome(outcome))
}

func registerLiveStreamOverlayMetrics() {
	liveStreamOverlayMetricsOnce.Do(func() {
		liveStreamOverlayOutcomes = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "llmgw_live_stream_tile_overlay_db_lookup_total",
			Help: "Live-stream stale tiles corrected from database terminal status by outcome.",
		}, []string{"outcome"})
		prometheus.MustRegister(liveStreamOverlayOutcomes)
	})
	for _, outcome := range liveStreamOverlayOutcomeLabels {
		liveStreamOverlayOutcomes.WithLabelValues(outcome).Add(0)
	}
}

func normalizeLiveStreamOverlayOutcome(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "success":
		return "success"
	case "failure", "rate_limited":
		return "failure"
	case "locked":
		return "locked"
	default:
		return "unknown"
	}
}

func init() { registerLiveStreamOverlayMetrics() }
