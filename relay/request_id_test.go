package relay

import "testing"

func TestNewAuditEvent_UsesExplicitRequestID(t *testing.T) {
	event := newAuditEvent("req-explicit-123").Build()
	if event.RequestID != "req-explicit-123" {
		t.Fatalf("expected explicit request id, got %q", event.RequestID)
	}
}

func TestNewAuditEvent_GeneratesRequestIDWhenMissing(t *testing.T) {
	event := newAuditEvent("").Build()
	if event.RequestID == "" {
		t.Fatal("expected generated request id")
	}
}