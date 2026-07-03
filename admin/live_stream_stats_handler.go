package admin

import (
	"encoding/json"
	"net/http"
)

// handleLiveStreamStats 返回 SSE Hub 的监控指标
func (h *Handler) handleLiveStreamStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if h.liveStreamHub == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Live stream hub not initialized",
		})
		return
	}

	stats := h.liveStreamHub.Stats()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}
