// Package admin - session_list_v2.go
//
// 会话列表 v2 — 把 sessionforensics.Exporter.ListRecentSessions 用 admin 端点暴露。
//
//   GET /api/admin/sessions/list?tenant=...&limit=...
//     返回最近活跃 session 列表（用 sessionforensics.ListRecentSessions 实现）
//
// 仅 super 用户可用。

package admin

import (
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/domains/sessionforensics"
)

// SessionListV2API 用 sessionforensics 实现的会话列表端点。
type SessionListV2API struct {
	pool *pgxpool.Pool
}

// NewSessionListV2API 构造函数。
func NewSessionListV2API(pool *pgxpool.Pool) *SessionListV2API {
	return &SessionListV2API{pool: pool}
}

func (api *SessionListV2API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if api.pool == nil {
		writeExportJSONError(w, http.StatusServiceUnavailable, "session list v2 API requires database")
		return
	}
	if r.URL.Path != "/api/admin/sessions/list" {
		writeExportJSONError(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodGet {
		writeExportJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	tenantID := r.URL.Query().Get("tenant")
	if tenantID == "" {
		tenantID = "default"
	}
	limit := 50
	if s := r.URL.Query().Get("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}

	exp := sessionforensics.NewExporter(api.pool)
	audits, err := exp.ListRecentSessions(r.Context(), tenantID, limit)
	if err != nil {
		writeExportJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeExportJSON(w, http.StatusOK, map[string]any{
		"tenant":   tenantID,
		"limit":    limit,
		"sessions": audits,
	})
}
