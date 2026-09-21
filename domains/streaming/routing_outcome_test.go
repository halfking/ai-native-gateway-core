package streaming

// routing_outcome_test.go — 2026-09-08 audit (round 2, Track A): the
// completion-path projection of terminal request_logs entries onto the
// autoroute outcome report must carry ids and settled metrics verbatim, and
// tolerate absent (nil) metric pointers.

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/autoroute"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

func TestRoutingOutcomeFromEntry(t *testing.T) {
	latency := 4321
	cost := 0.0314
	e := &telemetry.RequestLogEntry{
		RequestID: "req-e2e-1",
		Success:   true,
		LatencyMs: &latency,
		CostUSD:   &cost,
	}
	out := routingOutcomeFromEntry(e)
	if out.RequestID != "req-e2e-1" || !out.Success {
		t.Fatalf("identity/outcome must be carried verbatim, got %+v", out)
	}
	if out.LatencyMs != 4321 || out.CostUSD != 0.0314 {
		t.Fatalf("settled metrics must be carried verbatim, got %+v", out)
	}
}

func TestRoutingOutcomeFromEntry_NilMetricsAndNilEntry(t *testing.T) {
	out := routingOutcomeFromEntry(&telemetry.RequestLogEntry{RequestID: "req-e2e-2", Success: false})
	if out.RequestID != "req-e2e-2" || out.Success {
		t.Fatalf("identity/outcome must be carried verbatim, got %+v", out)
	}
	if out.LatencyMs != 0 || out.CostUSD != 0 {
		t.Fatalf("nil metrics must default to zero, got %+v", out)
	}
	if got := routingOutcomeFromEntry(nil); got != (autoroute.RoutingOutcome{}) {
		t.Fatalf("nil entry must yield the zero outcome, got %+v", got)
	}
}
