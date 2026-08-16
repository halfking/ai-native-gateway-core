package shadow

import (
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type Outcome string

const (
	OutcomeIdentical            Outcome = "identical"
	OutcomeAvailabilityMismatch Outcome = "availability"
	OutcomeOrderMismatch        Outcome = "order"
	OutcomeTop1Mismatch         Outcome = "top1"
	OutcomeNotReady             Outcome = "not_ready"
	OutcomeError                Outcome = "error"
	OutcomeSampledOut           Outcome = "sampled_out"
	OutcomeDropped              Outcome = "dropped"
)

var allOutcomes = [...]Outcome{
	OutcomeIdentical,
	OutcomeAvailabilityMismatch,
	OutcomeOrderMismatch,
	OutcomeTop1Mismatch,
	OutcomeNotReady,
	OutcomeError,
	OutcomeSampledOut,
	OutcomeDropped,
}

var (
	shadowDiffTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ursm_shadow_diff_total",
		Help: "URSM v2 observe-only shadow comparisons classified by outcome.",
	}, []string{"type"})
	outcomeCounts [len(allOutcomes)]atomic.Uint64
)

func init() {
	for _, outcome := range allOutcomes {
		shadowDiffTotal.WithLabelValues(string(outcome)).Add(0)
	}
}

func Record(outcome Outcome) {
	for i, known := range allOutcomes {
		if outcome == known {
			outcomeCounts[i].Add(1)
			shadowDiffTotal.WithLabelValues(string(outcome)).Inc()
			return
		}
	}
}

// Snapshot exposes monotonic in-process counts for tests and diagnostics. The
// durable rollout record is the Prometheus time series scraped from /metrics.
func Snapshot() map[Outcome]uint64 {
	out := make(map[Outcome]uint64, len(allOutcomes))
	for i, outcome := range allOutcomes {
		out[outcome] = outcomeCounts[i].Load()
	}
	return out
}
