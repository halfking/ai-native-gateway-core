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
//	  models       public.session_turns.model
//	  providers    public.session_turns.provider
//	  status_codes public.session_turns.status_code
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

	"golang.org/x/sync/errgroup"
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
		// projects：与会话列表展示/筛选使用同一回退口径
		h.lastActiveQuery(h.lastActiveSource(
			"COALESCE(NULLIF(ss.gw_project_id, ''), sd.project_id)", joinDim,
			"COALESCE(NULLIF(ss.gw_project_id, ''), sd.project_id) IS NOT NULL AND COALESCE(NULLIF(ss.gw_project_id, ''), sd.project_id) != ''", tenantWhere)),
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
//	FROM session_summaries ss<fromSuffix>
//	WHERE <cond> AND ss.first_request_at >= NOW() - INTERVAL '30 days'<tenantWhere>
//	GROUP BY 1
//
// ★ 2026-10-18：这里原本**没有时间窗**，与本文件头「基于近 30 天实际使用数据」
// 和 turns 臂的 30 天窗都不一致——端点内部两个臂口径不同，而前端
// TurnsListView 默认 timePreset='all'，于是 ss 臂给出全历史选项、turns 臂只给
// 近 30 天，同一个下拉框里两套语义。
//
// 经 owner 裁定：**统一为近 30 天**（与头注和 turns 臂一致）。代价如实记录：
// 超过 30 天没用过的 project/task/owner/client/tag 不再出现在下拉里。
//
// 顺带的性能收益（实测，同一 30 天窗口、无并发干扰）：
//   projects  3.92s → 1.36s（−65%）
//   tasks     7.03~9.66s → 3.55s（−60%）
// 原因是 session_summaries 共 776,465 行而近 30 天只有 312,457 行（40%），
// 无窗时每次分组都要扫全量历史。
func (h *Handler) lastActiveSource(valueSel, fromSuffix, cond, tenantWhere string) string {
	return fmt.Sprintf(
		"SELECT %s AS v, MAX(ss.first_request_at) AS last_at FROM session_summaries ss%s"+
			" WHERE %s AND ss.first_request_at >= NOW() - INTERVAL '%d days'%s GROUP BY 1",
		valueSel, fromSuffix, cond, turnsFilterOptionsWindowDays, tenantWhere)
}

// lastActiveQuery 包成外层：SELECT v FROM (<source>) t ORDER BY last_at DESC LIMIT K。
func (h *Handler) lastActiveQuery(source string) string {
	return "SELECT v FROM (" + source + ") t ORDER BY last_at DESC LIMIT " + fmt.Sprint(turnsFilterOptionsLimit)
}

// lastActiveTurnQuery 组装基于 session_turns 的热门值查询。
func (h *Handler) lastActiveTurnQuery(valueExpr, filter, tenantWhere string) string {
	return fmt.Sprintf(
		"SELECT v FROM (SELECT %s AS v, MAX(t.ts) AS last_at FROM public.session_turns_with_current_month t"+
			" WHERE %s AND t.ts > NOW() - INTERVAL '%d days'%s GROUP BY 1)"+
			" t2 ORDER BY last_at DESC LIMIT %d",
		valueExpr, filter, turnsFilterOptionsWindowDays, tenantWhere, turnsFilterOptionsLimit)
}

// runFilterOptionQueries 并发执行一组热门值查询并写入对应目标切片。
//
// ★ 2026-10-18：原本是**顺序 for 循环**，8 条查询共用一个 12s ctx。
// 逐条实测（无并发干扰）需要 ~40s ⇒ 预算结构上不可能成立，端点稳定 500@12s。
//
// 改成 errgroup 并行，墙钟从「各条之和」变成「最慢的一条」。
// ⚠ 但**别照抄 dashboardapi/session_overview.go 的「墙钟=max(段)」就以为必然成立**：
//   那 9 段有空闲核；本组查询是 CPU/缓冲密集的，实测并发时每条自身都变慢
//   （q1 9,971→18,845 ms、q2 7,027→16,349 ms）。实测收益是 **5.3×，不是 9×**。
//
// 并行是本修复的一半，另一半是 lastActiveSource 的 30 天窗（单条再降 ~60%）。
// 两者叠加后 8 条并发，墙钟由最慢单条决定（实测 3.5~6s 量级），落在 12s 内。
//
// 错误语义不变：任一条失败即整体失败，仍带序号报 500。
// 每个 goroutine 写**各自**的 targets[i]，互不重叠，无数据竞争；
// 但先在局部变量里收完再整体赋值，避免半截结果被 errgroup 的提前取消暴露。
func (h *Handler) runFilterOptionQueries(ctx context.Context, queries []string, args []any, targets []*[]string) error {
	if len(queries) != len(targets) {
		return fmt.Errorf("query/target count mismatch: %d vs %d", len(queries), len(targets))
	}
	results := make([][]string, len(queries))
	g, gctx := errgroup.WithContext(ctx)
	for i, q := range queries {
		i, q := i, q
		g.Go(func() error {
			vals, err := h.queryFilterOptionValues(gctx, q, args)
			if err != nil {
				return fmt.Errorf("query filter options [%d] failed: %w", i, err)
			}
			results[i] = vals
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}
	for i := range targets {
		*targets[i] = results[i]
	}
	return nil
}

// queryFilterOptionValues 跑一条热门值查询并把结果收成字符串切片。
func (h *Handler) queryFilterOptionValues(ctx context.Context, query string, args []any) ([]string, error) {
	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	vals := []string{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		vals = append(vals, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return vals, nil
}
