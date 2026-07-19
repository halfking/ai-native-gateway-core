package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/kaixuan/llm-gateway-go/domains/dbdegradation"
)

// TelemetryFallbackBufferHandler exposes the in-memory ring buffer
// (domains/dbdegradation.RingBuffer) that holds failed request_log
// INSERTs over HTTP. Three operations:
//
//	GET  /internal/telemetry/fallback-buffer/stats   — counters (capacity/size/dropped/writes)
//	GET  /internal/telemetry/fallback-buffer/dump    — full snapshot, FIFO order
//	POST /internal/telemetry/fallback-buffer/clear   — explicit clear (body {"confirm": true})
//	POST /internal/telemetry/fallback-buffer/replay  — replay to DB via telemetryClient.ReplayFallback
//
// All endpoints are mounted under middleware.NewAdminTokenMiddleware
// in cmd/gateway/main.go so they require the same admin token as
// /healthz/full and /metrics.
//
// 2026-07-20: created to surface the ring buffer added by the
// telemetry-fallback-buffer task; replaces grep-the-logs workflow
// for incident response.
type TelemetryFallbackBufferHandler struct {
	ringBuffer *dbdegradation.RingBuffer
	// replayFn is supplied by main.go because the handler must reach
	// into telemetryClient.ReplayFallback (which is a method on the
	// Client struct, not exported at the package level). Keeping the
	// function pluggable also makes the handler unit-testable.
	replayFn func(ctx context.Context, record dbdegradation.BackupRecord) error
}

func NewTelemetryFallbackBufferHandler(
	rb *dbdegradation.RingBuffer,
	replayFn func(ctx context.Context, record dbdegradation.BackupRecord) error,
) *TelemetryFallbackBufferHandler {
	return &TelemetryFallbackBufferHandler{
		ringBuffer: rb,
		replayFn:   replayFn,
	}
}

// serveHTTP dispatches on path suffix (stats | dump | clear | replay).
// Returns 404 for unknown sub-paths so the mux doesn't fall through
// to other handlers with a confusing prefix.
func (h *TelemetryFallbackBufferHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/internal/telemetry/fallback-buffer/stats":
		h.handleStats(w, r)
	case "/internal/telemetry/fallback-buffer/dump":
		h.handleDump(w, r)
	case "/internal/telemetry/fallback-buffer/clear":
		h.handleClear(w, r)
	case "/internal/telemetry/fallback-buffer/replay":
		h.handleReplay(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *TelemetryFallbackBufferHandler) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, h.ringBuffer.Stats())
}

func (h *TelemetryFallbackBufferHandler) handleDump(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	records := h.ringBuffer.Dump()
	// Wrap to make output self-describing.
	writeJSON(w, http.StatusOK, struct {
		Count   int                          `json:"count"`
		Records []dbdegradation.BackupRecord `json:"records"`
	}{
		Count:   len(records),
		Records: records,
	})
}

type clearRequest struct {
	Confirm bool `json:"confirm"`
}

func (h *TelemetryFallbackBufferHandler) handleClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	var req clearRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, http.ErrBodyReadAfterClose) {
		// empty body is acceptable — refuse without explicit confirm
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "body must be {\"confirm\": true}",
		})
		return
	}
	if !req.Confirm {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "refusing to clear without confirm=true (data would be lost)",
		})
		return
	}
	cleared := h.ringBuffer.Stats().Size
	h.ringBuffer.Clear()
	writeJSON(w, http.StatusOK, map[string]int{"cleared": cleared})
}

type replayRequest struct {
	// Limit caps how many entries to replay; 0 means "all".
	// Useful for chunked replay when DB is slow.
	Limit int `json:"limit"`
}

func (h *TelemetryFallbackBufferHandler) handleReplay(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	if h.replayFn == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "replay not wired (telemetry client not configured)",
		})
		return
	}
	var req replayRequest
	// body is optional — empty body → replay all
	_ = json.NewDecoder(r.Body).Decode(&req)

	replayed, failed, err := h.ringBuffer.Replay(r.Context(), req.Limit, h.replayFn)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error":    err.Error(),
			"replayed": replayed,
			"failed":   failed,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{
		"replayed": replayed,
		"failed":   failed,
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// Body already partly written; best effort logging.
		// Caller can't do anything useful with this error.
		_ = err
	}
}
