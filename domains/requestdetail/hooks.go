package requestdetail

import (
	"log/slog"
	"sync"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

var (
	globalMu  sync.RWMutex
	global    *Store
	globalFwd *captureForwarder
)

// SetGlobal registers the process-wide content store (called once at startup).
// The first non-nil call also lazily constructs the async capture forwarder;
// the forwarder's consumer goroutine is started by StartGlobalCaptureForwarder
// (called explicitly from cmd/gateway/main.go so the goroutine's lifecycle
// is observable to the shutdown sequence).
func SetGlobal(s *Store) {
	globalMu.Lock()
	oldFwd := globalFwd
	global = s
	if s == nil {
		globalFwd = nil
	} else if oldFwd == nil || oldFwd.store != s || oldFwd.stopped.Load() {
		globalFwd = newCaptureForwarder(s)
	}
	newFwd := globalFwd
	globalMu.Unlock()
	if oldFwd != nil && oldFwd != newFwd {
		oldFwd.stop()
	}
}

// Global returns the process-wide store (may be nil).
func Global() *Store {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return global
}

// CaptureForwarder returns the process-wide async capture forwarder, if any.
// Returns nil if SetGlobal(nil) was called or the package was never wired.
// Exposed for tests and admin diagnostics.
func CaptureForwarder() *captureForwarder {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return globalFwd
}

// StartGlobalCaptureForwarder launches the consumer goroutine for the
// process-wide forwarder. Idempotent: subsequent calls are no-ops. Returns
// immediately if no store is wired. Called from cmd/gateway/main.go after
// telemetryClient.SetOnRequestLogEmitted is wired so the consumer drains
// the queue in lockstep with telemetry production.
func StartGlobalCaptureForwarder() {
	globalMu.RLock()
	fwd := globalFwd
	globalMu.RUnlock()
	if fwd == nil {
		return
	}
	if fwd.started.CompareAndSwap(false, true) {
		go fwd.runStarted()
		slog.Info("requestdetail: async capture forwarder started",
			"queue_capacity", captureForwarderCapacity)
	}
}

// StopGlobalCaptureForwarder closes the consumer goroutine. Idempotent.
// Called from the main shutdown sequence.
func StopGlobalCaptureForwarder() {
	globalMu.RLock()
	fwd := globalFwd
	globalMu.RUnlock()
	if fwd == nil {
		return
	}
	fwd.stop()
}

// CaptureFromEntry is the onEmitted hook handed to telemetry. It is
// non-blocking: the entry is shallow-copied and enqueued onto the
// process-wide forwarder; marshalling + file I/O happen on the consumer
// goroutine started by StartGlobalCaptureForwarder. See
// captureForwarder docstring for the design rationale.
func CaptureFromEntry(entry *telemetry.RequestLogEntry) {
	globalMu.RLock()
	fwd := globalFwd
	globalMu.RUnlock()
	if fwd == nil {
		return
	}
	fwd.emit(entry)
}

// CaptureFromEntrySync performs a synchronous body capture, bypassing the
// forwarder. Kept for tests that need deterministic capture-then-assert
// ordering, and as a last-resort fallback if StartGlobalCaptureForwarder
// was never called (e.g. embedded use without telemetry). Production
// callers should use CaptureFromEntry.
//
// 2026-08-29 (audit follow-up): empty RequestID now logs a Warn so
// misconfigured callers are visible.
func CaptureFromEntrySync(entry *telemetry.RequestLogEntry) {
	s := Global()
	if s == nil || entry == nil {
		return
	}
	if entry.RequestID == "" {
		slog.Warn("requestdetail: CaptureFromEntrySync skipping entry with empty RequestID",
			"tenant_id", entry.TenantID)
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
		slog.Warn("requestdetail: capture failed",
			"request_id", entry.RequestID,
			"error", err)
	}
}

// ClearAfterPersist removes local hot content once DB persist succeeded
// for a terminal request status (not in_progress).
func ClearAfterPersist(entry *telemetry.RequestLogEntry) {
	s := Global()
	if s == nil || entry == nil {
		return
	}
	if entry.RequestID == "" {
		slog.Warn("requestdetail: ClearAfterPersist skipping entry with empty RequestID",
			"tenant_id", entry.TenantID)
		return
	}
	if entry.RequestStatus != nil && *entry.RequestStatus == "in_progress" {
		return
	}
	if err := s.Clear(entry.RequestID); err != nil {
		slog.Warn("requestdetail: clear after persist failed",
			"request_id", entry.RequestID,
			"error", err)
	}
}
