package v2

import (
	"testing"
	"time"
)

// TestEncodeDecodeSessionUpdate_RoundTrip pins the JSON shape used by the
// outbox reaper. If a future change adds a SessionUpdate field that must
// survive a reaper replay, this test must be updated at the same time the
// field is added to EncodeSessionUpdateForOutbox.
func TestEncodeDecodeSessionUpdate_RoundTrip(t *testing.T) {
	in := SessionUpdate{
		SessionID:           "sess_123",
		TenantID:            "tenant_a",
		RequestID:           "req_456",
		LastTurnNo:          7,
		LastRequestSummary:  "hi",
		LastResponseSummary: "hello",
		LastModel:           "gpt-4o",
		LastProvider:        "openai",
		TurnIncrement:       1,
		TokensIncrement:     123,
		CostIncrement:       0.0017,
		UpdatedAt:           time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC),
	}
	raw, err := EncodeSessionUpdateForOutbox(in)
	if err != nil {
		t.Fatalf("encode failed: %v", err)
	}
	var out SessionUpdate
	if err := decodeUpdatePayload(raw, &out); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if out.SessionID != in.SessionID ||
		out.TenantID != in.TenantID ||
		out.RequestID != in.RequestID ||
		out.LastTurnNo != in.LastTurnNo ||
		out.LastRequestSummary != in.LastRequestSummary ||
		out.LastResponseSummary != in.LastResponseSummary ||
		out.LastModel != in.LastModel ||
		out.LastProvider != in.LastProvider ||
		out.TurnIncrement != in.TurnIncrement ||
		out.TokensIncrement != in.TokensIncrement ||
		out.CostIncrement != in.CostIncrement ||
		!out.UpdatedAt.Equal(in.UpdatedAt) {
		t.Errorf("round trip mismatch: got %+v, want %+v", out, in)
	}
}

// TestDecodeUpdatePayload_MissingSessionID guards against silently accepting
// payloads that lost their session_id — the aggregator would no-op such a
// row, but the reaper must surface the error so the row transitions to dead.
func TestDecodeUpdatePayload_MissingSessionID(t *testing.T) {
	raw := []byte(`{"tenant_id":"t","request_id":"r"}`)
	var out SessionUpdate
	if err := decodeUpdatePayload(raw, &out); err == nil {
		t.Fatalf("expected error for missing session_id, got nil")
	}
}

// TestReaper_DefaultConstants pins the documented retry contract so a
// reviewer can grep these constants and reason about the worst-case retry
// window (1s + 2s + 4s + ... + 512s capped at 1h × 10 attempts = 1023s ≈
// 17 minutes before a row transitions to status='dead').
func TestReaper_DefaultConstants(t *testing.T) {
	if sessionOutboxDefaultInterval != 30*time.Second {
		t.Errorf("default interval drifted: %v", sessionOutboxDefaultInterval)
	}
	if sessionOutboxDefaultBatch != 100 {
		t.Errorf("default batch drifted: %d", sessionOutboxDefaultBatch)
	}
	if sessionOutboxDefaultMaxAtts != 10 {
		t.Errorf("default max attempts drifted: %d", sessionOutboxDefaultMaxAtts)
	}
	if sessionOutboxMaxBackoff != time.Hour {
		t.Errorf("max backoff drifted: %v", sessionOutboxMaxBackoff)
	}
}

// TestReaper_StartStopIdempotent ensures the lifecycle is safe to call from
// cmd/gateway main without ordering constraints.
func TestReaper_StartStopIdempotent(t *testing.T) {
	r := newSessionAggregateOutboxReaper(nil, nil, 0, 0, 0)
	r.Start(nil)
	r.Start(nil) // second Start must not panic / spawn a second goroutine
	r.Stop()
	r.Stop() // second Stop must not panic / deadlock
}
