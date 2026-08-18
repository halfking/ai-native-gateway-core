package stats

import (
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

func TestEventFromTelemetrySkipsNonTerminalRows(t *testing.T) {
	status := telemetry.RequestStatusInProgress
	if _, ok := EventFromTelemetry(&telemetry.RequestLogEntry{
		RequestID: "req-1", Op: telemetry.RequestLogUpdate, RequestStatus: &status,
	}, time.Now()); ok {
		t.Fatal("in-progress update must not create a terminal statistics event")
	}
}

func TestEventFromTelemetryIsBodyFreeAndIdempotent(t *testing.T) {
	status := telemetry.RequestStatusSuccess
	tenant := "tenant-a"
	model := "gpt-test"
	owner := "owner@example.com"
	prompt, completion, cache := 10, 5, 2
	cost := 0.12
	entry := &telemetry.RequestLogEntry{
		Op: telemetry.RequestLogUpdate, RequestID: "req-42", TenantID: tenant,
		RequestStatus: &status, OutboundModel: &model, APIKeyOwnerUser: &owner,
		PromptTokens: &prompt, CompletionTokens: &completion, CacheReadTokens: &cache,
		CostUSD: &cost, RequestBody: strptr("must not be persisted"),
	}
	now := time.Date(2026, 8, 18, 10, 20, 30, 0, time.UTC)
	first, ok := EventFromTelemetry(entry, now)
	if !ok {
		t.Fatal("expected terminal event")
	}
	second, ok := EventFromTelemetry(entry, now)
	if !ok || first.EventID != second.EventID {
		t.Fatalf("event id is not stable: %q vs %q", first.EventID, second.EventID)
	}
	if first.TotalTokens != 17 || first.Traffic != TrafficBusiness {
		t.Fatalf("unexpected event totals/class: %+v", first)
	}
	if first.PersonHash == "" || first.PersonHash == owner || strings.Contains(first.PersonHash, "@") {
		t.Fatalf("person identity was not reduced to a hash: %q", first.PersonHash)
	}
	if first.RequestID != entry.RequestID || first.EventType != EventRequestSucceeded {
		t.Fatalf("unexpected identity/event type: %+v", first)
	}
}

func TestEventFromTelemetryClassifiesProbeAndRateLimit(t *testing.T) {
	status := telemetry.RequestStatusRateLimited
	origin := "node_probe"
	kind := "429 rate limit"
	e, ok := EventFromTelemetry(&telemetry.RequestLogEntry{
		Op: telemetry.RequestLogUpdate, RequestID: "probe-1", TenantID: "default",
		RequestStatus: &status, OriginStage: &origin, ErrorKind: &kind,
	}, time.Now())
	if !ok {
		t.Fatal("expected probe event")
	}
	if e.Traffic != TrafficProbe || e.EventType != EventRequestRateLimit || e.ErrorClass != "rate_limit" {
		t.Fatalf("unexpected probe classification: %+v", e)
	}
}

func strptr(v string) *string { return &v }
