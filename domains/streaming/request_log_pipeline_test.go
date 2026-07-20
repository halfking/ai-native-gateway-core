package streaming

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/authentication" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

// TestRequestLogContext_BuildFailureEntry_ClientRequestID asserts that
// the 2026-06-26 client-request-id propagation works end-to-end inside
// the streaming package: a failure entry produced by EmitFailure must
// carry the client-supplied id on telemetry.RequestLogEntry.ClientRequestID
// so request_logs.client_request_id is populated when the row is persisted.
//
// Regression context: the original bug let a client retry 5× with the
// same X-Request-Id and produce 5 rows in request_logs sharing one
// request_id. The fix introduces client_request_id as a separate
// column for the client value; this test guards that propagation.
func TestRequestLogContext_BuildFailureEntry_ClientRequestID(t *testing.T) {
	ch := NewChatHandler(nil, nil, nil, nil, nil, nil)
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"glm-5.1"}`))
	r.Header.Set("X-Request-Id", "client-retry-XYZ")
	r.Header.Set("X-Gw-Client-Request-Id", "client-retry-XYZ")

	ctx := ch.NewRequestLogContext(r, "server-uuid-1", time.Now())
	ctx.ClientRequestID = "client-retry-XYZ" // what the middleware would set
	ctx.Body = []byte(`{"model":"glm-5.1"}`)
	ctx.SetClientModel("glm-5.1")
	ctx.SetKey(&authentication.KeyInfo{ID: 1, TenantID: "default"})

	entry := ctx.BuildFailureEntry("transient", "upstream transient", nil, nil)
	if entry == nil {
		t.Fatal("nil entry")
	}
	if entry.RequestID != "server-uuid-1" {
		t.Fatalf("RequestID=%q, want server-uuid-1", entry.RequestID)
	}
	if entry.ClientRequestID == nil || *entry.ClientRequestID != "client-retry-XYZ" {
		t.Fatalf("ClientRequestID=%v, want client-retry-XYZ", entry.ClientRequestID)
	}
}

// TestRequestLogContext_BuildFailureEntry_EmptyClientRequestID covers
// the no-client-header case: ClientRequestID must be a nil pointer (NOT
// &"") so the SQL COALESCE writes NULL rather than an empty string,
// keeping the partial index clean.
func TestRequestLogContext_BuildFailureEntry_EmptyClientRequestID(t *testing.T) {
	ch := NewChatHandler(nil, nil, nil, nil, nil, nil)
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"glm-5.1"}`))
	// No X-Gw-Client-Request-Id set on the request.

	ctx := ch.NewRequestLogContext(r, "server-uuid-2", time.Now())
	ctx.Body = []byte(`{"model":"glm-5.1"}`)
	ctx.SetClientModel("glm-5.1")
	ctx.SetKey(&authentication.KeyInfo{ID: 1, TenantID: "default"})

	entry := ctx.BuildFailureEntry("transient", "upstream transient", nil, nil)
	if entry == nil {
		t.Fatal("nil entry")
	}
	if entry.ClientRequestID != nil {
		t.Fatalf("ClientRequestID must be nil when no client header was sent, got %v", *entry.ClientRequestID)
	}
}

func TestRequestLogContext_BuildFailureEntry_EventAtUsesStartPlusLatency(t *testing.T) {
	before := time.Now().UTC()
	ctx := &RequestLogContext{
		RequestID: "req-1",
		StartTime: before.Add(-250 * time.Millisecond),
	}
	entry := ctx.BuildFailureEntry("transient", "upstream transient", nil, nil)
	if entry == nil || entry.EventAt == nil {
		t.Fatal("expected EventAt on failure entry")
	}
	after := time.Now().UTC()
	if entry.EventAt.Before(before) || entry.EventAt.After(after.Add(100*time.Millisecond)) {
		t.Fatalf("EventAt=%s want between %s and %s", entry.EventAt.Format(time.RFC3339Nano), before.Format(time.RFC3339Nano), after.Format(time.RFC3339Nano))
	}
}

// TestRequestLogContext_RateLimitedStatus asserts the rate-limit vs failure
// status split: gateway RPM/throttle rejections must record
// request_status="rate_limited" (not "failure") so dashboards can exclude
// them from provider error counts while still keeping them in the success-rate
// denominator. Success stays false in both cases — the request did not
// complete — but the status category is what separates "client was rate
// limited" from "system error".
func TestRequestLogContext_RateLimitedStatus(t *testing.T) {
	ch := NewChatHandler(nil, nil, nil, nil, nil, nil)
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"minimax-m3"}`))

	ctx := ch.NewRequestLogContext(r, "server-uuid-rl", time.Now())
	ctx.Body = []byte(`{"model":"minimax-m3"}`)
	ctx.SetClientModel("minimax-m3")
	ctx.SetKey(&authentication.KeyInfo{ID: 1, TenantID: "default"})

	// A genuine failure entry stays "failure".
	failEntry := ctx.BuildFailureEntry("transient", "upstream transient", nil, nil)
	if failEntry == nil || failEntry.RequestStatus == nil {
		t.Fatal("nil failure entry / status")
	}
	if *failEntry.RequestStatus != "failure" {
		t.Fatalf("BuildFailureEntry status=%q, want failure", *failEntry.RequestStatus)
	}
	if failEntry.Success {
		t.Fatal("failure entry must have Success=false")
	}

	// A rate-limited entry uses the dedicated status.
	rlEntry := ctx.buildEntry("rate_limit_exceeded", "rate limit exceeded", nil, nil, "rate_limited")
	if rlEntry == nil || rlEntry.RequestStatus == nil {
		t.Fatal("nil rate-limited entry / status")
	}
	if *rlEntry.RequestStatus != "rate_limited" {
		t.Fatalf("rate-limited status=%q, want rate_limited", *rlEntry.RequestStatus)
	}
	if rlEntry.Success {
		t.Fatal("rate-limited entry must have Success=false (request did not complete)")
	}
	// ErrorKind is preserved so the specific cause (rpm vs throttle) is queryable.
	if rlEntry.ErrorKind == nil || *rlEntry.ErrorKind != "rate_limit_exceeded" {
		t.Fatalf("ErrorKind=%v, want rate_limit_exceeded", rlEntry.ErrorKind)
	}
}
