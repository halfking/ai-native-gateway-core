package requestjourney

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	journeyRecorderDroppedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "request_journey_recorder_dropped_total",
		Help: "Total request journey external writes dropped before enqueue.",
	}, []string{"reason"})
	journeyRecorderDegradedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "request_journey_recorder_degraded_total",
		Help: "Total request journey observations degraded by queue or store failures.",
	}, []string{"reason"})
)

func recordJourneyDrop(reason string) {
	journeyRecorderDroppedTotal.WithLabelValues(reason).Inc()
}

func recordJourneyDegraded(reason string) {
	journeyRecorderDegradedTotal.WithLabelValues(reason).Inc()
}
