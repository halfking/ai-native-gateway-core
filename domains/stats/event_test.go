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

func TestEventFromTelemetryTerminalIDDoesNotDependOnOutcome(t *testing.T) {
	failure := telemetry.RequestStatusFailure
	success := telemetry.RequestStatusSuccess
	failed, ok := EventFromTelemetry(&telemetry.RequestLogEntry{Op: telemetry.RequestLogUpdate, RequestID: "req-terminal", TenantID: "t", RequestStatus: &failure}, time.Now())
	if !ok {
		t.Fatal("expected failure event")
	}
	completed, ok := EventFromTelemetry(&telemetry.RequestLogEntry{Op: telemetry.RequestLogUpdate, RequestID: "req-terminal", TenantID: "t", RequestStatus: &success}, time.Now())
	if !ok {
		t.Fatal("expected success event")
	}
	if failed.EventID != completed.EventID {
		t.Fatalf("terminal id changed with outcome: %q vs %q", failed.EventID, completed.EventID)
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

func TestPersonHash(t *testing.T) {
	t.Run("cross-tenant same end_user produces different hashes", func(t *testing.T) {
		a := personHash("tenant_a", "owner", "alice")
		b := personHash("tenant_b", "owner", "alice")
		if a == "" || b == "" {
			t.Fatalf("expected non-empty hashes, got a=%q b=%q", a, b)
		}
		if a == b {
			t.Fatalf("cross-tenant same end_user produced identical hash %q", a)
		}
		if len(a) != 16 || len(b) != 16 {
			t.Fatalf("hash length must be 16 hex chars (8 bytes): a=%d b=%d", len(a), len(b))
		}
	})
	t.Run("same tenant same end_user is deterministic", func(t *testing.T) {
		first := personHash("tenant_a", "owner", "alice")
		second := personHash("tenant_a", "owner", "alice")
		if first == "" {
			t.Fatalf("expected non-empty hash")
		}
		if first != second {
			t.Fatalf("hash not deterministic: %q vs %q", first, second)
		}
	})
	t.Run("fallback path uses owner value but still tenant-prefixed", func(t *testing.T) {
		viaOwner := personHash("tenant_a", "owner", "")
		explicitOwner := personHash("tenant_a", "owner", "owner")
		if viaOwner == "" || explicitOwner == "" {
			t.Fatalf("expected non-empty hashes, got viaOwner=%q explicitOwner=%q", viaOwner, explicitOwner)
		}
		if viaOwner != explicitOwner {
			t.Fatalf("empty endUser should fall back to owner with same tenant: %q vs %q", viaOwner, explicitOwner)
		}
		crossTenant := personHash("tenant_b", "owner", "owner")
		if viaOwner == crossTenant {
			t.Fatalf("fallback path still must be tenant-prefixed: %q", viaOwner)
		}
	})
	t.Run("length-prefix framing distinguishes colon-containing inputs", func(t *testing.T) {
		left := personHash("a", "", "b:c")
		right := personHash("a:b", "", "c")
		if left == right {
			t.Fatalf("length-prefix framing must distinguish ambiguous delimiter inputs: %q", left)
		}
	})
	t.Run("tenant whitespace is normalized", func(t *testing.T) {
		trimmed := personHash("tenant_a", "owner", "alice")
		spaced := personHash(" tenant_a ", "owner", "alice")
		if trimmed != spaced {
			t.Fatalf("tenant whitespace must not change person hash: %q vs %q", trimmed, spaced)
		}
	})
	t.Run("empty tenant returns empty string", func(t *testing.T) {
		if got := personHash("", "owner", "alice"); got != "" {
			t.Fatalf("empty tenant must short-circuit, got %q", got)
		}
		if got := personHash("   ", "owner", "alice"); got != "" {
			t.Fatalf("whitespace tenant must short-circuit, got %q", got)
		}
	})
	t.Run("empty everything returns empty string", func(t *testing.T) {
		if got := personHash("", "", ""); got != "" {
			t.Fatalf("all-empty must short-circuit, got %q", got)
		}
	})
}
