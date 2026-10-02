package telemetry

// request_abandoned_metrics.go — observability for the 819 「开始了却没结束」
// landing pad (audit §9.66 / §9.67).
//
// # Why a counter pair rather than a gauge of the table depth
//
// The obvious implementation is a gauge refreshed by a poller — that is what
// `session_v2_mirror_outbox_pending` does (internal/sessionv2mirror/replay.go
// refreshGauge). For this table a poller is unnecessary, and the pair below
// measures the thing that actually matters *more* directly:
//
//	mark fires once per request (the in_progress INSERT)
//	clear fires once per request (the terminal UPDATE)
//	⇒ rate(mark) − rate(clear) == the abandonment rate
//
// That difference is the operational signal in §9.66's terms, and it is
// available the instant a request is mis-handled instead of on the next poll.
//
// # Why this file declares no gauge of `count(*)`
//
// Table depth is a *lagging* symptom: a broken DELETE half shows up in it
// only after the table has already absorbed a full traffic day's worth of
// rows. The counter difference shows up within one `for:` window. The depth
// is still worth reading in a dashboard (one `SELECT count(*)`), but it must
// not be the thing that pages someone.
//
// # Known limitation (registered, not papered over)
//
// These are process-memory counters, so they reset on restart — the same
// property §9.65.8 flags for `llm_gateway_shadow_write_failed_total`.
// That is acceptable here because the **rate difference** is a short-window
// quantity: a restart zeroes the absolute counts, not the ongoing leak. It
// would NOT be acceptable for a cumulative "how many have we ever lost"
// counter, which is why no such counter exists.

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// requestAbandonedMarkerOps counts the two halves of the 819 invariant.
//
//	op = "mark"          — a request registered as started (t0, in_progress)
//	op = "clear"         — a terminal record removed that marker
//	op = "mark_failed"   — the mark write failed (fail-open; request logging unaffected)
//	op = "clear_failed"  — the clear write failed (row stays ⇒ reads as abandoned)
var requestAbandonedMarkerOps = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "llm_gateway_request_abandoned_marker_ops_total",
		Help: "Ops on the request_abandoned marker (migration 819). rate(mark) - rate(clear) is the " +
			"abandonment rate, i.e. requests that started and never reached a terminal record. " +
			"Process-memory: resets on restart (acceptable for a short-window rate, not for a lifetime count).",
	},
	[]string{"op"},
)

func recordRequestAbandonedOp(op string) {
	requestAbandonedMarkerOps.WithLabelValues(op).Inc()
}
