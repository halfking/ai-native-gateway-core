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
	filterTenant := r.URL.Query().Get("tenant_id")
	if filterTenant == "" {
		filterTenant = EffectiveTenantIDAll(r)
	}
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
		payload["days"] = days
		writeJSON(w, http.StatusOK, payload)
		return
	}

	// Custom range or Redis unavailable — query PostgreSQL directly.
	summary, fromMinute := h.queryBoardSummary(ctx, filterTenant, tr)
	if !fromMinute {
		summary = h.fallbackBoardSummary(ctx, filterTenant, tr)
	}

	pies, _ := h.resolveBoardPies(ctx, filterTenant, tr)
	trends, _ := h.resolveBoardTrends(ctx, filterTenant, tr, providerID)

	resp := map[string]any{
		"summary": summary,
		"pies":    pies,
		"trends":  trends,
		"days":    days,
		"source":           boardSource(fromMinute) + func() string {
			if tr.Custom {
				return "_custom_range"
			}
			return "_degraded_no_redis"
		}(),
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
		writeError(w, http.StatusInternalServerError, err.Error())
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
