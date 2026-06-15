package admin

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/memora"
)

// handleMemoraStatus returns the current Memora connectivity status and
// sink statistics for the admin UI dashboard.
func (h *Handler) handleMemoraStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	resp := map[string]any{
		"enabled":   false,
		"base_url":  nil,
		"connected": false,
	}
	if h.memoraClient != nil {
		baseURL := h.memoraClient.BaseURL()
		enabled := !h.memoraClient.Disabled()
		resp["enabled"] = enabled
		if enabled {
			resp["base_url"] = baseURL
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		pingErr := h.memoraClient.Ping(ctx)
		cancel()
		if !enabled {
			resp["connected"] = false
		} else if pingErr == nil {
			resp["connected"] = true
		} else {
			resp["connected"] = false
			resp["last_error"] = pingErr.Error()
		}
	}
	if h.memoraSink != nil {
		st := h.memoraSink.Stats()
		resp["sink"] = map[string]any{
			"enqueued":           st.Enqueued,
			"dropped":            st.Dropped,
			"processed":          st.Processed,
			"errored":            st.Errored,
			"queue_len":          st.QueueLen,
			"queue_cap":          st.QueueCap,
			"consecutive_errors": st.ConsecutiveErrors,
			"last_error":         st.LastError,
			"last_error_at":      st.LastErrorAt,
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleMemoraPing performs a manual connectivity test against Memora
// and returns the result with latency.
func (h *Handler) handleMemoraPing(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.memoraClient == nil || h.memoraClient.Disabled() {
		writeJSON(w, http.StatusOK, map[string]any{
			"connected": false,
			"error":     "memora not configured",
		})
		return
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	err := h.memoraClient.Ping(ctx)
	cancel()
	latency := time.Since(start).Milliseconds()
	if err == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"connected":   true,
			"latency_ms": latency,
		})
	} else {
		writeJSON(w, http.StatusOK, map[string]any{
			"connected":   false,
			"latency_ms": latency,
			"error":       err.Error(),
		})
	}
}

// handleMemoraSessions lists recent task/session activity from
// request_logs, grouped by gw_task_id.
func (h *Handler) handleMemoraSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	hours := 24
	if v := r.URL.Query().Get("hours"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 168 {
			hours = n
		}
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	query := fmt.Sprintf(`
		SELECT
			gw_task_id,
			gw_session_id,
			COUNT(*) AS request_count,
			COUNT(*) FILTER (WHERE request_status = 'success') AS ok_count,
			COUNT(*) FILTER (WHERE request_status = 'failure') AS fail_count,
			MAX(ts) AS last_activity,
			(SELECT rl2.client_model FROM request_logs rl2
			 WHERE rl2.gw_task_id = rl.gw_task_id AND rl2.gw_task_id <> ''
			 ORDER BY rl2.ts DESC LIMIT 1) AS latest_model,
			(SELECT rl3.api_key_prefix FROM request_logs rl3
			 WHERE rl3.gw_task_id = rl.gw_task_id AND rl3.gw_task_id <> ''
			 ORDER BY rl3.ts DESC LIMIT 1) AS latest_key_prefix
		FROM request_logs rl
		WHERE ts > NOW() - INTERVAL '%d hours'
		  AND gw_task_id <> ''
		GROUP BY gw_task_id, gw_session_id
		ORDER BY last_activity DESC
		LIMIT $1
	`, hours)
	rows, err := h.db.Query(ctx, query, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	var sessions []map[string]any
	for rows.Next() {
		var taskID, sessionID, latestModel, latestKeyPrefix *string
		var requestCount, okCount, failCount int
		var lastActivity time.Time
		if err := rows.Scan(&taskID, &sessionID, &requestCount, &okCount, &failCount,
			&lastActivity, &latestModel, &latestKeyPrefix); err != nil {
			continue
		}
		if q != "" {
			qLower := strings.ToLower(q)
			match := false
			if taskID != nil && strings.Contains(strings.ToLower(*taskID), qLower) {
				match = true
			}
			if sessionID != nil && strings.Contains(strings.ToLower(*sessionID), qLower) {
				match = true
			}
			if latestModel != nil && strings.Contains(strings.ToLower(*latestModel), qLower) {
				match = true
			}
			if !match {
				continue
			}
		}
		entry := map[string]any{
			"request_count": requestCount,
			"ok_count":      okCount,
			"fail_count":    failCount,
			"last_activity": lastActivity.UTC().Format(time.RFC3339),
		}
		if taskID != nil {
			entry["task_id"] = *taskID
		}
		if sessionID != nil {
			entry["session_id"] = *sessionID
		}
		if latestModel != nil {
			entry["latest_model"] = *latestModel
		}
		if latestKeyPrefix != nil {
			entry["latest_key_prefix"] = *latestKeyPrefix
		}
		sessions = append(sessions, entry)
	}
	if sessions == nil {
		sessions = []map[string]any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions, "hours": hours})
}

// handleMemoraContext returns the L1 Memora memories stored for a
// specific task, alongside basic request metadata.
func (h *Handler) handleMemoraContext(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	taskID := strings.TrimPrefix(r.URL.Path, "/api/system/memora-context/")
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		writeError(w, http.StatusBadRequest, "task_id required in URL path")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	var apiKeyID *int64
	var requestCount int
	var latestModel *string
	err := h.db.QueryRow(ctx, `
		SELECT
			(SELECT api_key_id FROM request_logs
			 WHERE gw_task_id = $1 ORDER BY ts DESC LIMIT 1),
			COUNT(*),
			(SELECT client_model FROM request_logs
			 WHERE gw_task_id = $1 ORDER BY ts DESC LIMIT 1)
		FROM request_logs
		WHERE gw_task_id = $1
	`, taskID).Scan(&apiKeyID, &requestCount, &latestModel)
	if err != nil {
		writeError(w, http.StatusNotFound, "task not found: "+taskID)
		return
	}

	var userID string
	if apiKeyID != nil {
		userID = fmt.Sprintf("k:%d:%s", *apiKeyID, taskID)
	}

	var facts []map[string]any
	if h.memoraClient != nil && !h.memoraClient.Disabled() && userID != "" {
		if mc, ok := h.memoraClient.(interface {
			Search(ctx context.Context, userID, query string, topK int) ([]memora.Memory, error)
		}); ok {
			searchCtx, searchCancel := context.WithTimeout(ctx, 5*time.Second)
			memories, searchErr := mc.Search(searchCtx, userID, "", 20)
			searchCancel()
			if searchErr == nil {
				for _, m := range memories {
					facts = append(facts, map[string]any{
						"id":     m.ID,
						"memory": m.Text,
						"score":  m.Score,
						"tags":   m.Tags,
					})
				}
			}
		}
	}
	if facts == nil {
		facts = []map[string]any{}
	}

	resp := map[string]any{
		"task_id":       taskID,
		"user_id":       userID,
		"request_count": requestCount,
		"facts":         facts,
	}
	if latestModel != nil {
		resp["latest_model"] = *latestModel
	}
	writeJSON(w, http.StatusOK, resp)
}
