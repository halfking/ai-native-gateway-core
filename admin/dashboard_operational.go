package admin

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"
)

const boardOperationalCacheTTL = 30 * time.Second

type boardOperationalCache struct {
	mu      sync.Mutex
	expires time.Time
	payload map[string]any
}

func (c *boardOperationalCache) get() (map[string]any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.payload == nil || time.Now().After(c.expires) {
		return nil, false
	}
	out := make(map[string]any, len(c.payload))
	for k, v := range c.payload {
		out[k] = v
	}
	return out, true
}

func (c *boardOperationalCache) set(payload map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.payload = payload
	c.expires = time.Now().Add(boardOperationalCacheTTL)
}

func (h *Handler) ensureBoardOperationalCache() *boardOperationalCache {
	if h.boardOperationalCache == nil {
		h.boardOperationalCache = &boardOperationalCache{}
	}
	return h.boardOperationalCache
}

func includeBoardOperational(r *http.Request) bool {
	if r == nil {
		return true
	}
	v := strings.TrimSpace(r.URL.Query().Get("include_operational"))
	if v == "" || v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes") {
		return true
	}
	return false
}

func (h *Handler) boardOperationalPayload(ctx context.Context) map[string]any {
	cache := h.ensureBoardOperationalCache()
	if cached, ok := cache.get(); ok {
		return cached
	}
	payload := map[string]any{
		"background_tasks": h.queryBoardBackgroundTasks(ctx),
		"selfcheck":        h.queryBoardSelfCheck(ctx),
	}
	cache.set(payload)
	return payload
}

func (h *Handler) handleDashboardOperational(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	w.Header().Set("Cache-Control", "private, max-age=30")
	writeJSON(w, http.StatusOK, h.boardOperationalPayload(ctx))
}
