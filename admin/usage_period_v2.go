package admin

import (
	"context"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/metrics"
	"github.com/kaixuan/llm-gateway-go/domains/reconciliation"
	"github.com/kaixuan/llm-gateway-go/settings"
)

type usagePeriodV2 struct {
	PeriodStats
	Start      time.Time `json:"start"`
	End        time.Time `json:"end"`
	Credits    float64   `json:"total_credits_charged"`
	NonUSDRows int64     `json:"cost_rows_outside_usd"`
}

type usageDimensionChangeV2 struct {
	DimensionValue string   `json:"dimension_value"`
	CurrentCost    float64  `json:"current_cost"`
	PreviousCost   float64  `json:"previous_cost"`
	ChangePct      *float64 `json:"change_pct"`
}

func (h *Handler) handleUsagePeriodCompareV2(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	tenant, owner, allowed := requestStatsV2Scope(r)
	if !allowed {
		writeError(w, http.StatusForbidden, "request statistics require a resolved identity")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "request metrics are unavailable")
		return
	}
	params := r.URL.Query()
	current := usagePeriodV2{PeriodStats: PeriodStats{Period: strings.TrimSpace(params.Get("current"))}}
	previous := usagePeriodV2{PeriodStats: PeriodStats{Period: strings.TrimSpace(params.Get("previous"))}}
	for _, period := range []*usagePeriodV2{&current, &previous} {
		var err error
		if len(period.Period) == len("2006-01-02") {
			period.Start, period.End, err = reconciliation.NaturalDay(period.Period, settings.SystemTimezoneName())
		} else {
			period.Start, period.End, err = parsePeriod(period.Period)
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, "current and previous must use YYYY-MM or YYYY-MM-DD")
			return
		}
	}
	if len(current.Period) != len(previous.Period) {
		writeError(w, http.StatusBadRequest, "current and previous must have the same calendar resolution")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), usageAggregateTimeout)
	defer cancel()
	result, err := metrics.ReadRequestPeriodComparison(ctx, h.db, metrics.MetricsFilter{
		TenantID: tenant, IncludeProbes: queryBool(r, "include_probes"), StartTime: current.Start, EndTime: current.End,
	}, previous.Start, previous.End, owner)
	if err != nil {
		if IsMissingRelationError(err) {
			writeError(w, http.StatusServiceUnavailable, "request metrics are not initialized")
			return
		}
		writeInternalErr(w, "request metrics period comparison failed", err)
		return
	}
	byDimension := map[string][]usageDimensionChangeV2{}
	if result.Ready {
		if len(result.Periods) != 2 || result.Periods[0].Ordinal != 0 || result.Periods[1].Ordinal != 1 {
			writeError(w, http.StatusInternalServerError, "request metrics returned incomplete period totals")
			return
		}
		setUsagePeriodV2Totals(&current, result.Periods[0])
		setUsagePeriodV2Totals(&previous, result.Periods[1])
		byDimension["model"] = usagePeriodModelChanges(result.Periods[0].ModelCostsUSD, result.Periods[1].ModelCostsUSD)
	}
	changeAbs := current.TotalCostUSD - previous.TotalCostUSD
	changePct := usagePercentageChange(current.TotalCostUSD, previous.TotalCostUSD)
	trend := "flat"
	if changePct != nil && *changePct > 5 {
		trend = "up"
	} else if changePct != nil && *changePct < -5 {
		trend = "down"
	}
	payload := map[string]any{
		"current": current, "previous": previous, "change_abs": changeAbs, "change_pct": changePct,
		"trend": trend, "significant": changePct != nil && math.Abs(*changePct) > 20, "by_dimension": byDimension,
		"source": "request_metrics", "timezone": settings.SystemTimezoneName(), "ready": result.Ready,
		"updated_at": result.UpdatedAt, "pending_since": result.PendingSince, "degraded": !result.Ready,
	}
	if !result.Ready {
		payload["degraded_reason"] = "request_metrics_backfill_incomplete"
	}
	writeJSON(w, http.StatusOK, payload)
}

func setUsagePeriodV2Totals(dst *usagePeriodV2, totals metrics.RequestPeriodTotals) {
	dst.TotalCostUSD, dst.TotalRequests, dst.TotalTokens = totals.CostUSD, totals.Requests, totals.Tokens
	dst.UniqueModels, dst.Credits, dst.NonUSDRows = totals.UniqueModels, totals.Credits, totals.NonUSDRows
	if dst.TotalRequests > 0 {
		dst.AvgCostPerReq = dst.TotalCostUSD / float64(dst.TotalRequests)
	}
}

func usagePercentageChange(current, previous float64) *float64 {
	if previous == 0 {
		return nil
	}
	change := (current - previous) / math.Abs(previous) * 100
	return &change
}

func usagePeriodModelChanges(current, previous map[string]float64) []usageDimensionChangeV2 {
	models := make(map[string]struct{}, len(current)+len(previous))
	for model := range current {
		models[model] = struct{}{}
	}
	for model := range previous {
		models[model] = struct{}{}
	}
	changes := make([]usageDimensionChangeV2, 0, len(models))
	for model := range models {
		c, p := current[model], previous[model]
		if c != 0 || p != 0 {
			changes = append(changes, usageDimensionChangeV2{DimensionValue: model, CurrentCost: c, PreviousCost: p, ChangePct: usagePercentageChange(c, p)})
		}
	}
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].CurrentCost != changes[j].CurrentCost {
			return changes[i].CurrentCost > changes[j].CurrentCost
		}
		if changes[i].PreviousCost != changes[j].PreviousCost {
			return changes[i].PreviousCost > changes[j].PreviousCost
		}
		return changes[i].DimensionValue < changes[j].DimensionValue
	})
	return changes[:min(len(changes), 10)]
}
