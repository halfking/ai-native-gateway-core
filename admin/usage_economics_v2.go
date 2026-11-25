package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/metrics"
)

type usageEconomicsMetaV2 struct {
	Source     string     `json:"source"`
	Start      time.Time  `json:"start"`
	End        time.Time  `json:"end"`
	DateFrom   string     `json:"date_from"`
	DateTo     string     `json:"date_to"`
	Timezone   string     `json:"timezone"`
	Ready      bool       `json:"ready"`
	UpdatedAt  *time.Time `json:"updated_at"`
	Pending    *time.Time `json:"pending_since"`
	Degraded   bool       `json:"degraded"`
	Reason     string     `json:"degraded_reason,omitempty"`
	Credits    int64      `json:"total_credits_charged"`
	NonUSDRows int64      `json:"cost_rows_outside_usd"`
}

type projectedEconomicsRow struct {
	Key               string  `json:"key"`
	Requests          int64   `json:"total_requests"`
	Failed            int64   `json:"failed_count"`
	PromptTokens      int64   `json:"total_prompt_tokens"`
	CompletionTokens  int64   `json:"total_completion_tokens"`
	Credits           int64   `json:"total_credits_charged"`
	CostUSD           float64 `json:"total_cost_usd"`
	NonUSDRows        int64   `json:"cost_rows_outside_usd"`
	Latency           float64 `json:"avg_latency_ms"`
	CacheRead         int64   `json:"cache_read_tokens"`
	CacheWrite        int64   `json:"cache_write_tokens"`
	EligibleCacheRead int64   `json:"eligible_cache_read_tokens"`
	CacheInput        int64   `json:"cache_input_tokens"`
	CacheScope        int64   `json:"cache_scope_requests"`
	CompressionScope  int64   `json:"compression_scope_requests"`
	Compressed        int64   `json:"compressed_requests"`
}

type projectedCostTrendEntry struct {
	DimensionValue   string   `json:"dimension_value"`
	RequestCount     int64    `json:"request_count"`
	TotalCostUSD     float64  `json:"total_cost_usd"`
	InputCostUSD     *float64 `json:"input_cost_usd"`
	OutputCostUSD    *float64 `json:"output_cost_usd"`
	Credits          int64    `json:"total_credits_charged"`
	NonUSDRows       int64    `json:"cost_rows_outside_usd"`
	PromptTokens     int64    `json:"prompt_tokens"`
	CompletionTokens int64    `json:"completion_tokens"`
	AvgLatencyMS     float64  `json:"avg_latency_ms"`
	ErrorRate        float64  `json:"error_rate"`
	Percentage       float64  `json:"percentage"`
}

type projectedCacheEconomics struct {
	usageEconomicsMetaV2
	Requests           int64    `json:"total_requests"`
	CacheRead          int64    `json:"cache_read_tokens"`
	CacheWrite         int64    `json:"cache_write_tokens"`
	PromptTokens       int64    `json:"prompt_tokens"`
	EligibleCacheRead  int64    `json:"eligible_cache_read_tokens"`
	CacheInput         int64    `json:"cache_input_tokens"`
	CacheScope         int64    `json:"cache_scope_requests"`
	CacheHitRatio      *float64 `json:"cache_hit_ratio"`
	CompressionScope   int64    `json:"compression_scope_requests"`
	Compressed         *int64   `json:"compressed_requests"`
	DollarsSpent       float64  `json:"dollars_spent"`
	DollarsSaved       *float64 `json:"dollars_saved"`
	EffectiveCostRatio *float64 `json:"effective_cost_ratio"`
	CompressionSaved   *float64 `json:"compression_saved"`
	TotalSaved         *float64 `json:"total_saved"`
	SavingsRate        *float64 `json:"savings_rate"`
}

func parseEconomicsQuery(r *http.Request, tenant string, cache bool) (metrics.RequestStatsQuery, string, error) {
	params := r.URL.Query()
	if params.Has("date_from") || params.Has("date_to") {
		if params.Has("start") || params.Has("end") {
			return metrics.RequestStatsQuery{}, "", fmt.Errorf("use either date_from/date_to or start/end")
		}
		params.Set("start", params.Get("date_from"))
		params.Set("end", params.Get("date_to"))
		r = r.Clone(r.Context())
		r.URL.RawQuery = params.Encode()
	}
	days := 7
	if cache {
		days = 30
	}
	query, err := parseRequestStatsV2WithDefaultDays(r, tenant, days)
	if err != nil {
		return query, "", err
	}
	if cache {
		query.Group = "cache-economics"
		return query, "", nil
	}
	group := params.Get("group_by")
	if group == "" {
		group = "model"
	}
	switch group {
	case "key", "api_key":
		query.Group = "cost-api_key"
	case "model", "provider", "tenant", "application", "work_type", "intent":
		query.Group = "cost-" + group
	default:
		return query, "", fmt.Errorf("unsupported cost group_by")
	}
	return query, group, nil
}

func (h *Handler) handleUsageEconomicsV2(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	tenant, owner, allowed := requestStatsV2Scope(r)
	if !allowed {
		writeError(w, http.StatusForbidden, "request statistics require a resolved identity")
		return
	}
	cache := strings.HasSuffix(r.URL.Path, "/cache-economics")
	query, group, err := parseEconomicsQuery(r, tenant, cache)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if owner != "" && group == "intent" {
		writeError(w, http.StatusForbidden, "session intent reporting requires administrator access")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "request metrics are unavailable")
		return
	}
	query.Owner = owner
	ctx, cancel := context.WithTimeout(r.Context(), usageAggregateTimeout)
	defer cancel()
	result, err := metrics.ReadRequestStats(ctx, h.db, query)
	if err != nil {
		if IsMissingRelationError(err) {
			writeError(w, http.StatusServiceUnavailable, "request metrics are not initialized")
			return
		}
		writeInternalErr(w, "request economics query failed", err)
		return
	}
	payload, err := projectedEconomicsPayload(result, group)
	if err != nil {
		writeInternalErr(w, "request economics response failed", err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func projectedEconomicsPayload(result metrics.RequestStats, group string) (any, error) {
	location, err := time.LoadLocation(result.Timezone)
	if err != nil {
		return nil, err
	}
	meta := usageEconomicsMetaV2{
		Source: result.Source, Start: result.Start, End: result.End, Timezone: result.Timezone,
		DateFrom: result.Start.In(location).Format(time.DateOnly), DateTo: result.End.Add(-time.Nanosecond).In(location).Format(time.DateOnly),
		Ready: result.Ready, UpdatedAt: result.UpdatedAt, Pending: result.PendingSince, Degraded: !result.Ready,
	}
	if !result.Ready {
		meta.Reason = "request_metrics_backfill_incomplete"
	}
	rows := make([]projectedEconomicsRow, len(result.Items))
	var cost float64
	for i, item := range result.Items {
		if err := json.Unmarshal(item, &rows[i]); err != nil {
			return nil, err
		}
		meta.Credits += rows[i].Credits
		meta.NonUSDRows += rows[i].NonUSDRows
		cost += rows[i].CostUSD
	}
	if group == "" {
		out := projectedCacheEconomics{usageEconomicsMetaV2: meta}
		if !result.Ready {
			return out, nil
		}
		if len(rows) != 1 {
			return nil, fmt.Errorf("cache economics expected one summary row")
		}
		row := rows[0]
		out.Requests, out.PromptTokens = row.Requests, row.PromptTokens
		out.CacheRead, out.CacheWrite, out.EligibleCacheRead = row.CacheRead, row.CacheWrite, row.EligibleCacheRead
		out.CacheInput, out.CacheScope, out.CompressionScope = row.CacheInput, row.CacheScope, row.CompressionScope
		out.DollarsSpent = row.CostUSD
		if row.CacheInput > 0 {
			ratio := float64(row.EligibleCacheRead) / float64(row.CacheInput)
			out.CacheHitRatio = &ratio
		}
		if row.CompressionScope > 0 || row.Requests == 0 {
			out.Compressed = &row.Compressed
		}
		return out, nil
	}
	entries := make([]projectedCostTrendEntry, 0, len(rows))
	var otherCost float64
	var otherCount int
	for _, row := range rows {
		entry := projectedCostTrendEntry{
			DimensionValue: row.Key, RequestCount: row.Requests, TotalCostUSD: row.CostUSD, Credits: row.Credits,
			NonUSDRows: row.NonUSDRows, PromptTokens: row.PromptTokens, CompletionTokens: row.CompletionTokens, AvgLatencyMS: row.Latency,
			ErrorRate: float64(row.Failed) / float64(row.Requests),
		}
		if cost > 0 {
			entry.Percentage = row.CostUSD / cost * 100
		}
		if entry.Percentage < 2 && len(entries) >= 10 {
			otherCost += entry.TotalCostUSD
			otherCount++
		} else {
			entries = append(entries, entry)
		}
	}
	return struct {
		usageEconomicsMetaV2
		Group      string                    `json:"group_by"`
		TotalCost  float64                   `json:"total_cost"`
		Entries    []projectedCostTrendEntry `json:"entries"`
		OtherCost  float64                   `json:"other_cost"`
		OtherCount int                       `json:"other_count"`
	}{meta, group, cost, entries, otherCost, otherCount}, nil
}
