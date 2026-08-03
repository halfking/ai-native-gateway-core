package streaming

import (
	"encoding/json"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/statesource"
)

// TestApplyRoutingMetadata_MergesSourceAndPath pins the spec §12 GAP 3
// contract: applyRoutingMetadata reads the per-request routing decision
// from the executors seam and merges routing_state_source +
// conversion_path into request_logs.compression_meta JSONB (the generic
// metadata map, pending a dedicated column migration).
func TestApplyRoutingMetadata_MergesSourceAndPath(t *testing.T) {
	executors.ResetRoutingSourceForTest()
	t.Cleanup(executors.ResetRoutingSourceForTest)
	executors.RecordRoutingSourceForRequest("req-meta-1", statesource.StateSourceAuthoritative)
	executors.RecordConversionPathForRequest("req-meta-1", "ir")

	entry := &telemetry.RequestLogEntry{RequestID: "req-meta-1"}
	applyRoutingMetadata(entry, "req-meta-1")

	if len(entry.CompressionMeta) == 0 {
		t.Fatal("CompressionMeta not merged")
	}
	var m map[string]any
	if err := json.Unmarshal(entry.CompressionMeta, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := m["routing_state_source"]; got != "authoritative" {
		t.Fatalf("routing_state_source = %v, want authoritative", got)
	}
	if got := m["conversion_path"]; got != "ir" {
		t.Fatalf("conversion_path = %v, want ir", got)
	}
}

// TestApplyRoutingMetadata_PreservesExistingMeta pins the additive
// merge: pre-existing compression_meta keys must survive the routing
// metadata merge (same contract as mergeCompressionMetaV3).
func TestApplyRoutingMetadata_PreservesExistingMeta(t *testing.T) {
	executors.ResetRoutingSourceForTest()
	t.Cleanup(executors.ResetRoutingSourceForTest)
	executors.RecordRoutingSourceForRequest("req-meta-2", statesource.StateSourceFallback)

	existing := json.RawMessage(`{"window_triggered":"size","strategy":"auto_threshold"}`)
	entry := &telemetry.RequestLogEntry{
		RequestID:       "req-meta-2",
		CompressionMeta: existing,
	}
	applyRoutingMetadata(entry, "req-meta-2")

	var m map[string]any
	if err := json.Unmarshal(entry.CompressionMeta, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := m["routing_state_source"]; got != "fallback" {
		t.Fatalf("routing_state_source = %v, want fallback", got)
	}
	// Pre-existing keys preserved.
	if got := m["window_triggered"]; got != "size" {
		t.Fatalf("window_triggered = %v, want size (must be preserved)", got)
	}
	if got := m["strategy"]; got != "auto_threshold" {
		t.Fatalf("strategy = %v, want auto_threshold (must be preserved)", got)
	}
}

// TestApplyRoutingMetadata_NoopWhenUnrecorded pins the best-effort
// contract: a request whose routing source was never recorded (or was
// evicted) leaves compression_meta untouched — never a row failure.
func TestApplyRoutingMetadata_NoopWhenUnrecorded(t *testing.T) {
	executors.ResetRoutingSourceForTest()
	t.Cleanup(executors.ResetRoutingSourceForTest)

	entry := &telemetry.RequestLogEntry{RequestID: "req-meta-3"}
	applyRoutingMetadata(entry, "req-meta-3")
	if len(entry.CompressionMeta) != 0 {
		t.Fatalf("CompressionMeta = %s, want empty for unrecorded request", entry.CompressionMeta)
	}
}
