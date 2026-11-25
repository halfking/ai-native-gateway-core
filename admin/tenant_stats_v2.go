package admin

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/kaixuan/llm-gateway-go/domains/metrics"
)

type tenantStatsV2 struct {
	accountProjectionMeta
	Days          int                    `json:"days"`
	TotalRequests int64                  `json:"total_requests"`
	TotalTokens   int64                  `json:"total_tokens"`
	TotalCredits  int64                  `json:"total_credits"`
	TotalCost     float64                `json:"total_cost_usd"`
	NonUSDRows    int64                  `json:"cost_rows_outside_usd"`
	UniqueKeys    int                    `json:"unique_keys"`
	UniqueModels  int                    `json:"unique_models"`
	UniqueApps    int                    `json:"unique_apps"`
	InputTokens   int64                  `json:"input_tokens"`
	OutputTokens  int64                  `json:"output_tokens"`
	CacheRead     int64                  `json:"cache_read_tokens"`
	CacheWrite    int64                  `json:"cache_write_tokens"`
	AvgLatencyMS  float64                `json:"avg_latency_ms"`
	ByModel       []tenantModelBreakdown `json:"by_model"`
	ByApplication []tenantAppBreakdown   `json:"by_application"`
	Daily         []tenantDailyStat      `json:"daily"`
}

func (h *Handler) handleTenantStatsV2(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	code := r.PathValue("code")
	if code == "" || (r.URL.Query().Get("tenant_id") != "" && r.URL.Query().Get("tenant_id") != code) {
		writeError(w, http.StatusBadRequest, "tenant does not match the requested statistics scope")
		return
	}
	query, err := parseRequestStatsV2WithDefaultDays(r, code, 7)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "request metrics are unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), usageAggregateTimeout)
	defer cancel()
	var exists bool
	if err := h.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM public.tenants WHERE code=$1)`, code).Scan(&exists); err != nil {
		writeTenantStatsError(w, ctx, code, "tenant existence check", err)
		return
	}
	if !exists {
		writeError(w, http.StatusNotFound, "tenant not found")
		return
	}
	result, err := metrics.ReadProjectedTenantActivity(ctx, h.db, query.MetricsFilter, query.Timezone)
	if err != nil {
		writeAccountUsageErrorV2(w, err)
		return
	}
	if !result.Ready {
		writeError(w, http.StatusServiceUnavailable, "request metrics backfill is running")
		return
	}
	payload, err := projectedTenantStatsPayload(result, query)
	if err != nil {
		writeInternalErr(w, "tenant request metrics response failed", err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func projectedTenantStatsPayload(result metrics.ProjectedAccountActivity, query metrics.RequestStatsQuery) (tenantStatsV2, error) {
	var computed struct {
		Kpi struct {
			userStatsKpi
			NonUSDRows   int64   `json:"cost_rows_outside_usd"`
			UniqueKeys   int     `json:"unique_keys"`
			UniqueModels int     `json:"unique_models"`
			UniqueApps   int     `json:"unique_apps"`
			InputTokens  int64   `json:"input_tokens"`
			OutputTokens int64   `json:"output_tokens"`
			CacheRead    int64   `json:"cache_read_tokens"`
			CacheWrite   int64   `json:"cache_write_tokens"`
			AvgLatencyMS float64 `json:"avg_latency_ms"`
		} `json:"kpi"`
		Daily  []tenantDailyStat `json:"daily"`
		Models []userStatsBucket `json:"top_models"`
		Apps   []userStatsBucket `json:"top_apps"`
	}
	if err := json.Unmarshal(result.Payload, &computed); err != nil {
		return tenantStatsV2{}, err
	}
	kpi := computed.Kpi
	out := tenantStatsV2{
		accountProjectionMeta: accountUsageSourceV2(result, query),
		Days:                  len(computed.Daily), TotalRequests: kpi.Requests, TotalTokens: kpi.Tokens, TotalCredits: kpi.Credits, TotalCost: kpi.CostUSD,
		NonUSDRows: kpi.NonUSDRows, UniqueKeys: kpi.UniqueKeys, UniqueModels: kpi.UniqueModels, UniqueApps: kpi.UniqueApps,
		InputTokens: kpi.InputTokens, OutputTokens: kpi.OutputTokens, CacheRead: kpi.CacheRead, CacheWrite: kpi.CacheWrite, AvgLatencyMS: kpi.AvgLatencyMS,
		ByModel: make([]tenantModelBreakdown, 0, len(computed.Models)), ByApplication: make([]tenantAppBreakdown, 0, len(computed.Apps)), Daily: computed.Daily,
	}
	for _, row := range computed.Models {
		out.ByModel = append(out.ByModel, tenantModelBreakdown{Model: row.Name, Requests: row.Requests, Tokens: row.Tokens, Credits: row.Credits, Cost: row.CostUSD})
	}
	for _, row := range computed.Apps {
		out.ByApplication = append(out.ByApplication, tenantAppBreakdown{AppCode: row.Name, Requests: row.Requests, Tokens: row.Tokens, Credits: row.Credits, Cost: row.CostUSD})
	}
	return out, nil
}
