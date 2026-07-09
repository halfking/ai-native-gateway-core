package streaming

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

// TestBuildClientDisconnectProbeEntry_Cancel covers 问题4: when the client
// cancels (context.Canceled) the handler must synthesize a "probe-" prefixed
// RequestLogEntry carrying the selected credential_id, so it lands in
// request_logs_hot and the live-stream swim lane under the right provider.
func TestBuildClientDisconnectProbeEntry_Cancel(t *testing.T) {
	credID := 11
	provID := 18
	logCtx := &RequestLogContext{
		ClientModel:  "glm-5.2",
		OutboundModel: "glm-5.2",
		CredentialID: &credID,
		ProviderID:   &provID,
		KeyInfo:      &authentication.KeyInfo{TenantID: "tenant-1"},
	}

	// Build a request whose context is already canceled (client went away).
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
	ctx, cancel := context.WithCancel(r.Context())
	r = r.WithContext(ctx)
	cancel()

	entry, ok := buildClientDisconnectProbeEntry("req-abc", r, logCtx)
	if !ok {
		t.Fatal("expected probe entry to be built on a canceled context")
	}
	if !strings.HasPrefix(entry.RequestID, "probe-client_cancel-cred11-") {
		t.Errorf("RequestID=%q must be probe-client_cancel-cred11-<ts>", entry.RequestID)
	}
	if entry.ErrorKind == nil || *entry.ErrorKind != "client_cancel" {
		t.Errorf("ErrorKind must be client_cancel, got %v", entry.ErrorKind)
	}
	if entry.FailureStage == nil || *entry.FailureStage != "probe" {
		t.Errorf("FailureStage must be probe, got %v", entry.FailureStage)
	}
	if entry.RequestStatus == nil || *entry.RequestStatus != telemetry.RequestStatusFailure {
		t.Errorf("RequestStatus must be failure, got %v", entry.RequestStatus)
	}
	if entry.CredentialID == nil || *entry.CredentialID != credID {
		t.Errorf("CredentialID must be carried for provider reverse-lookup, got %v", entry.CredentialID)
	}
	if entry.TenantID != "tenant-1" {
		t.Errorf("TenantID must propagate, got %q", entry.TenantID)
	}
	if entry.ClientRequestID == nil || *entry.ClientRequestID != "req-abc" {
		t.Errorf("ClientRequestID must link back to original request_id, got %v", entry.ClientRequestID)
	}
}

// TestBuildClientDisconnectProbeEntry_Timeout verifies the deadline-exceeded
// path produces error_kind="probe_timeout".
func TestBuildClientDisconnectProbeEntry_Timeout(t *testing.T) {
	logCtx := &RequestLogContext{ClientModel: "glm-5.2"}

	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
	ctx, cancel := context.WithTimeout(r.Context(), 0) // already expired
	r = r.WithContext(ctx)
	defer cancel()
	// Force the deadline-exceeded error to surface.
	<-ctx.Done()

	entry, ok := buildClientDisconnectProbeEntry("req-xyz", r, logCtx)
	if !ok {
		t.Fatal("expected probe entry to be built on a timed-out context")
	}
	if entry.ErrorKind == nil || *entry.ErrorKind != "probe_timeout" {
		t.Errorf("ErrorKind must be probe_timeout for DeadlineExceeded, got %v", entry.ErrorKind)
	}
	if !strings.HasPrefix(entry.RequestID, "probe-probe_timeout-nocred-") {
		t.Errorf("RequestID=%q must reflect no-credential + timeout", entry.RequestID)
	}
	// credential_id nil when none selected — still records the client-side event.
	if entry.CredentialID != nil {
		t.Errorf("CredentialID should be nil when none selected, got %d", *entry.CredentialID)
	}
}

// TestBuildClientDisconnectProbeEntry_NoErrorReturnsFalse: when the context
// is still alive there is nothing to probe.
func TestBuildClientDisconnectProbeEntry_NoErrorReturnsFalse(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
	if _, ok := buildClientDisconnectProbeEntry("req-1", r, &RequestLogContext{}); ok {
		t.Fatal("must not build a probe entry when context has no error")
	}
}

// TestBuildClientDisconnectProbeEntry_NilRequest: defensive — a nil request
// must not panic.
func TestBuildClientDisconnectProbeEntry_NilRequest(t *testing.T) {
	if _, ok := buildClientDisconnectProbeEntry("req-1", nil, &RequestLogContext{}); ok {
		t.Fatal("must not build a probe entry for a nil request")
	}
}

// Ensure errors.Is is wired (guards against future import removals).
var _ = errors.Is
