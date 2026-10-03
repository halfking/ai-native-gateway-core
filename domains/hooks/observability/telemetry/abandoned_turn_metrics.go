package telemetry

// abandoned_turn_metrics.go — observability for the migration-820 landing pad
// ("request started but never reached a terminal record", audit §9.92).
//
// # Why a CounterVec and not a gauge of the row count
//
// The obvious implementation is a gauge refreshed by a poller. For this pad a
// poller would be wrong in a specific, damaging way: the pad is written from
// `updateRequestLog`'s upsert-race branch, and that branch is **racy against
// the async outbox** that writes the terminal turn (see abandoned_turn.go —
// markAbandonedTurn documents the window). A poller that counted
// `WHERE is_abandoned IS TRUE` would therefore report a *lagging* number
// that is indistinguishable from "nothing was marked yet".
//
// The counter pair below measures the thing that actually matters:
//
//	"mark"   — a turn was flagged abandoned
//	"mark_failed" — the flag write failed (fail-open; request logging unaffected)
//	"mark_no_row" — the flag write matched **no** row
//
// # `mark_no_row` is the one that earns its own label
//
// It is **not** an error: the outbox writes the terminal turn asynchronously,
// so the UPDATE frequently runs before the row exists. The common case is a
// healthy system.
//
// It is nevertheless the only signal that the race window is losing rows. If
// `mark_no_row` is a large fraction of requests, the pad is systematically
// under-marking and the landing pad is **less trustworthy than it looks** —
// which is worse than not having one, because the column reads as authoritative.
// Treat a rising `mark_no_row` share as a correctness problem, not a
// performance one. See §9.92.4, which registers this as a known gap rather
// than closing it.
//
// # Known limitation (registered, not papered over)
//
// These are process-memory counters, so they reset on restart — the same
// property §9.65.8 flags for `llm_gateway_shadow_write_failed_total`. The
// **authoritative** count is the database column:
//
//	SELECT count(*) FROM public.session_turns_hot WHERE is_abandoned IS TRUE
//
// The counters exist to answer "is the pad wired at all, and is it failing?",
// not "how many have we ever lost?" — a question no process-memory counter can
// answer across restarts, and which is why no cumulative counter exists here.

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// abandonedTurnOps counts operations on the migration-820 landing pad.
//
//	op = "mark"         — a turn was flagged is_abandoned = TRUE
//	op = "mark_failed"  — the flag write failed (fail-open, request logging unaffected)
//	op = "mark_no_row"  — the flag write matched no row (normally the async
//	                      outbox has not written the terminal turn yet)
var abandonedTurnOps = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "llm_gateway_abandoned_turn_ops_total",
		Help: "Ops on the session_turns abandoned landing pad (migration 820). " +
			"A high mark_no_row share means the async outbox race is losing rows and the " +
			"pad is under-marking (rate(mark_no_row) / rate(all ops)). Process-memory: " +
			"resets on restart; the authoritative count is the is_abandoned column.",
	},
	[]string{"op"},
)

func recordAbandonedTurnOp(op string) {
	abandonedTurnOps.WithLabelValues(op).Inc()
}
