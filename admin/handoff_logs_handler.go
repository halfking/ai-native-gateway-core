package admin

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// HandoffLogsHandler — read-only admin endpoints over the handoff_logs table
// (DB schema in db/migrations/354_handoff_schema_fix.sql + 356_handoff_enhanced.sql).
//
// These endpoints are intended for the operator/admin UI in
// web/src/views/session-context/ + the ModulesView's handoff tab. They are
// deliberately separate from the POST /api/admin/session-handoff endpoint
// (admin/session_compare.go:HandleHandoff) which triggers a *manual* handoff.
//
// Routes (registered in cmd/gateway/main.go):
//
//	GET /api/admin/handoff/logs              ?tenant=&session=&trigger=&limit=&offset=
//	GET /api/admin/handoff/logs/{id}         one row by id
//	GET /api/admin/handoff/stats             ?tenant=&days=    aggregate counts per trigger_reason
//
// All endpoints respect RLS via withTenantTx (see admin/session_compare.go
// for the existing pattern). super_admin can pass ?tenant=* to bypass.
type HandoffLogsHandler struct {
	db *pgxpool.Pool
}

// NewHandoffLogsHandler builds the handler.
func NewHandoffLogsHandler(db *pgxpool.Pool) *HandoffLogsHandler {
	return &HandoffLogsHandler{db: db}
}

// RegisterRoutes wires the endpoints onto the supplied mux.
//
// The wrap callback should be the project's auth middleware (admin/admin or
// superAdmin/superAdmin depending on caller preference). When nil, the routes
// are registered raw (only useful in tests).
func (h *HandoffLogsHandler) RegisterRoutes(mux *http.ServeMux, wrap func(http.HandlerFunc) http.HandlerFunc) {
	h.registerHandoffLogRoutes(mux, wrap)
}

// registerHandoffLogRoutes wires the endpoints onto the supplied mux.
func (h *HandoffLogsHandler) registerHandoffLogRoutes(mux *http.ServeMux, wrap func(http.HandlerFunc) http.HandlerFunc) {
	if wrap == nil {
		wrap = func(f http.HandlerFunc) http.HandlerFunc { return f }
	}
	mux.HandleFunc("/api/admin/handoff/logs", wrap(h.handleList))
	mux.HandleFunc("/api/admin/handoff/logs/", wrap(h.handleSubrouter))
	mux.HandleFunc("/api/admin/handoff/stats", wrap(h.handleStats))
}

// handleSubrouter dispatches /api/admin/handoff/logs/{id}.
func (h *HandoffLogsHandler) handleSubrouter(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/admin/handoff/logs/")
	if rest == "" || strings.Contains(rest, "/") {
		writeError(w, http.StatusNotFound, "unknown handoff log endpoint")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	id, err := strconv.Atoi(rest)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "id must be a positive integer")
		return
	}
	h.handleGet(w, r, id)
}

// handleList returns a paginated list of handoff log rows, newest first.
func (h *HandoffLogsHandler) handleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	q := r.URL.Query()
	tenantFilter := q.Get("tenant")
	sessionFilter := q.Get("session")
	triggerFilter := q.Get("trigger")
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	offset, _ := strconv.Atoi(q.Get("offset"))
	if offset < 0 {
		offset = 0
	}

	// Build WHERE clause from filters.
	where := []string{}
	args := []any{}
	idx := 1
	if tenantFilter != "" && tenantFilter != "*" {
		where = append(where, "tenant_id = $"+strconv.Itoa(idx))
		args = append(args, tenantFilter)
		idx++
	}
	if sessionFilter != "" {
		where = append(where, "session_id = $"+strconv.Itoa(idx))
		args = append(args, sessionFilter)
		idx++
	}
	if triggerFilter != "" {
		where = append(where, "trigger_reason LIKE $"+strconv.Itoa(idx))
		args = append(args, triggerFilter+"%")
		idx++
	}
	whereSQL := ""
	if len(where) > 0 {
		whereSQL = "WHERE " + strings.Join(where, " AND ")
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	list := func(tx pgxQueryer) error {
		rows, err := tx.Query(ctx, `
			SELECT id, session_id, tenant_id, trigger_reason, trigger_mode,
			       tokens_at_handoff, context_window, tokens_in_session,
			       messages_in_session, summary_engine, skill_name, duration_ms,
			       new_session_id, created_at,
			       LEFT(COALESCE(summary_text, ''), 200) AS summary_preview,
			       LEFT(COALESCE(handoff_prompt, ''), 200) AS prompt_preview
			  FROM handoff_logs
			  `+whereSQL+`
			 ORDER BY created_at DESC
			 LIMIT `+strconv.Itoa(limit)+` OFFSET `+strconv.Itoa(offset)+`
		`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var (
				id, tokensAtHandoff, contextWindow, tokensInSess, messagesInSess, durationMs int
				sessionID, tenantID, triggerReason, triggerMode, summaryEngine, skillName    string
				newSessionID                                                                 *string
				createdAt                                                                    time.Time
				summaryPreview, promptPreview                                                string
			)
			if err := rows.Scan(&id, &sessionID, &tenantID, &triggerReason, &triggerMode,
				&tokensAtHandoff, &contextWindow, &tokensInSess, &messagesInSess,
				&summaryEngine, &skillName, &durationMs, &newSessionID, &createdAt,
				&summaryPreview, &promptPreview); err != nil {
				return err
			}
			row := map[string]any{
				"id":                  id,
				"session_id":          sessionID,
				"tenant_id":           tenantID,
				"trigger_reason":      triggerReason,
				"trigger_mode":        triggerMode,
				"tokens_at_handoff":   tokensAtHandoff,
				"context_window":      contextWindow,
				"tokens_in_session":   tokensInSess,
				"messages_in_session": messagesInSess,
				"summary_engine":      summaryEngine,
				"skill_name":          skillName,
				"duration_ms":         durationMs,
				"new_session_id":      stringPtrToString(newSessionID),
				"created_at":          createdAt.UTC(),
				"summary_preview":     summaryPreview,
				"prompt_preview":      promptPreview,
			}
			out = append(out, row)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"items":  out,
			"limit":  limit,
			"offset": offset,
		})
		return nil
	}

	if tenantFilter == "*" && IsSuperAdminOrLegacy(r) {
		if err := list(h.db); err != nil {
			writeError(w, http.StatusInternalServerError, "list failed: "+err.Error())
		}
		return
	}
	if err := withTenantTx(ctx, h.db, EffectiveTenantID(r), func(tx pgx.Tx) error {
		return list(tx)
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "list failed: "+err.Error())
	}
}

// handleGet returns one handoff log row by id (full summary + prompt).
func (h *HandoffLogsHandler) handleGet(w http.ResponseWriter, r *http.Request, id int) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	run := func(tx pgxQueryer, queryRow func(ctx context.Context, sql string, args ...any) pgx.Row) error {
		var (
			outID, tokensAtHandoff, contextWindow, tokensInSess, messagesInSess, durationMs int
			sessionID, tenantID, triggerReason, triggerMode, summaryEngine, skillName       string
			newSessionID                                                                    *string
			createdAt                                                                       time.Time
			summaryText, handoffPrompt                                                      *string
		)
		row := queryRow(ctx, `
			SELECT id, session_id, tenant_id, trigger_reason, trigger_mode,
			       tokens_at_handoff, context_window, tokens_in_session,
			       messages_in_session, summary_engine, skill_name, duration_ms,
			       new_session_id, created_at, summary_text, handoff_prompt
			  FROM handoff_logs
			 WHERE id = $1
		`, id)
		if err := row.Scan(&outID, &sessionID, &tenantID, &triggerReason, &triggerMode,
			&tokensAtHandoff, &contextWindow, &tokensInSess, &messagesInSess,
			&summaryEngine, &skillName, &durationMs, &newSessionID, &createdAt,
			&summaryText, &handoffPrompt); err != nil {
			if err == pgx.ErrNoRows {
				writeError(w, http.StatusNotFound, "handoff log not found")
				return nil
			}
			return err
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id":                  outID,
			"session_id":          sessionID,
			"tenant_id":           tenantID,
			"trigger_reason":      triggerReason,
			"trigger_mode":        triggerMode,
			"tokens_at_handoff":   tokensAtHandoff,
			"context_window":      contextWindow,
			"tokens_in_session":   tokensInSess,
			"messages_in_session": messagesInSess,
			"summary_engine":      summaryEngine,
			"skill_name":          skillName,
			"duration_ms":         durationMs,
			"new_session_id":      stringPtrToString(newSessionID),
			"created_at":          createdAt.UTC(),
			"summary_text":        stringPtrToString(summaryText),
			"handoff_prompt":      stringPtrToString(handoffPrompt),
		})
		return nil
	}

	// super_admin bypass (no RLS) — use the pool directly.
	if IsSuperAdminOrLegacy(r) {
		if err := run(h.db, h.db.QueryRow); err != nil {
			writeError(w, http.StatusInternalServerError, "get failed: "+err.Error())
		}
		return
	}

	// tenant-scoped path uses withTenantTx so RLS is applied.
	if err := withTenantTx(ctx, h.db, EffectiveTenantID(r), func(tx pgx.Tx) error {
		return run(tx, tx.QueryRow)
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "get failed: "+err.Error())
	}
}

// handleStats returns per-trigger-reason aggregate counts and trigger counts
// per day for the last `days` days (default 7).
func (h *HandoffLogsHandler) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	q := r.URL.Query()
	tenantFilter := q.Get("tenant")
	days, _ := strconv.Atoi(q.Get("days"))
	if days <= 0 || days > 90 {
		days = 7
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	stats := func(tx pgxQueryer) error {
		// 1. counts per trigger_reason.
		reasonRows, err := tx.Query(ctx, `
			SELECT trigger_reason, COUNT(*) AS n
			  FROM handoff_logs
			 WHERE created_at > NOW() - ($1 || ' days')::interval
			 GROUP BY trigger_reason
			 ORDER BY n DESC
		`, strconv.Itoa(days))
		if err != nil {
			return err
		}
		defer reasonRows.Close()
		reasons := []map[string]any{}
		for reasonRows.Next() {
			var reason string
			var n int
			if err := reasonRows.Scan(&reason, &n); err != nil {
				return err
			}
			reasons = append(reasons, map[string]any{"trigger_reason": reason, "count": n})
		}
		if err := reasonRows.Err(); err != nil {
			return err
		}

		// 2. counts per day.
		dayRows, err := tx.Query(ctx, `
			SELECT TO_CHAR(DATE_TRUNC('day', created_at), 'YYYY-MM-DD') AS d, COUNT(*)
			  FROM handoff_logs
			 WHERE created_at > NOW() - ($1 || ' days')::interval
			 GROUP BY 1
			 ORDER BY 1
		`, strconv.Itoa(days))
		if err != nil {
			return err
		}
		defer dayRows.Close()
		daily := []map[string]any{}
		for dayRows.Next() {
			var d string
			var n int
			if err := dayRows.Scan(&d, &n); err != nil {
				return err
			}
			daily = append(daily, map[string]any{"date": d, "count": n})
		}
		if err := dayRows.Err(); err != nil {
			return err
		}

		// 3. top 10 sessions by handoff count.
		topRows, err := tx.Query(ctx, `
			SELECT session_id, COUNT(*) AS n
			  FROM handoff_logs
			 WHERE created_at > NOW() - ($1 || ' days')::interval
			 GROUP BY session_id
			 ORDER BY n DESC
			 LIMIT 10
		`, strconv.Itoa(days))
		if err != nil {
			return err
		}
		defer topRows.Close()
		topSessions := []map[string]any{}
		for topRows.Next() {
			var sid string
			var n int
			if err := topRows.Scan(&sid, &n); err != nil {
				return err
			}
			topSessions = append(topSessions, map[string]any{"session_id": sid, "count": n})
		}
		if err := topRows.Err(); err != nil {
			return err
		}

		// 4. summary engine distribution.
		engineRows, err := tx.Query(ctx, `
			SELECT COALESCE(summary_engine, 'unknown') AS engine, COUNT(*)
			  FROM handoff_logs
			 WHERE created_at > NOW() - ($1 || ' days')::interval
			 GROUP BY 1
		`, strconv.Itoa(days))
		if err != nil {
			return err
		}
		defer engineRows.Close()
		engines := []map[string]any{}
		for engineRows.Next() {
			var engine string
			var n int
			if err := engineRows.Scan(&engine, &n); err != nil {
				return err
			}
			engines = append(engines, map[string]any{"engine": engine, "count": n})
		}
		if err := engineRows.Err(); err != nil {
			return err
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"days":         days,
			"by_reason":    reasons,
			"by_day":       daily,
			"top_sessions": topSessions,
			"by_engine":    engines,
		})
		return nil
	}

	if tenantFilter == "*" && IsSuperAdminOrLegacy(r) {
		if err := stats(h.db); err != nil {
			writeError(w, http.StatusInternalServerError, "stats failed: "+err.Error())
		}
		return
	}
	if err := withTenantTx(ctx, h.db, EffectiveTenantID(r), func(tx pgx.Tx) error {
		return stats(tx)
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "stats failed: "+err.Error())
	}
}

// stringPtrToString safely dereferences a *string for JSON output.
func stringPtrToString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
