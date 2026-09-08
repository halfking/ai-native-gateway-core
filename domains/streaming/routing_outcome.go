package streaming

// routing_outcome.go — 2026-09-08 24h audit (round 2, Track A): the
// request-completion side of the auto-route feedback loop.
//
// The decider parks its decision-time feedback in the autoroute outcome
// registry (autoroute/outcome_feedback.go) instead of writing a placeholder
// row; the terminal request_logs choke points call ReportRoutingOutcome here
// with the real success/latency/cost. Import direction is the existing
// streaming → autoroute edge (decider wiring), so no cycle is created and no
// cmd/-level indirection is needed.

import (
	"github.com/kaixuan/llm-gateway-go/autoroute"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

// routingOutcomeFromEntry projects a terminal request_logs entry onto the
// outcome report the autoroute registry matches stashed decisions by.
// Latency/cost default to 0 when the entry carries no value — the feedback
// row then simply keeps the zero metrics instead of a wrong number.
func routingOutcomeFromEntry(e *telemetry.RequestLogEntry) autoroute.RoutingOutcome {
	if e == nil {
		return autoroute.RoutingOutcome{}
	}
	out := autoroute.RoutingOutcome{
		RequestID: e.RequestID,
		Success:   e.Success,
	}
	if e.LatencyMs != nil {
		out.LatencyMs = int64(*e.LatencyMs)
	}
	if e.CostUSD != nil {
		out.CostUSD = *e.CostUSD
	}
	return out
}
