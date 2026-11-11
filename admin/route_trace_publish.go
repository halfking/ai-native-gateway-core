// Package admin — route trace publishing methods for LiveStreamSSEHub.
//
// These methods enable request handlers to publish route trace updates to
// connected dashboard clients. The hub maintains a RouteTraceStore for active
// traces and fans out updates via SSE.
//
// 2026-11-11: Initial implementation for dynamic routing visualization.
// 2026-11-11 (merge fix): 上游初版按一个本仓库不存在的 client API 编写
// （c.identity.TenantID/.Super、client.send 通道）⇒ admin 包无法编译。
// 现按 hub 真实的投递原语重写：mu 保护下的客户端扫描 + writeEvent +
// evict（与 fanOutScope 同构）；route_trace 不携带 Delta/TileDelta ⇒
// 各客户端字节相同，序列化一次即可。租户隔离用 liveStreamClient 的
// 真实字段（tenantID string / isSuper bool）。
package admin

import (
	"encoding/json"
	"log/slog"
	"strconv"
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
	if h == nil || trace == nil {
		return
	}
	snapshot := trace.Snapshot()

	envelope := LiveStreamEnvelope{
		Type:       "route_trace",
		Timestamp:  time.Now(),
		RouteTrace: &snapshot,
	}

	// 序列化一次：route_trace 无 Delta/TileDelta 变体（同 fanOut 的
	// format-agnostic 路径），各客户端字节相同。
	data, err := json.Marshal(envelope)
	if err != nil {
		slog.Warn("route trace marshal failed", "err", err.Error())
		return
	}

	tenantKey := strconv.Itoa(snapshot.TenantID)

	h.mu.RLock()
	clients := make([]*liveStreamClient, 0, len(h.clients))
	for c := range h.clients {
		// Tenant isolation: matching tenant scope or super-admin sees all.
		if !c.isSuper && normalizeLiveStreamTenant(c.tenantID) != normalizeLiveStreamTenant(tenantKey) {
			continue
		}
		clients = append(clients, c)
	}
	h.mu.RUnlock()

	for _, c := range clients {
		if !h.writeEvent(c, data) {
			h.evict(c)
		}
	}
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

// RouteTraceStats returns current statistics about route trace storage.
// Useful for monitoring and debugging.
func (h *LiveStreamSSEHub) RouteTraceStats() map[string]interface{} {
	return map[string]interface{}{
		"active_traces": h.routeTraceStore.Len(),
	}
}
