package admin

import (
	"net/http"
	"strconv"
	"strings"
)

// sessionScope mirrors memora-sessions list filters so detail APIs return
// the same rows as the list row the user clicked.
type sessionScope struct {
	Hours     int
	SessionID string // gw_session_id; empty = all sessions for task_id
}

func parseSessionScope(r *http.Request) sessionScope {
	sc := sessionScope{Hours: 24}
	if r == nil {
		return sc
	}
	if v := strings.TrimSpace(r.URL.Query().Get("hours")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 168 {
			sc.Hours = n
		}
	}
	if v := strings.TrimSpace(r.URL.Query().Get("session_id")); v != "" && v != "[空]" {
		sc.SessionID = v
	}
	return sc
}

// sessionLogsWhere builds WHERE clause + args for request_logs scoped like
// memora-sessions topic_sessions (task_id + optional session_id + hours).
// Returns clause starting with "WHERE" and args beginning with taskID.
func sessionLogsWhere(taskID string, sc sessionScope) (clause string, args []any) {
	args = []any{taskID, sc.Hours}
	clause = `WHERE gw_task_id = $1 AND ts > NOW() - INTERVAL '1 hour' * $2`
	if sc.SessionID != "" {
		args = append(args, sc.SessionID)
		clause += ` AND COALESCE(NULLIF(TRIM(gw_session_id), ''), NULL) IS NOT DISTINCT FROM $3`
	}
	return clause, args
}
