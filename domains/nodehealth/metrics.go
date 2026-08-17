package nodehealth

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Eviction reasons for the reducer's bounded deduplication history.
const (
	evictionReasonTTL      = "ttl"
	evictionReasonCapacity = "capacity"
)

var seenEvictedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "nodehealth_reducer_seen_evicted_total",
	Help: "Deduplication entries removed from the outcome reducer's bounded seen history, by reclaim reason (ttl or capacity).",
}, []string{"reason"})

func recordSeenEviction(reason string) {
	seenEvictedTotal.WithLabelValues(reason).Inc()
}
