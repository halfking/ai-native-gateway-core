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
	// "rescued" should bump. Asserting the negative prevents an over-eager
	// rescue path from masking actual discards.
	discardBefore := counterValue(t, "discarded", "request_body", "json_field", "sanitize")
	rescuedBefore := counterValue(t, "rescued", "request_body", "json_field", "sanitize")

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
	if got := counterValue(t, "rescued", "request_body", "json_field", "sanitize") - rescuedBefore; got != 0 {
		t.Fatalf("rescued delta = %v, want 0", got)
	}
}

// TestSanitizeJSONField_RescuedKeepsTruncatedPrefix is the Step 6 / 245
// incident regression test: when sanitizeUTF8JSON can rescue a valid JSON
// prefix (e.g. a streamed body that ended with garbage bytes after the
// closing brace) we now KEEP the truncated prefix and label the event
// `rescued` — previously the same input was treated as binary discard in
// some call paths, hiding the body from the /request-logs UI.
func TestSanitizeJSONField_RescuedKeepsTruncatedPrefix(t *testing.T) {
	// A valid JSON object followed by stray "garbage" + invalid UTF-8.
	// truncateToValidJSON walks the string from the end looking for '}'
	// and ']' candidates; the closing brace of the object is the first
	// candidate that parses as valid JSON, so the rescued result is the
	// object minus the trailing garbage.
	input := `{"a":"v"}garbage` + "\xff\xfe"
	before := counterValue(t, "rescued", "request_body", "json_field", "sanitize")

	ptr := &input
	sanitizeJSONField("request_body", &ptr)

	if ptr == nil {
		t.Fatal("expected rescued (non-nil) result, got nil")
	}
	if *ptr == input {
		t.Fatalf("expected truncated output, got unchanged %q", *ptr)
	}
	// Truncated prefix must be valid JSON and start with '{'.
	if (*ptr)[0] != '{' {
		t.Fatalf("expected rescued prefix to start with '{', got %q", *ptr)
	}

	after := counterValue(t, "rescued", "request_body", "json_field", "sanitize")
	if got := after - before; got != 1 {
		t.Fatalf("expected delta=1, got %v", got)
	}

	// Sanity: discarded counter must NOT bump on a rescueable input.
	discardBefore := counterValue(t, "discarded", "request_body", "json_field", "sanitize")
	ptr2 := &input
	sanitizeJSONField("request_body", &ptr2)
	if got := counterValue(t, "discarded", "request_body", "json_field", "sanitize") - discardBefore; got != 0 {
		t.Fatalf("discarded delta = %v on rescueable input, want 0", got)
	}
}

// TestSanitizeRawJSONField_RescuedKeepsTruncatedPrefix mirrors the json
// variant for json.RawMessage columns (outbound_body / tool_calls /
// attachments / routing_attempts / …). Same expectation: truncated JSON
// prefix is retained and labelled `rescued`.
func TestSanitizeRawJSONField_RescuedKeepsTruncatedPrefix(t *testing.T) {
	input := json.RawMessage(`[1,2,3]garbage` + "\xff")
	before := counterValue(t, "rescued", "outbound_body", "raw_json_field", "sanitize")

	sanitizeRawJSONField("outbound_body", &input)

	if input == nil {
		t.Fatal("expected rescued (non-nil) result, got nil")
	}
	if string(input) == string(`[1,2,3]garbage`+"\xff") {
		t.Fatalf("expected truncated output, got unchanged %q", string(input))
	}
	if string(input)[0] != '[' {
		t.Fatalf("expected rescued prefix to start with '[', got %q", string(input))
	}

	after := counterValue(t, "rescued", "outbound_body", "raw_json_field", "sanitize")
	if got := after - before; got != 1 {
		t.Fatalf("expected delta=1, got %v", got)
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
