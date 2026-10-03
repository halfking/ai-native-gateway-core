package admin

import (
	"context"
	"net/http"
	"strconv"
	"time"
)

type boardPieItem struct {
	Key      string  `json:"key"`
	Requests int64   `json:"requests"`
	Tokens   int64   `json:"tokens"`
	Credits  int64   `json:"credits"`
	CostUSD  float64 `json:"cost_usd"`
}

type boardTrendPoint struct {
	Bucket   string  `json:"bucket"`
	Requests int64   `json:"requests"`
	Tokens   int64   `json:"tokens"`
	Credits  int64   `json:"credits"`
	CostUSD  float64 `json:"cost_usd"`
}

func (h *Handler) handleDashboardBoard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	tr, rangeErr := boardTimeRangeFromRequest(r)
	if rangeErr != nil {
		writeError(w, http.StatusBadRequest, rangeErr.Error())
		return
	}
	days := tr.Days
	filterTenant := statsTenantScope(r)

	providerID, _ := strconv.ParseInt(r.URL.Query().Get("provider_id"), 10, 64)

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	scope := boardScopeForTenant(filterTenant)

	if h.boardCache != nil && !tr.Custom {
		payload, err := h.boardCache.GetOrRebuild(ctx, scope, days, providerID)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "board stats cache: "+err.Error())
			return
		}
		if payload["source"] == nil {
			payload["source"] = "redis_baseline_delta"
		}
		if summaryMap, ok := payload["summary"].(map[string]any); ok {
			if summary, ok := statsSummaryFromMap(summaryMap); ok {
				h.enqueueStatsShadow("dashboard_board", filterTenant, tr.Start, tr.End, summary)
			}
		}
		payload["days"] = days
		// 2026-07-25: Add body size stats from Redis tracker
		if h.bodySizeTracker != nil {
			if bodyStats, err := h.bodySizeTracker.GetStats(ctx); err == nil {
				payload["body_stats"] = bodyStats
			}
		}
		if includeBoardOperational(r) {
			payload["operational"] = h.boardOperationalPayload(ctx)
		}
		writeJSON(w, http.StatusOK, payload)
		return
	}

	// Custom range or Redis unavailable — query PostgreSQL directly.
	summary, fromMinute := h.queryBoardSummary(ctx, filterTenant, tr)
	if !fromMinute {
		// 2026-10-03：回退失败不再被吞掉。原来这里拿到的是一张
		// 「全 0 且无任何标记」的 summary，页面看起来就是「这个时段没有流量」。
		fb, fbErr := h.fallbackBoardSummary(ctx, filterTenant, tr)
		if fbErr != nil {
			writeInternalErr(w, "board summary fallback failed", fbErr)
			return
		}
		summary = fb
	}

	if !tr.Custom {
		if summary, ok := statsSummaryFromMap(summary); ok {
			h.enqueueStatsShadow("dashboard_board", filterTenant, tr.Start, tr.End, summary)
		}
	}

	// 2026-10-03：pies/trends 的错误原来用 `_` 丢掉。
	// 后果不是「少一块图」，而是**载荷里没有任何痕迹**：pies 变 nil、
	// trends 变 nil，页面把 nil 当空数组渲染，于是「这个维度没有客户端」
	// 与「聚合视图没迁移、算不出来」产出同一张页面。现在两者的错误都
	// 写进载荷的 degraded 字段（见 applyBoardDegradation）。
	pies, piesDegraded, piesErr := h.resolveBoardPies(ctx, filterTenant, tr)
	if piesErr != nil {
		pies = emptyBoardPies()
		piesDegraded = boardPieDegradation{
			Keys:        []string{"clients", "client_ips", "identity_hashes", "models", "errors", "tenants", "providers"},
			MissingView: boardMissingViewName(piesErr),
		}
	}
	trends, trendsErr := h.resolveBoardTrends(ctx, filterTenant, tr, providerID)
	if trendsErr != nil {
		// 显式空数组而非 nil：前端 `?? []` 兜底仍生效，但载荷会带 degraded。
		trends = []boardTrendPoint{}
	}

	resp := map[string]any{
		"summary": summary,
		"pies":    pies,
		"trends":  trends,
		"days":    days,
		"source": boardSource(fromMinute) + func() string {
			if tr.Custom {
				return "_custom_range"
			}
			return "_degraded_no_redis"
		}(),
	}
	applyBoardDegradation(resp, piesDegraded, trendsErr)
	// 2026-07-25: Add body size stats from Redis tracker
	if h.bodySizeTracker != nil {
		if bodyStats, err := h.bodySizeTracker.GetStats(ctx); err == nil {
			resp["body_stats"] = bodyStats
		}
	}
	if includeBoardOperational(r) {
		resp["operational"] = h.boardOperationalPayload(ctx)
	}
	if tr.Custom {
		resp["range_start"] = tr.Start.Format("2006-01-02")
		resp["range_end"] = tr.End.Add(-24 * time.Hour).Format("2006-01-02")
		resp["includes_today"] = tr.includesTodayUTC()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) handleDashboardBoardErrorDrill(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	errorKind := r.URL.Query().Get("error_kind")
	if errorKind == "" {
		writeError(w, http.StatusBadRequest, "error_kind required")
		return
	}
	days := boardDays(r)
	tenantID := r.URL.Query().Get("tenant_id")
	if tenantID == "" {
		tenantID = EffectiveTenantIDAll(r)
	}
	dimension := r.URL.Query().Get("dimension")
	if dimension == "" {
		dimension = "model"
	}

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	scope := boardScopeForTenant(tenantID)
	if h.boardCache != nil {
		items, err := h.boardCache.GetOrRebuildDrill(ctx, scope, days, errorKind, dimension,
			func(c context.Context, tenantFilter string, d int, ek, dim string) ([]map[string]any, error) {
				return h.buildErrorDrillMaps(c, tenantFilter, d, ek, dim)
			})
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "board drill cache: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"error_kind": errorKind,
			"dimension":  dimension,
			"items":      items,
			"source":     "redis",
		})
		return
	}

	items, err := h.queryErrorDrill(ctx, tenantID, days, errorKind, dimension)
	if err != nil {
		writeInternalErr(w, "internal error (see server logs)", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"error_kind": errorKind,
		"dimension":  dimension,
		"items":      items,
	})
}

func boardDays(r *http.Request) int {
	days := queryInt(r, "days", 1)
	if days < 1 {
		days = 1
	}
	if days > 90 {
		days = 90
	}
	return days
}

func boardSource(fromMinute bool) string {
	if fromMinute {
		return "request_stats_minute"
	}
	return "request_logs_with_current_month"
}

func boardTenantClause(tenantID string, startArg int) (clause string, args []any) {
	args = append(args, tenantID)
	if tenantID == "" {
		return "", nil
	}
	return " AND tenant_id = $" + strconv.Itoa(startArg), args
}
