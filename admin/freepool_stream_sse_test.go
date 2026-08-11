package admin

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/freeresource"
)

// TestFreePoolSSEHub_ShouldSkipSelfNotification verifies the fix for the
// double-delivery bug: Publish() fans out locally AND republishes to Redis;
// the Redis subscriber must drop notifications that carry its own instanceID
// so same-instance clients don't see each event twice.
//
// Before the fix, FreePoolSSEHub had no instanceID tag — the subscriber
// re-fanned-out every self-originated event, delivering it twice to local
// clients. This mirrors the ProbeSSEHub dedup contract.
func TestFreePoolSSEHub_ShouldSkipSelfNotification(t *testing.T) {
	hub := NewFreePoolSSEHub(nil) // in-memory mode; instanceID still generated
	if hub.instanceID == "" {
		t.Fatal("instanceID must be generated so self-notifications can be skipped")
	}

	// A payload tagged with this hub's own instanceID must be skipped.
	self := struct {
		FreePoolEnvelope
		InstanceID string `json:"instance_id"`
	}{
		FreePoolEnvelope{Type: "quota_exhausted", CredentialID: 1, Ts: time.Now().UTC()},
		hub.instanceID,
	}
	selfPayload, _ := json.Marshal(self)
	if !hub.shouldSkipNotification(string(selfPayload)) {
		t.Fatal("self-originated notification must be skipped to avoid double delivery")
	}

	// A payload from a DIFFERENT instance must NOT be skipped — cross-instance
	// fan-out is the whole point of Redis pub/sub.
	other := struct {
		FreePoolEnvelope
		InstanceID string `json:"instance_id"`
	}{
		FreePoolEnvelope{Type: "quota_exhausted", CredentialID: 2, Ts: time.Now().UTC()},
		"f-otherinstance",
	}
	otherPayload, _ := json.Marshal(other)
	if hub.shouldSkipNotification(string(otherPayload)) {
		t.Fatal("cross-instance notification must NOT be skipped")
	}

	// A malformed payload must not panic / not be skipped (fail-open).
	if hub.shouldSkipNotification("not-json") {
		t.Fatal("malformed payload should not be treated as self-originated")
	}
}

// TestFreePoolSSEHub_LocalFanOutDeliversOnce verifies that a Publish in
// in-memory mode (no Redis) delivers exactly one event to a subscribed client
// channel — the regression target for the double-delivery bug.
func TestFreePoolSSEHub_LocalFanOutDeliversOnce(t *testing.T) {
	hub := NewFreePoolSSEHub(nil)
	clientCh := make(chan FreePoolEnvelope, 8)
	hub.mu.Lock()
	hub.clients[clientCh] = struct{}{}
	hub.mu.Unlock()
	defer hub.removeClient(clientCh)

	hub.PublishQuotaEvent(freeresource.QuotaEvent{
		Type:         "quota_exhausted",
		CredentialID: 42,
		ProviderCode: "openrouter-free",
		Ts:           time.Now().UTC(),
	})

	// Exactly one delivery — a second would indicate the Redis-round-trip
	// double-delivery bug (or a missing dedup gate).
	select {
	case env := <-clientCh:
		if env.CredentialID != 42 || env.Type != "quota_exhausted" {
			t.Fatalf("unexpected envelope: %+v", env)
		}
	case <-time.After(time.Second):
		t.Fatal("expected one local delivery, got none")
	}
	select {
	case extra := <-clientCh:
		t.Fatalf("double-delivery bug: got a second event %+v", extra)
	default:
		// good — exactly one
	}
}
