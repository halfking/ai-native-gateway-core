// Package admin — Session Clusters API.
//
//	GET  /api/admin/session-clusters        聚类列表（分页+按 tenant）
//	GET  /api/admin/session-clusters/<id>   聚类详情（含成员会话）
//	POST /api/admin/session-clusters/run    手动触发聚类（需要 ClusterRunner）
package admin

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// SessionClusterItem 聚类列表项。
type SessionClusterItem struct {
	ClusterID       string    `json:"cluster_id"`
	TenantID        string    `json:"tenant_id"`
	CoarseKey       *string   `json:"coarse_key,omitempty"`
	Label           *string   `json:"label,omitempty"`
	TopicPath       []string  `json:"topic_path"`
	MemberCount     int       `json:"member_count"`
	AvgCostUSD      float64   `json:"avg_cost_usd"`
	AvgQualityScore *float64  `json:"avg_quality_score,omitempty"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// SessionClusterDetail 聚类详情。
type SessionClusterDetail struct {
	SessionClusterItem
	Members []SessionClusterMemberItem `json:"members"`
}

// SessionClusterMemberItem 聚类成员。
type SessionClusterMemberItem struct {
	GwSessionID string  `json:"gw_session_id"`
	Score       float64 `json:"score"`
	Title       *string `json:"title,omitempty"`
	TotalCost   float64 `json:"total_cost_usd"`
}

// ClusterRunner 手动触发聚类的能力（由 main 注入；nil 时 run 返回 503）。
type ClusterRunner interface {
	ClusterTenant(ctx context.Context, tenantID string, lookbackHours int) (int, error)
}

// clusterRunnerHolder 允许 main 注入 ClusterRunner（避免改 Handler 构造签名）。
var clusterRunnerHolder ClusterRunner

// SetClusterRunner 注入聚类执行器。
func SetClusterRunner(r ClusterRunner) { clusterRunnerHolder = r }

// RouteSessionClusters dispatches /api/admin/session-clusters/<id> and /run.
func (h *Handler) RouteSessionClusters(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/admin/session-clusters/")
	rest = strings.TrimSuffix(rest, "/")
	parts := strings.Split(rest, "/")
	if len(parts) == 0 || parts[0] == "" {
		h.HandleSessionClustersList(w, r)
		return
	}
	if parts[0] == "run" && len(parts) == 1 {
		h.HandleSessionClusterRun(w, r)
		return
	}
	// /<cluster_id>
	h.HandleSessionClusterDetail(w, r)
}

// HandleSessionClustersList GET /api/admin/session-clusters
func (h *Handler) HandleSessionClustersList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "db not available")
		return
	}
	tenantID := EffectiveTenantIDAll(r)
	page := queryInt(r, "page", 1)
	if page < 1 {
		page = 1
	}
	pageSize := queryInt(r, "page_size", 20)
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	query := `SELECT cluster_id, tenant_id, coarse_key, label,
		COALESCE(topic_path,'{}'), member_count, avg_cost_usd, avg_quality_score, updated_at
		FROM session_clusters`
	args := []any{}
	where := " WHERE 1=1"
	if tenantID != "" {
		where += " AND tenant_id = $1"
		args = append(args, tenantID)
	}
	query += where + " ORDER BY member_count DESC, updated_at DESC"
	query += " LIMIT $" + strconv.Itoa(len(args)+1) + " OFFSET $" + strconv.Itoa(len(args)+2)
	args = append(args, pageSize, offset)

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	defer rows.Close()

	items := []SessionClusterItem{}
	for rows.Next() {
		var it SessionClusterItem
		if err := rows.Scan(&it.ClusterID, &it.TenantID, &it.CoarseKey, &it.Label,
			&it.TopicPath, &it.MemberCount, &it.AvgCostUSD, &it.AvgQualityScore, &it.UpdatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed: "+err.Error())
			return
		}
		items = append(items, it)
	}

	var total int
	countQ := "SELECT COUNT(*) FROM session_clusters" + where
	_ = h.db.QueryRow(ctx, countQ, args[:len(args)-2]...).Scan(&total)

	writeJSON(w, http.StatusOK, map[string]any{
		"clusters":  items,
		"page":      page,
		"page_size": pageSize,
		"total":     total,
	})
}

// HandleSessionClusterDetail GET /api/admin/session-clusters/<id>
func (h *Handler) HandleSessionClusterDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "db not available")
		return
	}
	clusterID := strings.TrimPrefix(r.URL.Path, "/api/admin/session-clusters/")
	clusterID = strings.Split(clusterID, "/")[0]
	if clusterID == "" || clusterID == "run" {
		writeError(w, http.StatusBadRequest, "cluster_id is required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	var it SessionClusterItem
	err := h.db.QueryRow(ctx, `SELECT cluster_id, tenant_id, coarse_key, label,
		COALESCE(topic_path,'{}'), member_count, avg_cost_usd, avg_quality_score, updated_at
		FROM session_clusters WHERE cluster_id=$1`, clusterID).Scan(
		&it.ClusterID, &it.TenantID, &it.CoarseKey, &it.Label,
		&it.TopicPath, &it.MemberCount, &it.AvgCostUSD, &it.AvgQualityScore, &it.UpdatedAt)
	if err != nil {
		writeError(w, http.StatusNotFound, "cluster not found")
		return
	}

	// 成员
	rows, err := h.db.Query(ctx, `
		SELECT m.gw_session_id, m.score, ss.title, COALESCE(ss.total_cost_usd,0)
		FROM session_cluster_members m
		LEFT JOIN session_summaries ss ON ss.session_key = m.gw_session_id
		WHERE m.cluster_id=$1 ORDER BY m.score DESC LIMIT 100`, clusterID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "members query failed: "+err.Error())
		return
	}
	defer rows.Close()
	members := []SessionClusterMemberItem{}
	for rows.Next() {
		var m SessionClusterMemberItem
		if err := rows.Scan(&m.GwSessionID, &m.Score, &m.Title, &m.TotalCost); err == nil {
			members = append(members, m)
		}
	}

	writeJSON(w, http.StatusOK, SessionClusterDetail{SessionClusterItem: it, Members: members})
}

// HandleSessionClusterRun POST /api/admin/session-clusters/run
func (h *Handler) HandleSessionClusterRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if RequireSuperAdminForWrite(w, r) {
		return
	}
	if clusterRunnerHolder == nil {
		writeError(w, http.StatusServiceUnavailable, "cluster runner not configured")
		return
	}
	tenantID := EffectiveTenantIDAll(r)
	lookback := queryInt(r, "lookback_hours", 168)
	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()

	tid := tenantID
	if tid == "" {
		tid = "" // 全租户（需 super_admin；已由 RequireSuperAdminForWrite 保证）
	}
	count, err := clusterRunnerHolder.ClusterTenant(ctx, tid, lookback)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cluster run failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":         "ok",
		"clusters_built": count,
	})
}
