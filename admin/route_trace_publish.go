// Package admin — route trace publishing methods for LiveStreamSSEHub.
//
// These methods enable request handlers to publish route trace updates to
// connected dashboard clients. The hub maintains a RouteTraceStore for active
// traces and fans out updates via SSE.
//
// 2026-11-11: Initial implementation for dynamic routing visualization.
package admin

import (
	"log/slog"
	"time"
)

// StartRouteTrace creates and registers a new route trace for a request.
// Returns the trace instance for the caller to update as routing progresses.
// Thread-safe.
func (h *LiveStreamSSEHub) StartRouteTrace(requestID string, tenantID int, clientModel string) *RouteTrace {
	trace := NewRouteTrace(requestID, tenantID, clientModel)
	h.routeTraceStore.Set(trace)
	
	// Broadcast initial trace state
	h.publishRouteTrace(trace)
	
	return trace
}

// PublishRouteTraceUpdate broadcasts an incremental update for a route trace.
// Should be called after any significant routing event (phase change, attempt
// start/complete, etc.). Thread-safe.
func (h *LiveStreamSSEHub) PublishRouteTraceUpdate(trace *RouteTrace) {
	if trace == nil {
		return
	}
	h.publishRouteTrace(trace)
}

// publishRouteTrace is the internal method that creates and fans out a
// route_trace envelope to connected clients.
func (h *LiveStreamSSEHub) publishRouteTrace(trace *RouteTrace) {
	snapshot := trace.Snapshot()
	
	envelope := LiveStreamEnvelope{
		Type:       "route_trace",
		Timestamp:  time.Now(),
		RouteTrace: &snapshot,
	}
	
	h.fanOutEnvelope(envelope, func(c *liveStreamClient) bool {
		// Tenant isolation: only deliver to clients with matching tenant scope
		if c.identity.TenantID != nil && *c.identity.TenantID == snapshot.TenantID {
			return true
		}
		// Super-admin clients see all traces
		if c.identity.Super {
			return true
		}
		return false
	})
}

// CompleteRouteTrace marks a route trace as complete and broadcasts the final
// state. The trace remains in the store for a short period before cleanup.
// Thread-safe.
func (h *LiveStreamSSEHub) CompleteRouteTrace(requestID string, finalStatus int, latencyMs int64) {
	trace := h.routeTraceStore.Get(requestID)
	if trace == nil {
		return
	}
	
	trace.MarkDone(finalStatus, latencyMs)
	h.publishRouteTrace(trace)
	
	// Note: trace is NOT immediately removed from store here.
	// The periodic cleanup ticker will remove it after 10 minutes.
	// This allows clients to query recent completed traces.
}

// FailRouteTrace marks a route trace as failed and broadcasts the final state.
// Thread-safe.
func (h *LiveStreamSSEHub) FailRouteTrace(requestID string, finalStatus int, latencyMs int64, errorKind, errorMsg string) {
	trace := h.routeTraceStore.Get(requestID)
	if trace == nil {
		return
	}
	
	trace.MarkFailed(finalStatus, latencyMs, errorKind, errorMsg)
	h.publishRouteTrace(trace)
}

// GetRouteTrace retrieves an active route trace by request ID.
// Returns nil if not found. Thread-safe.
func (h *LiveStreamSSEHub) GetRouteTrace(requestID string) *RouteTrace {
	return h.routeTraceStore.Get(requestID)
}

// fanOutEnvelope sends an envelope to all clients matching the predicate.
// This is a lower-level primitive used by publishRouteTrace and other
// specialized broadcast methods.
func (h *LiveStreamSSEHub) fanOutEnvelope(envelope LiveStreamEnvelope, predicate func(*liveStreamClient) bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	
	sent := 0
	for client := range h.clients {
		if predicate(client) {
			select {
			case client.send <- envelope:
				sent++
			default:
				// Client's send buffer is full; skip this message.
				// The client will catch up on reconnect via initial_data.
				slog.Debug("route trace: client send buffer full, dropping envelope",
					"client", client.identity.Name, "type", envelope.Type)
			}
		}
	}
	
	if sent > 0 {
		slog.Debug("route trace: envelope fanned out",
			"type", envelope.Type,
			"request_id", func() string {
				if envelope.RouteTrace != nil {
					return envelope.RouteTrace.RequestID
				}
				return ""
			}(),
			"clients", sent)
	}
}

// RouteTraceStats returns current statistics about route trace storage.
// Useful for monitoring and debugging.
func (h *LiveStreamSSEHub) RouteTraceStats() map[string]interface{} {
	return map[string]interface{}{
		"active_traces": h.routeTraceStore.Len(),
	}
}
