package admin

import (
	"context"
	"net/http"
	"strconv"
	"time"
)

// handleSessionCrosstalkCheck returns sessions that have been accessed by
// multiple tenants (cross-talk / 串话).
//
// GET /api/admin/session-crosstalk?hours=24
//
// Returns sessions where tenant_id starts with "CONFLICT:" indicating
// the session was used by multiple tenants.
func (h *Handler) handleSessionCrosstalkCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	hoursStr := r.URL.Query().Get("hours")
	hours := 24
	if hoursStr != "" {
		if h, err := strconv.Atoi(hoursStr); err == nil && h > 0 && h <= 720 {
			hours = h
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	type conflict struct {
		SessionID    string `json:"gw_session_id"`
		TenantIDs    string `json:"tenant_ids"` // "CONFLICT:t1,t2"
		FirstSeenAt  string `json:"first_seen_at"`
		LastSeenAt   string `json:"last_seen_at"`
		RequestCount int    `json:"request_count"`
	}

	rows, err := h.db.Query(ctx, `
		SELECT gw_session_id, tenant_id, first_seen_at, last_seen_at, request_count
		FROM session_tenant_binding
		WHERE tenant_id LIKE 'CONFLICT:%'
		  AND last_seen_at >= NOW() - INTERVAL '1 hour' * $1
		ORDER BY last_seen_at DESC
		LIMIT 100
	`, hours)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	defer rows.Close()

	var conflicts []conflict
	for rows.Next() {
		var c conflict
		var firstSeen, lastSeen time.Time
		if err := rows.Scan(&c.SessionID, &c.TenantIDs, &firstSeen, &lastSeen, &c.RequestCount); err != nil {
			continue
		}
		c.FirstSeenAt = firstSeen.Format(time.RFC3339)
		c.LastSeenAt = lastSeen.Format(time.RFC3339)
		conflicts = append(conflicts, c)
	}

	if conflicts == nil {
		conflicts = []conflict{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"conflicts": conflicts,
		"count":     len(conflicts),
		"hours":     hours,
	})
}
