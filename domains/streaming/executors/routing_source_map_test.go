package executors

import (
	"encoding/json"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/statesource"
)

// TestRoutingSourceMap_RecordAndRead pins the spec §12 GAP 3 contract:
// RecordRoutingSourceForRequest stores the outer routing_state_source
// keyed by requestID, and RoutingSourceForRequest reads it back.
func TestRoutingSourceMap_RecordAndRead(t *testing.T) {
	ResetRoutingSourceForTest()
	RecordRoutingSourceForRequest("req-src-1", statesource.StateSourceAuthoritative)

	got, conv := RoutingSourceForRequest("req-src-1")
	if got != statesource.StateSourceAuthoritative {
		t.Fatalf("source = %q, want authoritative", got)
	}
	if conv != "" {
		t.Fatalf("conversion_path = %q, want empty (not recorded)", conv)
	}

	// Unknown requestID → empty.
	got, _ = RoutingSourceForRequest("does-not-exist")
	if got != "" {
		t.Fatalf("unknown requestID source = %q, want empty", got)
	}
}

// TestRoutingSourceMap_ConversionPath pins the conversion_path storage.
func TestRoutingSourceMap_ConversionPath(t *testing.T) {
	ResetRoutingSourceForTest()
	RecordRoutingSourceForRequest("req-conv-1", statesource.StateSourceOff)
	RecordConversionPathForRequest("req-conv-1", "ir")

	src, conv := RoutingSourceForRequest("req-conv-1")
	if src != statesource.StateSourceOff {
		t.Fatalf("source = %q, want off", src)
	}
	if conv != "ir" {
		t.Fatalf("conversion_path = %q, want ir", conv)
	}
}

// TestRoutingSourceMap_Clear removes a consumed entry.
func TestRoutingSourceMap_Clear(t *testing.T) {
	ResetRoutingSourceForTest()
	RecordRoutingSourceForRequest("req-clear-1", statesource.StateSourceCanary)
	ClearRoutingSourceForRequest("req-clear-1")
	src, _ := RoutingSourceForRequest("req-clear-1")
	if src != "" {
		t.Fatalf("source after clear = %q, want empty", src)
	}
}

// TestRoutingSourceMap_BoundedEviction pins the FIFO eviction contract:
// once the map exceeds its cap, the oldest entry is dropped and a read
// for it returns empty.
func TestRoutingSourceMap_BoundedEviction(t *testing.T) {
	ResetRoutingSourceForTest()
	// Fill exactly to cap.
	for i := 0; i < routingSourceMapCap; i++ {
		RecordRoutingSourceForRequest(reqID(i), statesource.StateSourceOff)
	}
	if src, _ := RoutingSourceForRequest(reqID(0)); src == "" {
		t.Fatalf("reqID(0) should still be present at exactly cap, got empty")
	}
	// One more push evicts the oldest (reqID(0)).
	RecordRoutingSourceForRequest(reqID(routingSourceMapCap), statesource.StateSourceOff)
	if src, _ := RoutingSourceForRequest(reqID(0)); src != "" {
		t.Fatalf("reqID(0) should have been evicted, got %q", src)
	}
	// The newest entry survives.
	if src, _ := RoutingSourceForRequest(reqID(routingSourceMapCap)); src != statesource.StateSourceOff {
		t.Fatalf("newest entry missing after eviction, got %q", src)
	}
}

func reqID(i int) string {
	// deterministic id generator
	return "req-evict-" + jsonIntString(i)
}

func jsonIntString(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}

// TestApplyRoutingMetadata_MergesIntoCompressionMeta (lives in the
// streaming package) is covered separately; here we only assert the
// executors-side seam. The merge into CompressionMeta is exercised via
// the streaming package's request_log_pipeline tests.

// Compile-time guard: the helper used by recordInitialRequestLog must
// be the same symbol the router writes.
var _ = telemetry.RequestLogEntry{}
