package requestdetail

import (
	"log/slog"
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
			bodies.RequestBody = DecodeRaw(entry.RequestBody)
		}
		if entry.ResponseBody != nil {
			bodies.ResponseBody = DecodeRaw(entry.ResponseBody)
		}
		if len(entry.OutboundBody) > 0 {
			bodies.OutboundBody = append([]byte(nil), entry.OutboundBody...)
		}
	}
	if err := s.Put(meta, bodies); err != nil {
		// Capture runs on the telemetry path; retain observability without
		// turning a best-effort snapshot failure into a request failure.
		slog.Warn("requestdetail: capture failed", "request_id", entry.RequestID, "error", err)
	}
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
