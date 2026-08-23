package telemetry

import (
	"encoding/json"
	"testing"

	dto "github.com/prometheus/client_model/go"
)

// counterValue reads a single labelled Counter's current value via the
// prometheus.Metric Write path (matches the request_logger_prometheus_test
// convention). Returns the value or panics if the series does not exist
// — every label tuple is pre-initialised at boot by sanitize_prometheus.go
// so this should never happen in practice.
func counterValue(t *testing.T, outcome, field, source, stage string) float64 {
	t.Helper()
	m := &dto.Metric{}
	if err := sanitizeEventsTotal.WithLabelValues(outcome, field, source, stage).Write(m); err != nil {
		t.Fatalf("counter write for (%s/%s/%s/%s): %v", outcome, field, source, stage, err)
	}
	return m.Counter.GetValue()
}

func TestSanitizeJSONField_DiscardsAndIncrementsMetric(t *testing.T) {
	// The exact 0xE5 0xBC 0xE2 invalid UTF-8 sequence from the 2026-06-11
	// incident (and again the minimax-m3 reproducer on env 245).
	bad := string([]byte{0xE5, 0xBC, 0xE2, 0x80, 0xA6}) + "prompt fragment"
	before := counterValue(t, "discarded", "request_body", "json_field", "sanitize")

	ptr := &bad
	sanitizeJSONField("request_body", &ptr)

	if ptr != nil {
		t.Fatalf("expected nil after discard, got %q", *ptr)
	}

	after := counterValue(t, "discarded", "request_body", "json_field", "sanitize")
	if got := after - before; got != 1 {
		t.Fatalf("expected delta=1, got %v", got)
	}
}

func TestSanitizeRawJSONField_DiscardsAndIncrementsMetric(t *testing.T) {
	bad := json.RawMessage("\xff\xfe\xfd")
	before := counterValue(t, "discarded", "outbound_body", "raw_json_field", "sanitize")

	sanitizeRawJSONField("outbound_body", &bad)

	if bad != nil {
		t.Fatalf("expected nil after discard, got %q", string(bad))
	}

	after := counterValue(t, "discarded", "outbound_body", "raw_json_field", "sanitize")
	if got := after - before; got != 1 {
		t.Fatalf("expected delta=1, got %v", got)
	}
}

func TestSanitizeJSONField_ValidInputDoesNotIncrement(t *testing.T) {
	// Valid JSON should pass through untouched — neither "discarded" nor
	// "repaired" should bump. Asserting the negative prevents an
	// over-eager repair path from masking actual discards.
	discardBefore := counterValue(t, "discarded", "request_body", "json_field", "sanitize")
	repairedBefore := counterValue(t, "repaired", "request_body", "json_field", "sanitize")

	good := `{"model":"minimax-m3","messages":[{"role":"user","content":"hello 世界"}]}`
	ptr := &good
	sanitizeJSONField("request_body", &ptr)

	if ptr == nil {
		t.Fatal("expected passthrough, got nil")
	}
	if *ptr != good {
		t.Fatalf("expected unchanged passthrough, got %q", *ptr)
	}

	if got := counterValue(t, "discarded", "request_body", "json_field", "sanitize") - discardBefore; got != 0 {
		t.Fatalf("discarded delta = %v, want 0", got)
	}
	if got := counterValue(t, "repaired", "request_body", "json_field", "sanitize") - repairedBefore; got != 0 {
		t.Fatalf("repaired delta = %v, want 0", got)
	}
}

func TestEmitRequestLogUpdate_RejectsEmptyRequestID(t *testing.T) {
	// Build a minimal Client with no DB — EmitRequestLogUpdate must short-
	// circuit before touching the queue when RequestID is empty.
	c := &Client{}

	entry := &RequestLogEntry{
		RequestID:   "",
		TenantID:    "default",
		ClientModel: strPtr("minimax-m3"),
	}

	before := counterValue(t, "discarded", "request_id", "string_field", "required_field_guard")

	c.EmitRequestLogUpdate(entry)

	after := counterValue(t, "discarded", "request_id", "string_field", "required_field_guard")
	if got := after - before; got != 1 {
		t.Fatalf("expected delta=1, got %v", got)
	}
	// Queue should not have been touched (it's nil on a zero-value Client,
	// but the function would have panicked if it tried to send to nil —
	// reaching this line is itself the assertion).
}

func strPtr(s string) *string { return &s }
