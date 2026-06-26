package streaming

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
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
