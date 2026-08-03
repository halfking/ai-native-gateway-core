package telemetry

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// requestWALEventsTotal is the Prometheus-facing mirror of the seven
// in-process atomic counters on RequestLogger (queueOverflow /
// fallbackWriteFailure / unrecoverableFallback / replayAttempt /
// replaySuccess / replayFailure / replayMarker).
//
// Spec §12 six-dimension audit GAP 1: the WAL counters were observable
// only via the in-process OverflowCounts() Snapshot API (and the
// /debug endpoint that reads it). Without a Prometheus surface the
// fail-open / fallback ratio could not be scraped by the standard
// /metrics pipeline, blocking the IR default-cut-over readiness gate.
//
// Design choice (sync Inc, delta mode): the counter is incremented
// in-place on every recordOverflow / fallbackUpdates / writeOverflowMarker
// / ReplayFallback call — the same pattern as
// domains/ursm/v2/statesource/prometheus.go (RecordRoutingStateSource
// calls routingStateSourceTotal.Inc() inline). This is simpler than a
// background sync goroutine and avoids the 5s scrape lag that a delta
// collector would introduce. The in-process atomic counters remain the
// source of truth for OverflowCounts()/Snapshot; the Prometheus counter
// is the production scrape channel.
//
// Label cardinality is fixed: the seven event values below are the
// complete set, pre-initialised at boot so dashboards never observe a
// missing label before traffic arrives.
var requestWALEventsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "request_wal_events_total",
	Help: "Request WAL lifecycle events labelled by event (queue_overflow/fallback_write_failure/unrecoverable_fallback/replay_attempt/replay_success/replay_failure/replay_marker). Mirrors the in-process RequestLogger atomic counters for /metrics scraping (spec §12 GAP 1).",
},
	[]string{"event"},
)

// walEventLabels is the exhaustive set of event label values. Keep in
// sync with the RequestLogger atomic counter set and the
// recordOverflow/fallback/Replay call sites that increment them.
var walEventLabels = []string{
	"queue_overflow",
	"fallback_write_failure",
	"unrecoverable_fallback",
	"replay_attempt",
	"replay_success",
	"replay_failure",
	"replay_marker",
}

func init() {
	// Pre-initialise every label so the metric surface is stable from
	// boot (dashboards never see a missing label before traffic).
	for _, ev := range walEventLabels {
		requestWALEventsTotal.WithLabelValues(ev).Add(0)
	}
}

// incWALEvent is the single sync-Inc seam used by the RequestLogger
// hot paths. Centralising it here keeps the label string literals in
// one place (typos become compile-time-visible divergences from
// walEventLabels) and makes a future switch to a delta collector a
// one-function change. Nil-safe so test paths that build a bare
// RequestLogger without calling init do not need to guard.
func incWALEvent(event string) {
	requestWALEventsTotal.WithLabelValues(event).Inc()
}
