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

var (
	journeyObservationOutboxEnqueueFailureTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "request_journey_observation_outbox_enqueue_failure_total",
		Help: "Total durable RequestJourney observations that could not be enqueued.",
	})
	journeyObservationOutboxClaimTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "request_journey_observation_outbox_claim_total",
		Help: "Total durable RequestJourney observations claimed for delivery.",
	})
	journeyObservationOutboxAckTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "request_journey_observation_outbox_ack_total",
		Help: "Total durable RequestJourney observations acknowledged after projection.",
	})
	journeyObservationOutboxRetryTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "request_journey_observation_outbox_retry_total",
		Help: "Total durable RequestJourney observations released for retry.",
	}, []string{"stage"})
)

func recordObservationOutboxEnqueueFailure() {
	journeyObservationOutboxEnqueueFailureTotal.Inc()
}

func recordObservationOutboxClaim() {
	journeyObservationOutboxClaimTotal.Inc()
}

func recordObservationOutboxAck() {
	journeyObservationOutboxAckTotal.Inc()
}

func recordObservationOutboxRetry(stage string) {
	journeyObservationOutboxRetryTotal.WithLabelValues(stage).Inc()
}

var journeyQuerySourceDivergenceTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "request_journey_query_source_divergence_total",
	Help: "Total Detail-query merges where observation sources disagreed, by kind.",
}, []string{"kind"})

func recordJourneySourceDivergence(kind string) {
	journeyQuerySourceDivergenceTotal.WithLabelValues(kind).Inc()
}
