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

	// shadowEnqueueTotal (2026-08-31, P2-3 observability): counts the result
	// of every shadow enqueue attempt. Distinguishes the previously-silent
	// "queue full" drop from the "sampled out" / "worker stopped" paths so
	// operators can alert specifically on production-load back-pressure.
	//
	//   - result="enqueued"     : task accepted into the worker queue
	//   - result="sampled_out"  : shadow sampler declined to observe this request
	//   - result="queue_full"   : queue at capacity (128); request lost silently
	//                             before this counter existed
	//   - result="worker_stopped": the shadow worker is shut down (StopShadowWorker)
	shadowEnqueueTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ursm_shadow_enqueued_total",
		Help: "URSM v2 shadow enqueue attempts classified by result. result=queue_full indicates queue-capacity drops that were previously silent.",
	}, []string{"result"})
)

const (
	enqueueResultEnqueued     = "enqueued"
	enqueueResultSampledOut   = "sampled_out"
	enqueueResultQueueFull    = "queue_full"
	enqueueResultWorkerStopped = "worker_stopped"
)

func init() {
	for _, outcome := range allOutcomes {
		shadowDiffTotal.WithLabelValues(string(outcome)).Add(0)
	}
	for _, result := range []string{enqueueResultEnqueued, enqueueResultSampledOut, enqueueResultQueueFull, enqueueResultWorkerStopped} {
		shadowEnqueueTotal.WithLabelValues(result).Add(0)
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

// RecordEnqueueResult (2026-08-31, P2-3 observability): reports the result
// of a single shadow enqueue attempt. Allowed results are the four
// pre-initialised label values above; any other string is rejected to keep
// Prometheus cardinality bounded.
func RecordEnqueueResult(result string) {
	switch result {
	case enqueueResultEnqueued, enqueueResultSampledOut, enqueueResultQueueFull, enqueueResultWorkerStopped:
		shadowEnqueueTotal.WithLabelValues(result).Inc()
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
