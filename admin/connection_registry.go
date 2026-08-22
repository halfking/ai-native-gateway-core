package admin

import (
	"net/http"
	"sync/atomic"

	"github.com/kaixuan/llm-gateway-go/domains/streaming"
)

// connection_registry.go — 会话优化 v4 R1.6 / §13.5 API（T4）
//
// Read-only admin projection of the streaming connection registry:
//
//	GET /api/admin/connection-registry            → live connections list
//	GET /api/admin/connection-registry/{id}       → one connection + close audit
//
// Only metadata (protocol/client type/timestamps/counters/close reason) is
// exposed — never request bodies, credentials or frame content (§13.5 安全
// 约束). The registry instance is wired from cmd/gateway/main.go via
// SetConnectionRegistry; route registration happens in RegisterRoutes
// (integrator follow-up — this file deliberately adds methods only, per the
// T4 file-domain split).

// connectionRegistry holds the process-wide streaming registry injected at
// startup. Package-level state mirrors the SetXxx wiring style used for
// handlers that must not touch admin/handler.go struct declarations.
var connectionRegistry atomic.Pointer[streaming.ConnectionRegistry]

// SetConnectionRegistry wires the streaming connection registry for the
// /api/admin/connection-registry endpoints. Pass nil to disable (endpoints
// return 503).
func SetConnectionRegistry(reg *streaming.ConnectionRegistry) {
	if reg == nil {
		return
	}
	connectionRegistry.Store(reg)
}

// CurrentConnectionRegistry returns the wired registry (nil when unset).
func CurrentConnectionRegistry() *streaming.ConnectionRegistry {
	return connectionRegistry.Load()
}

// handleConnectionRegistryList serves GET /api/admin/connection-registry:
// every live entry plus the most recent closed-connection audit records
// (注销原因: write_deadline / replaced / stream_end / ...).
func (h *Handler) handleConnectionRegistryList(w http.ResponseWriter, r *http.Request) {
	reg := connectionRegistry.Load()
	if reg == nil {
		writeError(w, http.StatusServiceUnavailable, "connection registry not wired")
		return
	}
	live := reg.List()
	writeJSON(w, http.StatusOK, map[string]any{
		"live":       live,
		"live_count": len(live),
		"capacity":   reg.Capacity(),
		"closed":     reg.ClosedHistory(50),
	})
}

// handleConnectionRegistryGet serves GET /api/admin/connection-registry/{request_id}:
// one live entry's metadata (最后帧时间/帧计数) or 404 when the request is not
// registered. Body content is never included.
func (h *Handler) handleConnectionRegistryGet(w http.ResponseWriter, r *http.Request) {
	reg := connectionRegistry.Load()
	if reg == nil {
		writeError(w, http.StatusServiceUnavailable, "connection registry not wired")
		return
	}
	requestID := r.PathValue("request_id")
	if requestID == "" {
		writeError(w, http.StatusBadRequest, "missing request_id")
		return
	}
	snap, ok := reg.LookupAny(requestID)
	if !ok {
		writeError(w, http.StatusNotFound, "request not registered")
		return
	}
	writeJSON(w, http.StatusOK, snap)
}
