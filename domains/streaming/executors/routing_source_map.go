package executors

import (
	"sync"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/statesource"
)

// routingSourceMap (spec §12 GAP 3) is a process-local, request-id-keyed
// store for the per-request routing_state_source decision. The router
// records the OUTER state source (authoritative / fallback / canary / off)
// via statesource.RecordRoutingStateSource for the global counter, but
// that counter is aggregate — it cannot be attributed back to a single
// request_logs row. This map is the bridge: PlanCandidatesWithContext
// writes the source here keyed by requestID, and the streaming layer's
// recordInitialRequestLog reads it (via RoutingSourceForRequest) so the
// value lands in request_logs metadata (compression_meta JSONB until a
// dedicated routing_state_source column is migrated).
//
// Sizing: bounded to routingSourceMapCap entries with FIFO eviction so a
// stuck/long-lived requestID cannot grow the map unbounded. The map is
// best-effort — a missed read (entry evicted or never written) leaves
// the request_logs row with empty routing metadata, which is the
// pre-GAP-3 behaviour, never a request failure.
const routingSourceMapCap = 20000

type routingSourceEntry struct {
	source         statesource.RoutingStateSource
	conversionPath string // "ir" | "legacy" | "" (spec §12 GAP 3 conversion_path)
}

var (
	routingSourceMu sync.Mutex
	// routingSourceStore is a slice-as-FIFO + index. Pre-allocating the
	// index to the cap avoids map growth churn on the hot routing path;
	// eviction drops the oldest requestID (the head of the slice).
	routingSourceStore = make(map[string]routingSourceEntry, routingSourceMapCap)
	routingSourceFIFO  = make([]string, 0, routingSourceMapCap)
)

// RecordRoutingSourceForRequest records the outer routing_state_source
// and the conversion_path for a single request. Called by
// PlanCandidatesWithContext's recordOuterSource closure. Idempotent per
// requestID (the router's outerSourceRecorded guard already ensures one
// record per PlanCandidatesWithContext invocation; a duplicate write for
// the same requestID updates the existing entry rather than growing the
// FIFO, so retries do not evict fresh entries).
func RecordRoutingSourceForRequest(requestID string, source statesource.RoutingStateSource) {
	if requestID == "" {
		return
	}
	routingSourceMu.Lock()
	defer routingSourceMu.Unlock()
	if _, exists := routingSourceStore[requestID]; !exists {
		if len(routingSourceFIFO) >= routingSourceMapCap {
			// Evict the oldest entry (FIFO head).
			oldest := routingSourceFIFO[0]
			routingSourceFIFO = routingSourceFIFO[1:]
			delete(routingSourceStore, oldest)
		}
		routingSourceFIFO = append(routingSourceFIFO, requestID)
	}
	routingSourceStore[requestID] = routingSourceEntry{
		source: source,
		// Preserve a previously-recorded conversion_path if present;
		// the two fields may be set by independent call sites.
		conversionPath: routingSourceStore[requestID].conversionPath,
	}
}

// RecordConversionPathForRequest records the conversion_path
// ("ir" | "legacy") for a single request. Called by the
// TransportFactory.Pick instrumentation point. Best-effort: a request
// whose routing source was never recorded (entry absent) still gets a
// conversion_path entry so the request_logs metadata captures the
// transport decision even on the off/no-v2 path.
func RecordConversionPathForRequest(requestID, path string) {
	if requestID == "" || path == "" {
		return
	}
	routingSourceMu.Lock()
	defer routingSourceMu.Unlock()
	if _, exists := routingSourceStore[requestID]; !exists {
		if len(routingSourceFIFO) >= routingSourceMapCap {
			oldest := routingSourceFIFO[0]
			routingSourceFIFO = routingSourceFIFO[1:]
			delete(routingSourceStore, oldest)
		}
		routingSourceFIFO = append(routingSourceFIFO, requestID)
	}
	e := routingSourceStore[requestID]
	e.conversionPath = path
	routingSourceStore[requestID] = e
}

// RoutingSourceForRequest returns the recorded routing_state_source and
// conversion_path for a request. Empty values mean the entry was never
// recorded or was evicted (best-effort contract). Read by the streaming
// layer's recordInitialRequestLog so request_logs metadata carries the
// routing decision.
func RoutingSourceForRequest(requestID string) (source statesource.RoutingStateSource, conversionPath string) {
	if requestID == "" {
		return "", ""
	}
	routingSourceMu.Lock()
	defer routingSourceMu.Unlock()
	e, ok := routingSourceStore[requestID]
	if !ok {
		return "", ""
	}
	return e.source, e.conversionPath
}

// ClearRoutingSourceForRequest removes the entry after it has been
// consumed by recordInitialRequestLog, so the map does not retain
// completed requests longer than necessary. Best-effort; a missing key
// is not an error.
func ClearRoutingSourceForRequest(requestID string) {
	if requestID == "" {
		return
	}
	routingSourceMu.Lock()
	defer routingSourceMu.Unlock()
	if _, ok := routingSourceStore[requestID]; !ok {
		return
	}
	delete(routingSourceStore, requestID)
	// Remove from FIFO (linear scan; the cap keeps this bounded and the
	// call is off the hot path — it runs once per completed request).
	for i, id := range routingSourceFIFO {
		if id == requestID {
			routingSourceFIFO = append(routingSourceFIFO[:i], routingSourceFIFO[i+1:]...)
			break
		}
	}
}

// ResetRoutingSourceForTest clears the map. Test-only.
func ResetRoutingSourceForTest() {
	routingSourceMu.Lock()
	defer routingSourceMu.Unlock()
	routingSourceStore = make(map[string]routingSourceEntry, routingSourceMapCap)
	routingSourceFIFO = make([]string, 0, routingSourceMapCap)
}
