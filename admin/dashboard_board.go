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
	days := boardDays(r)
	filterTenant := r.URL.Query().Get("tenant_id")
	if filterTenant == "" {
		filterTenant = EffectiveTenantIDAll(r)
	}
	providerID, _ := strconv.ParseInt(r.URL.Query().Get("provider_id"), 10, 64)

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	scope := boardScopeForTenant(filterTenant)

	if h.boardCache != nil {
		payload, err := h.boardCache.GetOrRebuild(ctx, scope, days, providerID)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "board stats cache: "+err.Error())
			return
		}
		if payload["source"] == nil {
			payload["source"] = "redis_baseline_delta"
		}
		payload["days"] = days
		attachBoardOperational(ctx, h, payload)
		writeJSON(w, http.StatusOK, payload)
		return
	}

	// Redis unavailable — degraded path (dev/single-node without Redis only).
	summary, fromMinute := h.queryBoardSummary(ctx, filterTenant, days)
	if !fromMinute {
		summary = h.fallbackBoardSummary(ctx, filterTenant, days)
	}

	pies, _ := h.queryBoardPies(ctx, filterTenant, days)
	trends, _ := h.queryBoardTrends(ctx, filterTenant, days, providerID)
	bgTasks := h.queryBoardBackgroundTasks(ctx)
	selfcheck := h.queryBoardSelfCheck(ctx)

	writeJSON(w, http.StatusOK, map[string]any{
		"summary":          summary,
		"pies":             pies,
		"trends":           trends,
		"background_tasks": bgTasks,
		"selfcheck":        selfcheck,
		"days":             days,
		"source":           boardSource(fromMinute) + "_degraded_no_redis",
	})
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
	days := queryInt(r, "days", 7)
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
	return "request_logs_hot"
}

func boardTenantClause(tenantID string, startArg int) (clause string, args []any) {
	args = append(args, tenantID)
	if tenantID == "" {
		return "", nil
	}
	return " AND tenant_id = $" + strconv.Itoa(startArg), args
}
