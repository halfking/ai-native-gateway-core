// Package admin — turns 页热门筛选条件端点（2026-08-10）。
//
// GET /api/admin/turns/sessions/filter-options
//
//	返回轮次列表页（TurnsListView.vue）各筛选框的可选值 —— 基于近 30 天实际使用
//	数据聚合出的"热门条件"，供前端可检索下拉填充。数据不硬编码，新项目/任务/
//	用户/客户端/标签/模型接入后自动出现。
//
//	维度及来源：
//	  projects     ss.gw_project_id（session_summaries）
//	  tasks        sd.task_id（session_summaries JOIN session_dim）
//	  owners       sd.owner_user
//	  clients      sd.client_id ∪ sd.application_code
//	  tags         UNNEST(ss.user_tags)
//	  models       gateway.session_turns.model
//	  providers    gateway.session_turns.provider
//	  status_codes gateway.session_turns.status_code
//
//	每个维度按最近活跃（MAX(first_request_at / t.ts)）倒序取前 20。
//	鉴权：admin() 中间件；tenant_admin 仅看到本租户。
//	返回：{ projects, tasks, owners, clients, tags, models, providers, status_codes }
package admin

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

const (
	turnsFilterOptionsWindowDays = 30
	turnsFilterOptionsLimit      = 20
)

// TurnsFilterOptionsResponse 是各筛选维度的热门可选值。
type TurnsFilterOptionsResponse struct {
	Projects    []string `json:"projects"`
	Tasks       []string `json:"tasks"`
	Owners      []string `json:"owners"`
	Clients     []string `json:"clients"`
	Tags        []string `json:"tags"`
	Models      []string `json:"models"`
	Providers   []string `json:"providers"`
	StatusCodes []string `json:"status_codes"`
}

// handleTurnsFilterOptions 处理 GET /api/admin/turns/sessions/filter-options。
func (h *Handler) handleTurnsFilterOptions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()

	tenantID := ""
	if IsTenantAdmin(r) {
		tenantID = GetTenantID(r)
	} else {
		tenantID = tenantFromQueryOrContext(r)
	}

	tenantWhere := ""
	tenantArgs := []any{}
	if tenantID != "" {
		tenantWhere = " AND ss.tenant_id = $1"
		tenantArgs = []any{tenantID}
	}

	resp := &TurnsFilterOptionsResponse{}

	// 基于 session_summaries (+ session_dim) 的维度
	joinDim := " JOIN session_dim sd ON sd.gw_session_id = ss.session_key AND sd.tenant_id = ss.tenant_id"
	ssTargets := []*[]string{
		&resp.Projects, &resp.Tasks, &resp.Owners, &resp.Clients, &resp.Tags,
	}
	ssQueries := []string{
		// projects
		h.lastActiveQuery(h.lastActiveSource(
			"ss.gw_project_id", "", "ss.gw_project_id IS NOT NULL AND ss.gw_project_id != ''", tenantWhere)),
		// tasks
		h.lastActiveQuery(h.lastActiveSource(
			"sd.task_id", joinDim, "sd.task_id IS NOT NULL AND sd.task_id != ''", tenantWhere)),
		// owners
		h.lastActiveQuery(h.lastActiveSource(
			"sd.owner_user", joinDim, "sd.owner_user IS NOT NULL AND sd.owner_user != ''", tenantWhere)),
		// clients（client_id ∪ application_code）
		"SELECT v FROM (" +
			h.lastActiveSource(
				"sd.client_id", joinDim, "sd.client_id IS NOT NULL AND sd.client_id != ''", tenantWhere) +
			" UNION " +
			h.lastActiveSource(
				"sd.application_code", joinDim, "sd.application_code IS NOT NULL AND sd.application_code != ''", tenantWhere) +
			") t ORDER BY last_at DESC LIMIT " + fmt.Sprint(turnsFilterOptionsLimit),
		// tags
		h.lastActiveQuery(h.lastActiveSource(
			"tag", ", LATERAL UNNEST(ss.user_tags) AS tag", "1=1", tenantWhere)),
	}
	if err := h.runFilterOptionQueries(ctx, ssQueries, tenantArgs, ssTargets); err != nil {
		writeError(w, http.StatusInternalServerError, "query filter options failed")
		return
	}

	// 基于 session_turns 的维度
	turnTenantWhere := ""
	if tenantID != "" {
		turnTenantWhere = " AND t.tenant_id = $1"
	}
	turnTargets := []*[]string{
		&resp.Models, &resp.Providers, &resp.StatusCodes,
	}
	turnQueries := []string{
		h.lastActiveTurnQuery("t.model", "t.model IS NOT NULL AND t.model != ''", turnTenantWhere),
		h.lastActiveTurnQuery("t.provider", "t.provider IS NOT NULL AND t.provider != ''", turnTenantWhere),
		h.lastActiveTurnQuery("t.status_code::text", "t.status_code IS NOT NULL", turnTenantWhere),
	}
	if err := h.runFilterOptionQueries(ctx, turnQueries, tenantArgs, turnTargets); err != nil {
		writeError(w, http.StatusInternalServerError, "query filter options failed")
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

// lastActiveSource 组装基于 session_summaries 的热门值内层子查询：
//
//	SELECT <valueSel> AS v, MAX(ss.first_request_at) AS last_at
//	FROM session_summaries ss<fromSuffix> WHERE <cond><tenantWhere> GROUP BY 1
func (h *Handler) lastActiveSource(valueSel, fromSuffix, cond, tenantWhere string) string {
	return fmt.Sprintf(
		"SELECT %s AS v, MAX(ss.first_request_at) AS last_at FROM session_summaries ss%s WHERE %s%s GROUP BY 1",
		valueSel, fromSuffix, cond, tenantWhere)
}

// lastActiveQuery 包成外层：SELECT v FROM (<source>) t ORDER BY last_at DESC LIMIT K。
func (h *Handler) lastActiveQuery(source string) string {
	return "SELECT v FROM (" + source + ") t ORDER BY last_at DESC LIMIT " + fmt.Sprint(turnsFilterOptionsLimit)
}

// lastActiveTurnQuery 组装基于 session_turns 的热门值查询。
func (h *Handler) lastActiveTurnQuery(valueExpr, filter, tenantWhere string) string {
	return fmt.Sprintf(
		"SELECT v FROM (SELECT %s AS v, MAX(t.ts) AS last_at FROM gateway.session_turns t"+
			" WHERE %s AND t.ts > NOW() - INTERVAL '%d days'%s GROUP BY 1)"+
			" t2 ORDER BY last_at DESC LIMIT %d",
		valueExpr, filter, turnsFilterOptionsWindowDays, tenantWhere, turnsFilterOptionsLimit)
}

// runFilterOptionQueries 顺序执行一组热门值查询并写入对应目标切片。
func (h *Handler) runFilterOptionQueries(ctx context.Context, queries []string, args []any, targets []*[]string) error {
	if len(queries) != len(targets) {
		return fmt.Errorf("query/target count mismatch: %d vs %d", len(queries), len(targets))
	}
	for i, q := range queries {
		rows, err := h.db.Query(ctx, q, args...)
		if err != nil {
			return fmt.Errorf("query filter options [%d] failed: %w", i, err)
		}
		*targets[i] = []string{}
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				rows.Close()
				return fmt.Errorf("scan filter options [%d] failed: %w", i, err)
			}
			*targets[i] = append(*targets[i], v)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate filter options [%d] failed: %w", i, err)
		}
	}
	return nil
}
