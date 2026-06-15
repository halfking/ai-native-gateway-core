package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"
)

type auditLogEntry struct {
	ID         int64     `json:"id"`
	TS         time.Time `json:"ts"`
	Actor      string    `json:"actor"`
	Action     string    `json:"action"`
	TargetType *string   `json:"target_type,omitempty"`
	TargetID   *int64    `json:"target_id,omitempty"`
	BeforeJSON any       `json:"before_json,omitempty"`
	AfterJSON  any       `json:"after_json,omitempty"`
}

// handleListAuditLogs returns paginated audit log entries (super_admin only).
// Query params:
//   page (default 1), size (default 50, max 200)
//   actor (LIKE), action (LIKE), from/to (RFC3339 timestamp)
func (h *Handler) handleListAuditLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	page, _ := strconv.Atoi(queryString(r, "page"))
	if page < 1 {
		page = 1
	}
	size, _ := strconv.Atoi(queryString(r, "size"))
	if size < 1 {
		size = 50
	}
	if size > 200 {
		size = 200
	}
	offset := (page - 1) * size

	actor := queryString(r, "actor")
	action := queryString(r, "action")
	from := queryString(r, "from")
	to := queryString(r, "to")

	// Build dynamic WHERE
	where := "1=1"
	args := []any{}
	idx := 1
	if actor != "" {
		where += " AND actor ILIKE $" + strconv.Itoa(idx)
		args = append(args, "%"+actor+"%")
		idx++
	}
	if action != "" {
		where += " AND action ILIKE $" + strconv.Itoa(idx)
		args = append(args, "%"+action+"%")
		idx++
	}
	if from != "" {
		t, err := time.Parse(time.RFC3339, from)
		if err == nil {
			where += " AND ts >= $" + strconv.Itoa(idx)
			args = append(args, t)
			idx++
		}
	}
	if to != "" {
		t, err := time.Parse(time.RFC3339, to)
		if err == nil {
			where += " AND ts <= $" + strconv.Itoa(idx)
			args = append(args, t)
			idx++
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// Total count for pagination
	var total int
	if err := h.db.QueryRow(ctx, "SELECT COUNT(*) FROM routing_audit_log WHERE "+where, args...).Scan(&total); err != nil {
		writeError(w, http.StatusInternalServerError, "count failed: "+err.Error())
		return
	}

	// Page rows
	rows, err := h.db.Query(ctx, `
		SELECT id, ts, COALESCE(actor,''), COALESCE(action,''),
		       target_type, target_id, before_json, after_json
		FROM routing_audit_log
		WHERE `+where+`
		ORDER BY ts DESC
		LIMIT $`+strconv.Itoa(idx)+` OFFSET $`+strconv.Itoa(idx+1),
		append(args, size, offset)...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	defer rows.Close()

	entries := make([]auditLogEntry, 0)
	for rows.Next() {
		var e auditLogEntry
		var beforeJSON, afterJSON []byte
		if err := rows.Scan(&e.ID, &e.TS, &e.Actor, &e.Action, &e.TargetType, &e.TargetID, &beforeJSON, &afterJSON); err != nil {
			continue
		}
		if len(beforeJSON) > 0 {
			_ = json.Unmarshal(beforeJSON, &e.BeforeJSON)
		}
		if len(afterJSON) > 0 {
			_ = json.Unmarshal(afterJSON, &e.AfterJSON)
		}
		entries = append(entries, e)
	}
	if entries == nil {
		entries = []auditLogEntry{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"total":   total,
		"page":    page,
		"size":    size,
		"entries": entries,
	})
}
