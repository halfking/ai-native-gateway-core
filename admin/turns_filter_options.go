// Package admin — turns 页热门筛选条件端点（2026-08-10 / 2026-08-21 api_keys）。
//
// GET /api/admin/turns/sessions/filter-options
//
//	返回轮次列表页各筛选框可选值（近 30 天热门），含 api_keys。
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

type TurnsAPIKeyOption struct {
	ID    int64  `json:"id"`
	Label string `json:"label"`
}

type TurnsFilterOptionsResponse struct {
	Projects    []string            `json:"projects"`
	Tasks       []string            `json:"tasks"`
	Owners      []string            `json:"owners"`
	Clients     []string            `json:"clients"`
	Tags        []string            `json:"tags"`
	Models      []string            `json:"models"`
	Providers   []string            `json:"providers"`
	StatusCodes []string            `json:"status_codes"`
	APIKeys     []TurnsAPIKeyOption `json:"api_keys"`
}

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

	resp := &TurnsFilterOptionsResponse{APIKeys: []TurnsAPIKeyOption{}}
	joinDim := " JOIN session_dim sd ON sd.gw_session_id = ss.session_key AND sd.tenant_id = ss.tenant_id"
	windowCond := fmt.Sprintf("ss.first_request_at > NOW() - INTERVAL '%d days'", turnsFilterOptionsWindowDays)
	ssTargets := []*[]string{&resp.Projects, &resp.Tasks, &resp.Owners, &resp.Clients, &resp.Tags}
	ssQueries := []string{
		h.lastActiveQuery(h.lastActiveSource(
			"COALESCE(NULLIF(ss.gw_project_id, ''), sd.project_id)", joinDim,
			windowCond+" AND COALESCE(NULLIF(ss.gw_project_id, ''), sd.project_id) IS NOT NULL AND COALESCE(NULLIF(ss.gw_project_id, ''), sd.project_id) != ''", tenantWhere)),
		h.lastActiveQuery(h.lastActiveSource(
			"sd.task_id", joinDim, windowCond+" AND sd.task_id IS NOT NULL AND sd.task_id != ''", tenantWhere)),
		h.lastActiveQuery(h.lastActiveSource(
			"sd.owner_user", joinDim, windowCond+" AND sd.owner_user IS NOT NULL AND sd.owner_user != ''", tenantWhere)),
		"SELECT v FROM (" +
			h.lastActiveSource("sd.client_id", joinDim, windowCond+" AND sd.client_id IS NOT NULL AND sd.client_id != ''", tenantWhere) +
			" UNION " +
			h.lastActiveSource("sd.application_code", joinDim, windowCond+" AND sd.application_code IS NOT NULL AND sd.application_code != ''", tenantWhere) +
			") t ORDER BY last_at DESC LIMIT " + fmt.Sprint(turnsFilterOptionsLimit),
		h.lastActiveQuery(h.lastActiveSource("tag", ", LATERAL UNNEST(ss.user_tags) AS tag", windowCond, tenantWhere)),
	}
	if err := h.runFilterOptionQueries(ctx, ssQueries, tenantArgs, ssTargets); err != nil {
		writeError(w, http.StatusInternalServerError, "query filter options failed")
		return
	}

	turnTenantWhere := ""
	if tenantID != "" {
		turnTenantWhere = " AND t.tenant_id = $1"
	}
	turnTargets := []*[]string{&resp.Models, &resp.Providers, &resp.StatusCodes}
	turnQueries := []string{
		h.lastActiveTurnQuery("t.model", "t.model IS NOT NULL AND t.model != ''", turnTenantWhere),
		h.lastActiveTurnQuery("t.provider", "t.provider IS NOT NULL AND t.provider != ''", turnTenantWhere),
		h.lastActiveTurnQuery("t.status_code::text", "t.status_code IS NOT NULL", turnTenantWhere),
	}
	if err := h.runFilterOptionQueries(ctx, turnQueries, tenantArgs, turnTargets); err != nil {
		writeError(w, http.StatusInternalServerError, "query filter options failed")
		return
	}

	apiKeys, err := h.queryAPIKeyFilterOptions(ctx, tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query filter options failed")
		return
	}
	resp.APIKeys = apiKeys
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) queryAPIKeyFilterOptions(ctx context.Context, tenantID string) ([]TurnsAPIKeyOption, error) {
	tenantWhere := ""
	args := []any{}
	if tenantID != "" {
		tenantWhere = " AND t.tenant_id = $1"
		args = []any{tenantID}
	}
	q := fmt.Sprintf(`
		SELECT id, label FROM (
			SELECT rl.api_key_id AS id,
				COALESCE(NULLIF(ak.key_alias, ''), ak.key_prefix, 'key#' || rl.api_key_id::text) AS label,
				MAX(t.ts) AS last_at
			FROM public.session_turns_with_current_month t
			JOIN public.request_logs_with_current_month rl ON rl.request_id = t.request_id
			LEFT JOIN public.api_keys ak ON ak.id = rl.api_key_id
			WHERE rl.api_key_id IS NOT NULL
			  AND t.ts > NOW() - INTERVAL '%d days'%s
			GROUP BY 1, 2
		) x ORDER BY last_at DESC LIMIT %d`,
		turnsFilterOptionsWindowDays, tenantWhere, turnsFilterOptionsLimit)
	rows, err := h.db.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query api_keys filter options: %w", err)
	}
	defer rows.Close()
	out := []TurnsAPIKeyOption{}
	for rows.Next() {
		var opt TurnsAPIKeyOption
		if err := rows.Scan(&opt.ID, &opt.Label); err != nil {
			return nil, fmt.Errorf("scan api_keys filter options: %w", err)
		}
		out = append(out, opt)
	}
	return out, rows.Err()
}

func (h *Handler) lastActiveSource(valueSel, fromSuffix, cond, tenantWhere string) string {
	return fmt.Sprintf(
		"SELECT %s AS v, MAX(ss.first_request_at) AS last_at FROM session_summaries ss%s WHERE %s%s GROUP BY 1",
		valueSel, fromSuffix, cond, tenantWhere)
}

func (h *Handler) lastActiveQuery(source string) string {
	return "SELECT v FROM (" + source + ") t ORDER BY last_at DESC LIMIT " + fmt.Sprint(turnsFilterOptionsLimit)
}

func (h *Handler) lastActiveTurnQuery(valueExpr, filter, tenantWhere string) string {
	return fmt.Sprintf(
		"SELECT v FROM (SELECT %s AS v, MAX(t.ts) AS last_at FROM public.session_turns_with_current_month t"+
			" WHERE %s AND t.ts > NOW() - INTERVAL '%d days'%s GROUP BY 1)"+
			" t2 ORDER BY last_at DESC LIMIT %d",
		valueExpr, filter, turnsFilterOptionsWindowDays, tenantWhere, turnsFilterOptionsLimit)
}

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
