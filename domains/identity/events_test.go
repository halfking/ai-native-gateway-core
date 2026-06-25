package identity

import (
	"testing"
	"time"
)

func TestClientIdentifiedEvent_Type(t *testing.T) {
	e := &ClientIdentifiedEvent{}
	if got := e.Type(); got != "client.identified" {
		t.Fatalf("Type() = %q, want client.identified", got)
	}
}

func TestClientIdentifiedEvent_Timestamp(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	e := &ClientIdentifiedEvent{OccurredAt: now}
	if got := e.Timestamp(); !got.Equal(now) {
		t.Fatalf("Timestamp() = %v, want %v", got, now)
	}
}

func TestClientIdentifiedEvent_Fields(t *testing.T) {
	e := &ClientIdentifiedEvent{
		IdentityHash: "abc",
		VirtualIP:    "10.1.2.3",
		VirtualMAC:   "02:ab:cd:ef:00:00",
		TenantID:     "tenant-1",
	}
	if e.IdentityHash != "abc" || e.VirtualIP != "10.1.2.3" ||
		e.VirtualMAC != "02:ab:cd:ef:00:00" || e.TenantID != "tenant-1" {
		t.Fatal("field assignment failed")
	}
}

func TestAuthenticatedEvent_Type(t *testing.T) {
	e := &AuthenticatedEvent{}
	if got := e.Type(); got != "client.authenticated" {
		t.Fatalf("Type() = %q, want client.authenticated", got)
	}
}

func TestAuthenticatedEvent_Timestamp(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	e := &AuthenticatedEvent{OccurredAt: now}
	if got := e.Timestamp(); !got.Equal(now) {
		t.Fatalf("Timestamp() = %v, want %v", got, now)
	}
}

func TestAuthenticatedEvent_Fields(t *testing.T) {
	e := &AuthenticatedEvent{
		IdentityHash: "hash",
		APIKeyID:     "key-42",
		TenantID:     "tenant-z",
	}
	if e.APIKeyID != "key-42" {
		t.Fatalf("APIKeyID field assignment failed: %q", e.APIKeyID)
	}
}
