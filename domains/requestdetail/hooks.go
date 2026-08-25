package requestdetail

import (
	"sync"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

var (
	globalMu sync.RWMutex
	global   *Store
)

// SetGlobal registers the process-wide content store (called once at startup).
func SetGlobal(s *Store) {
	globalMu.Lock()
	global = s
	globalMu.Unlock()
}

// Global returns the process-wide store (may be nil).
func Global() *Store {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return global
}

// CaptureFromEntry writes in-flight meta + bodies from a telemetry entry.
func CaptureFromEntry(entry *telemetry.RequestLogEntry) {
	s := Global()
	if s == nil || entry == nil || entry.RequestID == "" {
		return
	}
	meta := Meta{
		RequestID:   entry.RequestID,
		TenantID:    entry.TenantID,
		GwSessionID: entry.GwSessionID,
		GwTaskID:    entry.GwTaskID,
		ClientModel: entry.ClientModel,
		Status:      entry.RequestStatus,
		LatencyMs:   entry.LatencyMs,
	}
	success := entry.Success
	meta.Success = &success

	var bodies *Bodies
	if entry.RequestBody != nil || entry.ResponseBody != nil || len(entry.OutboundBody) > 0 {
		bodies = &Bodies{}
		if entry.RequestBody != nil {
			bodies.RequestBody = []byte(*entry.RequestBody)
		}
		if entry.ResponseBody != nil {
			bodies.ResponseBody = []byte(*entry.ResponseBody)
		}
		if len(entry.OutboundBody) > 0 {
			bodies.OutboundBody = append([]byte(nil), entry.OutboundBody...)
		}
	}
	_ = s.Put(meta, bodies)
}

// ClearAfterPersist removes local hot content once DB persist succeeded
// for a terminal request status (not in_progress).
func ClearAfterPersist(entry *telemetry.RequestLogEntry) {
	s := Global()
	if s == nil || entry == nil || entry.RequestID == "" {
		return
	}
	if entry.RequestStatus != nil && *entry.RequestStatus == "in_progress" {
		return
	}
	s.Clear(entry.RequestID)
}
