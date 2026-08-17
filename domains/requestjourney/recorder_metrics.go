package requestjourney

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	journeyRecorderDroppedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "request_journey_recorder_dropped_total",
		Help: "Total request journey external writes dropped before or during delivery.",
	}, []string{"reason", "store"})
	journeyRecorderDegradedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "request_journey_recorder_degraded_total",
		Help: "Total request journey observations degraded by queue or store failures.",
	}, []string{"reason", "store"})
	journeyRecorderQueueDepth = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "request_journey_recorder_queue_depth",
		Help: "Bounded pending writes per store worker: queue plus outbox backlog (lag).",
	}, []string{"store"})
	journeyRecorderWrittenTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "request_journey_recorder_written_total",
		Help: "Total request journey writes accepted by a store on the first attempt.",
	}, []string{"store"})
	journeyRecorderReplayedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "request_journey_recorder_replayed_total",
		Help: "Total request journey writes that succeeded only after an outbox replay.",
	}, []string{"store"})
	journeyRecorderSeqGapTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "request_journey_recorder_seq_gap_total",
		Help: "Total per-request sequence numbers a store never received (drop or eviction gaps).",
	}, []string{"store"})
)

func recordJourneyDropWithStore(reason, store string) {
	journeyRecorderDroppedTotal.WithLabelValues(reason, store).Inc()
}

func recordJourneyDegradedWithStore(reason, store string) {
	journeyRecorderDegradedTotal.WithLabelValues(reason, store).Inc()
}

func recordJourneyReplay(store string) {
	journeyRecorderReplayedTotal.WithLabelValues(store).Inc()
}

func recordJourneyGap(store string, count int64) {
	journeyRecorderSeqGapTotal.WithLabelValues(store).Add(float64(count))
}

func setJourneyQueueDepth(store string, depth int) {
	journeyRecorderQueueDepth.WithLabelValues(store).Set(float64(depth))
}

var journeyQuerySourceDivergenceTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "request_journey_query_source_divergence_total",
	Help: "Total Detail-query merges where observation sources disagreed, by kind.",
}, []string{"kind"})

func recordJourneySourceDivergence(kind string) {
	journeyQuerySourceDivergenceTotal.WithLabelValues(kind).Inc()
}
